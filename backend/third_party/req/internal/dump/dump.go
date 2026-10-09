package dump

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"reflect"
	"sync"
)

// Options controls the dump behavior.
type Options interface {
	Output() io.Writer
	RequestHeaderOutput() io.Writer
	RequestBodyOutput() io.Writer
	ResponseHeaderOutput() io.Writer
	ResponseBodyOutput() io.Writer
	RequestHeader() bool
	RequestBody() bool
	ResponseHeader() bool
	ResponseBody() bool
	Async() bool
	Clone() Options
}

func (d *Dumper) WrapResponseBodyReadCloser(rc io.ReadCloser) io.ReadCloser {
	return &dumpResponseBodyReadCloser{rc, d}
}

type dumpResponseBodyReadCloser struct {
	io.ReadCloser
	dump *Dumper
}

func (r *dumpResponseBodyReadCloser) Read(p []byte) (n int, err error) {
	n, err = r.ReadCloser.Read(p)
	r.dump.DumpResponseBody(p[:n])
	if err == io.EOF {
		r.dump.DumpDefault([]byte("\r\n"))
	}
	return
}

func (d *Dumper) WrapRequestBodyWriteCloser(rc io.WriteCloser) io.WriteCloser {
	return &dumpRequestBodyWriteCloser{rc, d}
}

type dumpRequestBodyWriteCloser struct {
	io.WriteCloser
	dump *Dumper
}

func (w *dumpRequestBodyWriteCloser) Write(p []byte) (n int, err error) {
	n, err = w.WriteCloser.Write(p)
	w.dump.DumpRequestBody(p[:n])
	return
}

type dumpRequestHeaderWriter struct {
	w    io.Writer
	dump *Dumper
}

func (w *dumpRequestHeaderWriter) Write(p []byte) (n int, err error) {
	n, err = w.w.Write(p)
	w.dump.DumpRequestHeader(p[:n])
	return
}

func (d *Dumper) WrapRequestHeaderWriter(w io.Writer) io.Writer {
	return &dumpRequestHeaderWriter{
		w:    w,
		dump: d,
	}
}

type dumpRequestBodyWriter struct {
	w    io.Writer
	dump *Dumper
}

func (w *dumpRequestBodyWriter) Write(p []byte) (n int, err error) {
	n, err = w.w.Write(p)
	w.dump.DumpRequestBody(p[:n])
	return
}

func (d *Dumper) WrapRequestBodyWriter(w io.Writer) io.Writer {
	return &dumpRequestBodyWriter{
		w:    w,
		dump: d,
	}
}

// GetResponseHeaderDumpers return Dumpers which need dump response header.
func GetResponseHeaderDumpers(ctx context.Context, dump *Dumper) Dumpers {
	dumpers := GetDumpers(ctx, dump)
	var ds []*Dumper
	for _, d := range dumpers {
		if d.ResponseHeader() {
			ds = append(ds, d)
		}
	}
	return Dumpers(ds)
}

// Dumpers is an array of Dumpper
type Dumpers []*Dumper

// ShouldDump is true if Dumper is not empty.
func (ds Dumpers) ShouldDump() bool {
	return len(ds) > 0
}

func (ds Dumpers) DumpResponseHeader(p []byte) {
	for _, d := range ds {
		d.DumpResponseHeader(p)
	}
}

// Buffer provides synchronized snapshots and per-attempt output sinks. Reset
// retires prior sinks, so a canceled HTTP/2 upload cannot append to a retry.
type Buffer struct {
	mu         sync.Mutex
	data       bytes.Buffer
	generation uint64
}

