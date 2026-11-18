package req

import (
	"errors"
	"io"
	"os"
	"sync"

	"github.com/imroc/req/v3/internal/dump"
)

// DumpOptions controls the dump behavior.
type DumpOptions struct {
	Output               io.Writer
	RequestOutput        io.Writer
	ResponseOutput       io.Writer
	RequestHeaderOutput  io.Writer
	RequestBodyOutput    io.Writer
	ResponseHeaderOutput io.Writer
	ResponseBodyOutput   io.Writer
	RequestHeader        bool
	RequestBody          bool
	ResponseHeader       bool
	ResponseBody         bool
	Async                bool
}

// Clone return a copy of DumpOptions
func (do *DumpOptions) Clone() *DumpOptions {
	if do == nil {
		return nil
	}
	d := *do
	return &d
}

type dumpOptions struct {
	*DumpOptions
	scope *dumpScope
}

func (o dumpOptions) Output() io.Writer {
	if o.DumpOptions.Output == nil {
		return os.Stdout
	}
	return o.DumpOptions.Output
}

func (o dumpOptions) RequestHeaderOutput() io.Writer {
	if o.DumpOptions.RequestHeaderOutput != nil {
		return o.DumpOptions.RequestHeaderOutput
	}
	if o.DumpOptions.RequestOutput != nil {
		return o.DumpOptions.RequestOutput
	}
	return o.Output()
}

func (o dumpOptions) RequestBodyOutput() io.Writer {
	if o.DumpOptions.RequestBodyOutput != nil {
		return o.DumpOptions.RequestBodyOutput
	}
	if o.DumpOptions.RequestOutput != nil {
		return o.DumpOptions.RequestOutput
	}
	return o.Output()
}

func (o dumpOptions) ResponseHeaderOutput() io.Writer {
	if o.DumpOptions.ResponseHeaderOutput != nil {
		return o.DumpOptions.ResponseHeaderOutput
	}
	if o.DumpOptions.ResponseOutput != nil {
		return o.DumpOptions.ResponseOutput
	}
	return o.Output()
}

func (o dumpOptions) ResponseBodyOutput() io.Writer {
	if o.DumpOptions.ResponseBodyOutput != nil {
		return o.DumpOptions.ResponseBodyOutput
	}
	if o.DumpOptions.ResponseOutput != nil {
		return o.DumpOptions.ResponseOutput
	}
	return o.Output()
}

func (o dumpOptions) RequestHeader() bool {
	return o.DumpOptions.RequestHeader
}

func (o dumpOptions) RequestBody() bool {
	return o.DumpOptions.RequestBody
}

func (o dumpOptions) ResponseHeader() bool {
	return o.DumpOptions.ResponseHeader
}

func (o dumpOptions) ResponseBody() bool {
	return o.DumpOptions.ResponseBody
}

func (o dumpOptions) Async() bool {
	return o.DumpOptions.Async
}

func (o dumpOptions) Clone() dump.Options {
	// A dumper snapshot borrows its configuration scope. Only an owner with a
	// CloseDump entry point (Transport.Clone) may retain a separate file lease.
	return dumpOptions{DumpOptions: o.DumpOptions.Clone(), scope: o.scope}
}

func newDefaultDumpOptions() *DumpOptions {
	return &DumpOptions{
		Output:         os.Stdout,
		RequestBody:    true,
		ResponseBody:   true,
		ResponseHeader: true,
		RequestHeader:  true,
	}
}

func newDumper(opt *DumpOptions, scope *dumpScope) *dump.Dumper {
	if opt == nil {
		opt = newDefaultDumpOptions()
	}
	if opt.Output == nil {
		opt.Output = os.Stderr
	}
	return dump.NewDumper(dumpOptions{DumpOptions: opt, scope: scope})
}

// AcquireDump is optional: internal dump.Options remains compatible with caller
// implementations. The permit spans the actual write, including queued writes.
func (o dumpOptions) AcquireDump() (func(), bool) {
	return o.scope.acquire()
}

// dumpScope belongs to a configuration, not an HTTP attempt. Clones have their
// own admission/drain state and retain only the files opened by ToFile.
type dumpScope struct {
	mu        sync.Mutex
	cond      *sync.Cond
	active    int
	paused    bool
	replacing bool
	closed    bool
	finished  bool
	closeErr  error
	files     []*dumpFile
}

type dumpFile struct {
	file *os.File
	info os.FileInfo
	refs int
}

// Close may initialize an owner that has never configured dumping. Keep that
// one-time pointer publication separate from scope drain and OS I/O.
var dumpCloseInit sync.Mutex

var dumpFiles struct {
	sync.Mutex
	files []*dumpFile
}

