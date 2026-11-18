package dump_test

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imroc/req/v3/internal/dump"
)

// These options are immutable once the dumpers start. All output methods use
// the same bare buffer, so the tests exercise the dumper's writer protection.
type concurrencyOptions struct {
	output io.Writer
	async  bool
}

func (o concurrencyOptions) Output() io.Writer               { return o.output }
func (o concurrencyOptions) RequestHeaderOutput() io.Writer  { return o.output }
func (o concurrencyOptions) RequestBodyOutput() io.Writer    { return o.output }
func (o concurrencyOptions) ResponseHeaderOutput() io.Writer { return o.output }
func (o concurrencyOptions) ResponseBodyOutput() io.Writer   { return o.output }
func (o concurrencyOptions) RequestHeader() bool             { return true }
func (o concurrencyOptions) RequestBody() bool               { return true }
func (o concurrencyOptions) ResponseHeader() bool            { return true }
func (o concurrencyOptions) ResponseBody() bool              { return true }
func (o concurrencyOptions) Async() bool                     { return o.async }
func (o concurrencyOptions) Clone() dump.Options             { return o }

func waitForDump(t *testing.T, done <-chan struct{}, operation string) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func startDumpWorker(t *testing.T, d *dump.Dumper) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Start()
	}()
	t.Cleanup(func() {
		cleaned := make(chan struct{})
		go func() {
			defer close(cleaned)
			d.Stop()
			<-done
		}()
		waitForDump(t, cleaned, "async dumper cleanup")
	})
	return done
}

func checkDumpLines(t *testing.T, output string, want map[string]int) {
	t.Helper()
	got := make(map[string]int)
	for _, line := range strings.SplitAfter(output, "\n") {
		if line != "" {
			got[line]++
		}
	}
	if len(got) != len(want) {
		t.Errorf("dump contains %d distinct lines; want %d", len(got), len(want))
	}
	for line, count := range want {
		if got[line] != count {
			t.Errorf("dump line %q appeared %d times; want %d", line, got[line], count)
		}
	}
	for line := range got {
		if _, ok := want[line]; !ok {
			t.Errorf("unexpected dump line %q", line)
		}
	}
}

func dumpConcurrentLines(t *testing.T, dumpers []*dump.Dumper) map[string]int {
	t.Helper()
	const workers = 16
	const writes = 64
	want := make(map[string]int, workers*writes)
	for worker := 0; worker < workers; worker++ {
		for write := 0; write < writes; write++ {
			want[fmt.Sprintf("worker=%02d write=%03d\n", worker, write)] = 1
		}
	}
	start := make(chan struct{})
	done := make(chan struct{})
	var ready, finished sync.WaitGroup
	ready.Add(workers)
	finished.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func(worker int) {
			defer finished.Done()
			d := dumpers[worker%len(dumpers)]
			ready.Done()
			<-start
			for write := 0; write < writes; write++ {
				p := []byte(fmt.Sprintf("worker=%02d write=%03d\n", worker, write))
				switch write % 5 {
				case 0:
					d.DumpDefault(p)
				case 1:
					d.DumpRequestHeader(p)
				case 2:
					d.DumpRequestBody(p)
				case 3:
					d.DumpResponseHeader(p)
				case 4:
					d.DumpResponseBody(p)
				}
				// Async dumpers must own the bytes before Dump returns.
				for i := range p {
					p[i] = '!'
				}
			}
		}(worker)
	}
	go func() {
		ready.Wait()
		close(start)
		finished.Wait()
		close(done)
	}()
	waitForDump(t, done, "concurrent dump producers")
	return want
}

func TestDumperConcurrentSyncClones(t *testing.T) {
	var output bytes.Buffer
	d := dump.NewDumper(concurrencyOptions{output: &output})
	clone := d.Clone()
	dumpers := []*dump.Dumper{d, d, clone, clone.Clone()}
	want := dumpConcurrentLines(t, dumpers)
	checkDumpLines(t, output.String(), want)
}

// A value writer may contain slices and therefore cannot be a map key. It
// deliberately has no writer mutex, including across separately copied values.
type nonComparableWriter struct {
	output *bytes.Buffer
	tag    []byte
}

func (w nonComparableWriter) Write(p []byte) (int, error) {
	return w.output.Write(p)
}

func TestDumperConcurrentNonComparableWriter(t *testing.T) {
	var output bytes.Buffer
	writer := nonComparableWriter{output: &output, tag: []byte("first")}
	d := dump.NewDumper(concurrencyOptions{output: writer})
	other := dump.NewDumper(concurrencyOptions{
		output: nonComparableWriter{output: &output, tag: []byte("second")},
	})
	want := dumpConcurrentLines(t, []*dump.Dumper{d, d.Clone(), other, other.Clone()})
	checkDumpLines(t, output.String(), want)
}

func TestDumperConcurrentSyncAsyncClones(t *testing.T) {
	var output bytes.Buffer
	d := dump.NewDumper(concurrencyOptions{output: &output})
	async := d.Clone()
	async.SetOptions(concurrencyOptions{output: &output, async: true})
	asyncClone := async.Clone()
	asyncDone := startDumpWorker(t, async)
	cloneDone := startDumpWorker(t, asyncClone)
	want := dumpConcurrentLines(t, []*dump.Dumper{d, d.Clone(), async, asyncClone})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		async.Stop()
		// Stopping one clone must leave the other's queue and worker usable.
		asyncClone.DumpDefault([]byte("other clone remains active\n"))
		asyncClone.Stop()
	}()
	waitForDump(t, stopped, "independent clone Stop calls")
	waitForDump(t, asyncDone, "async worker exit")
	waitForDump(t, cloneDone, "async clone worker exit")
	want["other clone remains active\n"] = 1
	checkDumpLines(t, output.String(), want)
}