type bufferSink struct {
	buffer     *Buffer
	generation uint64
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func (b *Buffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.generation++
	b.data.Reset()
}

func (b *Buffer) Writer() io.Writer {
	b.mu.Lock()
	defer b.mu.Unlock()
	return &bufferSink{buffer: b, generation: b.generation}
}

func (w *bufferSink) Write(p []byte) (int, error) {
	b := w.buffer
	b.mu.Lock()
	defer b.mu.Unlock()
	if w.generation != b.generation {
		return len(p), nil
	}
	return b.data.Write(p)
}

// Only active writes retain a registry entry. The registry mutex is never held
// during I/O. Comparable writer identities share a lock across independent
// dumpers and clones; non-comparable values share a lock only by dynamic type.
type outputKey struct {
	writer   io.Writer
	typeOnly reflect.Type
}

type outputLock struct {
	mu    sync.Mutex
	users int
}

var outputs = struct {
	sync.Mutex
	active map[outputKey]*outputLock
}{active: make(map[outputKey]*outputLock)}

func writeOutput(output io.Writer, p []byte) {
	key := outputKey{writer: output}
	if value := reflect.ValueOf(output); !value.Comparable() {
		key = outputKey{typeOnly: value.Type()}
	}
	outputs.Lock()
	lock := outputs.active[key]
	if lock == nil {
		lock = &outputLock{}
		outputs.active[key] = lock
	}
	lock.users++
	outputs.Unlock()
	lock.mu.Lock()
	defer func() {
		lock.mu.Unlock()
		outputs.Lock()
		lock.users--
		if lock.users == 0 {
			delete(outputs.active, key)
		}
		outputs.Unlock()
	}()
	output.Write(p)
}

// Dumper is the dump tool. Options must be configured before concurrent use.
type Dumper struct {
	Options
	mu      sync.Mutex
	cond    *sync.Cond
	queue   []*dumpTask
	running bool
	stopped bool
	writes  int
}

type dumpTask struct {
	Data   []byte
	Output io.Writer
}

// NewDumper creates a Dumper. Async workers start on demand and retire when
// idle; request-level dumping therefore needs no separate Start/Stop lifecycle.
func NewDumper(opt Options) *Dumper {
	d := &Dumper{Options: opt}
	d.cond = sync.NewCond(&d.mu)
	return d
}

func (d *Dumper) SetOptions(opt Options) {
	d.Options = opt
}

func (d *Dumper) Clone() *Dumper {
	if d == nil {
		return nil
	}
	return NewDumper(d.Options.Clone())
}

func (d *Dumper) DumpTo(p []byte, output io.Writer) {
	if len(p) == 0 || output == nil {
		return
	}
	d.mu.Lock()
	if d.Async() {
		for len(d.queue) >= 20 && !d.stopped {
			d.cond.Wait()
		}
		if d.stopped {
			d.mu.Unlock()
			return
		}
		data := append([]byte(nil), p...)
		d.queue = append(d.queue, &dumpTask{Data: data, Output: output})
		d.startLocked()
		d.mu.Unlock()
		return
	}
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.writes++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.writes--
		d.cond.Broadcast()
		d.mu.Unlock()
	}()
	writeOutput(output, p)
}

// startLocked starts at most one worker and requires d.mu.
func (d *Dumper) startLocked() {
	if d.running || len(d.queue) == 0 {
		return
	}
	d.running = true
	go d.run()
}

func (d *Dumper) run() {
	for {
		d.mu.Lock()
		if len(d.queue) == 0 {
			d.running = false
			d.cond.Broadcast()
			d.mu.Unlock()
			return
		}
		task := d.queue[0]
		d.queue[0] = nil
		d.queue = d.queue[1:]
		d.cond.Broadcast()
		d.mu.Unlock()
		writeOutput(task.Output, task.Data)
	}
}

func (d *Dumper) DumpDefault(p []byte) {
	d.DumpTo(p, d.Output())
}

func (d *Dumper) DumpRequestHeader(p []byte) {
	d.DumpTo(p, d.RequestHeaderOutput())
}

func (d *Dumper) DumpRequestBody(p []byte) {
	d.DumpTo(p, d.RequestBodyOutput())
}

func (d *Dumper) DumpResponseHeader(p []byte) {
	d.DumpTo(p, d.ResponseHeaderOutput())
}

func (d *Dumper) DumpResponseBody(p []byte) {
	d.DumpTo(p, d.ResponseBodyOutput())
}

// Stop rejects new work and waits for this dumper's admitted writes. It is
// idempotent and does not stop or drain a clone's independent queue.
func (d *Dumper) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	d.cond.Broadcast()
	for d.running || d.writes != 0 {
		d.cond.Wait()
	}
}

// Start is retained for transport callers. On-demand workers do not need an
// idle goroutine and repeated Start calls do not create additional workers.
func (d *Dumper) Start() {
	d.mu.Lock()
	d.startLocked()
	d.mu.Unlock()
}

type dumperKeyType int

const DumperKey dumperKeyType = iota

func GetDumpers(ctx context.Context, dump *Dumper) []*Dumper {
	dumps := []*Dumper{}
	if dump != nil {
		dumps = append(dumps, dump)
	}
	if ctx == nil {
		return dumps
	}
	if d, ok := ctx.Value(DumperKey).(*Dumper); ok {
		dumps = append(dumps, d)
	}
	return dumps
}

func WrapResponseBodyIfNeeded(res *http.Response, req *http.Request, dump *Dumper) {
	dumps := GetDumpers(req.Context(), dump)
	for _, d := range dumps {
		if d.ResponseBody() {
			res.Body = d.WrapResponseBodyReadCloser(res.Body)
		}
	}
}