func newDumpScope() *dumpScope {
	s := &dumpScope{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *dumpScope) acquire() (func(), bool) {
	if s == nil {
		return func() {}, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.paused {
		return nil, false
	}
	s.active++
	return func() {
		s.mu.Lock()
		s.active--
		s.cond.Broadcast()
		s.mu.Unlock()
	}, true
}

func (s *dumpScope) isClosed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *dumpScope) clone() *dumpScope {
	next := newDumpScope()
	if s == nil {
		return next
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		next.closed, next.finished, next.closeErr = true, true, s.closeErr
		return next
	}
	dumpFiles.Lock()
	for _, f := range s.files {
		f.refs++
		next.files = append(next.files, f)
	}
	dumpFiles.Unlock()
	return next
}

func (s *dumpScope) pause() {
	if s == nil {
		return
	}
	s.mu.Lock()
	for s.replacing {
		s.cond.Wait()
	}
	s.replacing = true
	s.paused = true
	for s.active != 0 {
		s.cond.Wait()
	}
	s.mu.Unlock()
}

func (s *dumpScope) resume() {
	if s != nil {
		s.mu.Lock()
		s.paused = false
		s.replacing = false
		s.cond.Broadcast()
		s.mu.Unlock()
	}
}

func (s *dumpScope) close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	for s.replacing {
		s.cond.Wait()
	}
	if s.closed {
		for !s.finished {
			s.cond.Wait()
		}
		err := s.closeErr
		s.mu.Unlock()
		return err
	}
	s.closed = true
	for s.active != 0 {
		s.cond.Wait()
	}
	files := s.files
	s.mu.Unlock()
	// No scope or dumper lock is held while closing an OS resource.
	var err error
	for _, f := range files {
		err = errors.Join(err, releaseDumpFile(f))
	}
	s.mu.Lock()
	s.closeErr = errors.Join(s.closeErr, err)
	s.finished = true
	s.cond.Broadcast()
	err = s.closeErr
	s.mu.Unlock()
	return err
}

func releaseDumpFile(f *dumpFile) error {
	dumpFiles.Lock()
	defer dumpFiles.Unlock()
	f.refs--
	if f.refs != 0 {
		return nil
	}
	for i, item := range dumpFiles.files {
		if item == f {
			dumpFiles.files = append(dumpFiles.files[:i], dumpFiles.files[i+1:]...)
			break
		}
	}
	return f.file.Close()
}

func dumpUsesFile(opt *DumpOptions, file *os.File) bool {
	for _, output := range []io.Writer{opt.Output, opt.RequestOutput, opt.ResponseOutput,
		opt.RequestHeaderOutput, opt.RequestBodyOutput, opt.ResponseHeaderOutput, opt.ResponseBodyOutput} {
		if f, ok := output.(*os.File); ok && f == file {
			return true
		}
	}
	return false
}

// replaceDump pauses and drains the old generation before modifying a file.
// Failed preparation keeps the old options/scope usable. An OS truncation or
// seek failure cannot promise rollback of bytes the OS already modified.
func replaceDump(old *dumpScope, opt *DumpOptions, filename *string) (*dumpScope, error) {
	var candidate *os.File
	var info os.FileInfo
	if filename != nil {
		var err error
		candidate, err = os.OpenFile(*filename, os.O_WRONLY|os.O_CREATE, 0666)
		if err != nil {
			return nil, err
		}
		info, err = candidate.Stat()
		if err != nil {
			return nil, errors.Join(err, candidate.Close())
		}
	}
	old.pause()
	defer old.resume()
	next := newDumpScope()
	var oldFiles []*dumpFile
	if old != nil {
		old.mu.Lock()
		if !old.closed {
			oldFiles = old.files
		}
		old.mu.Unlock()
	}
	dumpFiles.Lock()
	if candidate != nil {
		var shared *dumpFile
		for _, f := range dumpFiles.files {
			if os.SameFile(info, f.info) {
				shared = f
				break
			}
		}
		// A file retained by another owner must keep its bytes and offset.
		// The sole paused old owner may safely restart the same file.
		soleOld := false
		if shared != nil && old != nil && shared.refs == 1 {
			for _, f := range oldFiles {
				soleOld = soleOld || f == shared
			}
		}
		if shared == nil || soleOld {
			file := candidate
			if shared != nil {
				file = shared.file
			}
			err := file.Truncate(0)
			if err == nil {
				_, err = file.Seek(0, io.SeekStart)
			}
			if err != nil {
				dumpFiles.Unlock()
				return nil, errors.Join(err, candidate.Close())
			}
		}
		if shared != nil {
			if err := candidate.Close(); err != nil {
				dumpFiles.Unlock()
				return nil, err
			}
			shared.refs++
			next.files = append(next.files, shared)
			opt.Output = shared.file
		} else {
			f := &dumpFile{file: candidate, info: info, refs: 1}
			dumpFiles.files = append(dumpFiles.files, f)
			next.files = append(next.files, f)
			opt.Output = candidate
		}
	}
	if old != nil {
		for _, f := range oldFiles {
			if dumpUsesFile(opt, f.file) && (len(next.files) == 0 || next.files[0] != f) {
				f.refs++
				next.files = append(next.files, f)
			}
		}
	}
	dumpFiles.Unlock()
	// Retarget cleanup errors are returned by the new CloseDump, never injected
	// into an otherwise successful HTTP request or its retry policy.
	if old != nil {
		old.mu.Lock()
		old.replacing = false
		old.cond.Broadcast()
		old.mu.Unlock()
	}
	next.closeErr = old.close()
	return next, nil
}