// This writer only controls when the first write may proceed. It provides no
// mutex around its buffer: the dumper remains responsible for serialization.
type firstWriteGate struct {
	output  bytes.Buffer
	entered chan struct{}
	release chan struct{}
	blockOn []byte
	once    sync.Once
}

func (w *firstWriteGate) Write(p []byte) (int, error) {
	if w.blockOn == nil || bytes.Equal(p, w.blockOn) {
		w.once.Do(func() {
			close(w.entered)
			<-w.release
		})
	}
	return w.output.Write(p)
}

func TestDumperAsyncCopiesQueuedData(t *testing.T) {
	w := &firstWriteGate{entered: make(chan struct{}), release: make(chan struct{})}
	d := dump.NewDumper(concurrencyOptions{output: w, async: true})
	workerDone := startDumpWorker(t, d)
	var release sync.Once
	openGate := func() { release.Do(func() { close(w.release) }) }
	defer openGate()
	firstSubmitted := make(chan struct{})
	go func() {
		defer close(firstSubmitted)
		d.DumpDefault([]byte("first\n"))
	}()
	waitForDump(t, w.entered, "first queued write")
	waitForDump(t, firstSubmitted, "first task submission")
	const queuedWrites = 20
	want := map[string]int{"first\n": 1}
	for i := 0; i < queuedWrites; i++ {
		want[fmt.Sprintf("queued=%02d\n", i)] = 1
	}
	queued := make(chan struct{})
	go func() {
		defer close(queued)
		for i := 0; i < queuedWrites; i++ {
			p := []byte(fmt.Sprintf("queued=%02d\n", i))
			d.DumpDefault(p)
			for j := range p {
				p[j] = '!'
			}
		}
	}()
	waitForDump(t, queued, "submissions filling the async queue")
	// The first task is blocked in Write, and another twenty have been accepted.
	// Releasing the writer lets the full queue drain without requiring a writer
	// lock to be held by the enqueue or Stop paths.
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		d.DumpDefault([]byte("beyond queue capacity\n"))
		d.Stop()
	}()
	openGate()
	waitForDump(t, stopped, "full queue producer and Stop")
	waitForDump(t, workerDone, "queued worker exit")
	want["beyond queue capacity\n"] = 1
	checkDumpLines(t, w.output.String(), want)
}

func TestDumperStopDrainsAndIsIdempotent(t *testing.T) {
	for _, async := range []bool{false, true} {
		name := "sync_without_start"
		if async {
			name = "async"
		}
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			var writer io.Writer = &output
			var gate *firstWriteGate
			var releaseOnce sync.Once
			open := func() {}
			if async {
				// Block the 48th write: all 64 submissions fit, with sixteen
				// admitted tasks still queued behind the blocked active write.
				gate = &firstWriteGate{entered: make(chan struct{}), release: make(chan struct{}), blockOn: []byte("accepted=47\n")}
				writer = gate
				open = func() { releaseOnce.Do(func() { close(gate.release) }) }
				defer open()
			}
			d := dump.NewDumper(concurrencyOptions{output: writer, async: async})
			var workerDone <-chan struct{}
			if async {
				workerDone = startDumpWorker(t, d)
			}
			const writes = 64
			want := make(map[string]int, writes)
			produced := make(chan struct{})
			go func() {
				defer close(produced)
				for i := 0; i < writes; i++ {
					d.DumpDefault([]byte(fmt.Sprintf("accepted=%02d\n", i)))
				}
			}()
			for i := 0; i < writes; i++ {
				want[fmt.Sprintf("accepted=%02d\n", i)] = 1
			}
			waitForDump(t, produced, "all pre-Stop submissions")
			if gate != nil {
				waitForDump(t, gate.entered, "admitted write blocking before Stop")
			}
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				d.Stop()
			}()
			if gate != nil {
				observation := time.NewTimer(100 * time.Millisecond)
				select {
				case <-stopped:
					observation.Stop()
					t.Fatal("Stop returned while an admitted write and sixteen queued tasks were blocked")
				case <-observation.C:
				}
				open()
			}
			waitForDump(t, stopped, "initial Stop")
			// Stop promises all accepted writes are finished, so reading the bare
			// buffer is safe immediately on return, before observing worker exit.
			snapshot := func() string {
				if gate != nil {
					return gate.output.String()
				}
				return output.String()
			}
			checkDumpLines(t, snapshot(), want)
			if async {
				waitForDump(t, workerDone, "stopped worker exit")
			}
			const callers = 32
			var finished sync.WaitGroup
			finished.Add(callers)
			start := make(chan struct{})
			for i := 0; i < callers; i++ {
				go func() {
					defer finished.Done()
					<-start
					d.Stop()
					d.DumpDefault([]byte("late data must be discarded\n"))
				}()
			}
			lateDone := make(chan struct{})
			go func() {
				finished.Wait()
				close(lateDone)
			}()
			close(start)
			waitForDump(t, lateDone, "concurrent repeated Stop and late submissions")
			checkDumpLines(t, snapshot(), want)
		})
	}
}
