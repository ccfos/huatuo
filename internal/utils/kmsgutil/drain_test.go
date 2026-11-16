package kmsgutil

import (
	"strings"
	"syscall"
	"testing"
)

// scriptedReader returns queued read results in order; each result carries
// the record content the "kernel" wrote.
type scriptedReader struct {
	results []readResult
	calls   int
}

type readResult struct {
	data string
	err  error
}

func (r *scriptedReader) read(buf []byte) (int, error) {
	res := r.results[r.calls]
	r.calls++
	n := copy(buf, res.data)
	return n, res.err
}

func TestDrainKmsgRecordsOversizedRecordDoesNotDiscardCapture(t *testing.T) {
	reader := &scriptedReader{results: []readResult{
		{data: "7,1,100,---;first record\n"},
		{err: syscall.EINVAL}, // oversized record: kernel consumed it
		{data: "7,2,200,---;second record\n"},
		{err: syscall.EAGAIN},
	}}
	got, err := drainKmsgRecords(reader.read, make([]byte, kmsgReadBufferSize))
	if err != nil {
		t.Fatalf("drainKmsgRecords() error = %v, want nil", err)
	}
	if !strings.Contains(got, "first record") || !strings.Contains(got, "second record") {
		t.Fatalf("capture lost records around the oversized one: %q", got)
	}
}

func TestDrainKmsgRecordsFatalErrorPropagates(t *testing.T) {
	reader := &scriptedReader{results: []readResult{
		{data: "7,1,100,---;first\n"},
		{err: syscall.EBADF},
	}}
	_, err := drainKmsgRecords(reader.read, make([]byte, kmsgReadBufferSize))
	if err != syscall.EBADF {
		t.Fatalf("drainKmsgRecords() error = %v, want EBADF", err)
	}
}

func TestDrainKmsgRecordsBufferCoversKernelRecordMax(t *testing.T) {
	// kernel PRINTK_MESSAGE_MAX is 2048 (header + escaped text + dict).
	if kmsgReadBufferSize < 2048 {
		t.Fatalf("kmsgReadBufferSize = %d, want >= 2048", kmsgReadBufferSize)
	}
}
