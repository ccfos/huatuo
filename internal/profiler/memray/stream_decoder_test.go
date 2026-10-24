// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package memray

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"testing"
	"time"
)

// TODO: Add recorded allocation/free and malformed/truncated stream fixtures
// so changes to the private Memray stream ABI are detected before release.

// Notify the test only after NextRecord reaches the missing record body.
type memrayRecordBodyReader struct {
	io.Reader
	waiting chan struct{}
}

func (r *memrayRecordBodyReader) Read(p []byte) (int, error) {
	if r.waiting != nil {
		close(r.waiting)
		r.waiting = nil
	}
	return r.Reader.Read(p)
}

func TestFlushDeltaWhileRecordBodyIsBlocked(t *testing.T) {
	contextSwitch := binary.LittleEndian.AppendUint64([]byte{recordTypeContextSwitch}, 2)
	segmentHeader := append([]byte{recordTypeSegmentHeader, 0, 1}, make([]byte, 8)...)
	segmentHeader = append(segmentHeader, recordTypeSegment)
	segmentHeader = append(segmentHeader, make([]byte, 8)...)
	segmentHeader = append(segmentHeader, 1)
	for _, test := range []struct {
		name   string
		record []byte
	}{
		{"context switch", contextSwitch},
		{"frame push", []byte{recordTypeFramePush | 1, 1, 0}},
		{"code object", []byte{recordTypeCodeObject, 2, 'f', 0, 0, 0, 0}},
		{"native frame", []byte{recordTypeNativeTraceIndex, 2, 0}},
		{"allocation", []byte{recordTypeAllocation | (15 << 3) | 2, 2, 128, 1}},
		{"thread name", []byte{recordTypeThreadRecord, 't', 0}},
		{"memory record", []byte{recordTypeMemoryRecord, 1, 1}},
		{"segment header", segmentHeader},
		{"object", []byte{recordTypeObject | (15 << 1), 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Decode a real allocation before leaving the next record incomplete.
			rd := &reader{
				r:            bytes.NewReader([]byte{recordTypeAllocation | (15 << 3) | 2, 2, 64}),
				opt:          Options{Separator: ";"},
				lastThreadID: 1,
				codeObjects:  map[uint64]codeObject{1: {Func: "main"}},
				simpleAllocs: make(map[uint64]liveAlloc),
			}
			frameID, err := packPythonFrameID(1, true)
			if err != nil {
				t.Fatal(err)
			}
			rd.threadStacks = map[uint64][]uint64{1: {rd.frameTree.getTraceIndex(0, frameID)}}
			decoder := &StreamDecoder{rd: rd, deltaAgg: make(map[locationKey]deltaValue)}
			if ok, err := decoder.NextRecord(); !ok || err != nil {
				t.Fatalf("decode allocation: ok=%v err=%v", ok, err)
			}

			writer, reader := net.Pipe()
			waiting := make(chan struct{})
			split := len(test.record) - 1
			rd.r = io.MultiReader(bytes.NewReader(test.record[:split]), &memrayRecordBodyReader{
				Reader: reader, waiting: waiting,
			})
			readDone := make(chan struct{})
			readErr := make(chan error, 1)
			go func() {
				defer close(readDone)
				_, err := decoder.NextRecord()
				readErr <- err
			}()
			t.Cleanup(func() {
				_ = writer.Close()
				_ = reader.Close()
				<-readDone
			})
			select {
			case <-waiting:
			case <-time.After(time.Second):
				t.Fatal("decoder did not reach the incomplete record body")
			}

			flushDone := make(chan struct{})
			lines := make(chan []string, 1)
			go func() {
				defer close(flushDone)
				lines <- decoder.FlushDelta("")
			}()
			t.Cleanup(func() {
				_ = reader.Close()
				<-flushDone
			})
			select {
			case got := <-lines:
				if len(got) != 1 || got[0] != "tid 1;main 64" {
					t.Fatalf("FlushDelta() = %v, want [tid 1;main 64]", got)
				}
			case <-time.After(time.Second):
				t.Fatal("FlushDelta blocked on an incomplete record")
			}
			if err := writer.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(test.record[split:]); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-readErr:
				if err != nil {
					t.Fatalf("finish record: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("decoder did not finish the record")
			}
		})
	}
}

func TestStreamDecoderRejectsTruncatedRecords(t *testing.T) {
	contextSwitch := binary.LittleEndian.AppendUint64([]byte{recordTypeContextSwitch}, 1)
	segmentHeader := append([]byte{recordTypeSegmentHeader, 'f', 0, 1}, make([]byte, 8)...)
	segmentHeader = append(segmentHeader, recordTypeSegment)
	segmentHeader = append(segmentHeader, make([]byte, 8)...)
	segmentHeader = append(segmentHeader, 128, 1)
	for _, test := range []struct {
		name         string
		record       []byte
		nativeTraces bool
	}{
		{"allocation", []byte{recordTypeAllocation | (15 << 3) | 2, 2, 128, 1}, false},
		{"free", []byte{recordTypeAllocation | (15 << 3) | 1, 0}, false},
		{"native allocation", []byte{recordTypeAllocation | (15 << 3) | 2, 2, 0, 128, 1}, true},
		{"ranged allocation", []byte{recordTypeAllocation | (15 << 3), 2, 14, 128, 1}, false},
		{"ranged free", []byte{recordTypeAllocation | (15 << 3), 2, 15, 128, 1}, false},
		{"context switch", contextSwitch, false},
		{"frame push", []byte{recordTypeFramePush | 1, 1, 128, 1}, false},
		{"code object", []byte{recordTypeCodeObject, 1, 'f', 0, 'p', 0, 2, 2, 1, 2}, false},
		{"native frame", []byte{recordTypeNativeTraceIndex, 128, 1, 0}, true},
		{"thread name", []byte{recordTypeThreadRecord, 't', 0}, false},
		{"memory record", []byte{recordTypeMemoryRecord, 128, 1, 2}, false},
		{"segment header", segmentHeader, false},
		{"object", []byte{recordTypeObject | (15 << 1), 2}, false},
		{"native object", []byte{recordTypeObject | (15 << 1) | 1, 2, 0}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Cut at every byte, including field boundaries where io.ReadFull
			// returns EOF instead of ErrUnexpectedEOF. Also accept the full record.
			for end := 1; end <= len(test.record); end++ {
				t.Run(fmt.Sprintf("bytes=%d", end), func(t *testing.T) {
					stream := []byte{recordTypeAllocation | (15 << 3) | 2, 2}
					if test.nativeTraces {
						stream = append(stream, 0)
					}
					stream = append(stream, 64)
					stream = append(stream, test.record[:end]...)
					decoder := &StreamDecoder{
						rd: &reader{
							r:            bytes.NewReader(stream),
							opt:          Options{Separator: ";"},
							header:       Header{NativeTraces: test.nativeTraces},
							threadStacks: make(map[uint64][]uint64),
							codeObjects:  make(map[uint64]codeObject),
							simpleAllocs: make(map[uint64]liveAlloc),
						},
						deltaAgg: make(map[locationKey]deltaValue),
					}
					if ok, err := decoder.NextRecord(); !ok || err != nil {
						t.Fatalf("decode initial allocation: ok=%v err=%v", ok, err)
					}
					ok, err := decoder.NextRecord()
					if end == len(test.record) {
						if !ok || err != nil {
							t.Fatalf("decode complete record: ok=%v err=%v", ok, err)
						}
						if ok, err := decoder.NextRecord(); ok || !errors.Is(err, io.ErrUnexpectedEOF) {
							t.Fatalf("EOF without trailer: ok=%v err=%v, want unexpected EOF", ok, err)
						}
						if test.name == "free" && len(decoder.FlushDelta("")) != 0 {
							t.Fatal("complete free did not cancel the initial allocation")
						}
						return
					}
					if ok || !errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
						t.Fatalf("decode truncated record: ok=%v err=%v, want unexpected EOF", ok, err)
					}
					if got := decoder.FlushDelta(""); len(got) != 1 || got[0] != "tid 0 64" {
						t.Fatalf("truncated record changed earlier allocations: %v", got)
					}
				})
			}
		})
	}
}

func TestStreamDecoderAcceptsTrailer(t *testing.T) {
	decoder := &StreamDecoder{rd: &reader{r: bytes.NewReader([]byte{recordTypeTrailer})}}
	if ok, err := decoder.NextRecord(); ok || err != nil {
		t.Fatalf("decode trailer: ok=%v err=%v", ok, err)
	}
}

func TestStreamDecoderRejectsEOFWithoutRecords(t *testing.T) {
	decoder := &StreamDecoder{rd: &reader{r: bytes.NewReader(nil)}}
	if ok, err := decoder.NextRecord(); ok || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("EOF without trailer: ok=%v err=%v, want unexpected EOF", ok, err)
	}
}

func TestStreamDecoderRejectsIncompleteFreeOnSocketClose(t *testing.T) {
	writer, socketReader := net.Pipe()
	waiting := make(chan struct{})
	decoder := &StreamDecoder{
		rd: &reader{
			r: io.MultiReader(
				// malloc(addr=8, size=64), then a free with its address missing.
				bytes.NewReader([]byte{recordTypeAllocation | (15 << 3) | 2, 2, 64, recordTypeAllocation | (15 << 3) | 1}),
				&memrayRecordBodyReader{Reader: socketReader, waiting: waiting},
			),
			opt:          Options{Separator: ";"},
			simpleAllocs: make(map[uint64]liveAlloc),
		},
		deltaAgg: make(map[locationKey]deltaValue),
	}
	defer writer.Close()
	defer socketReader.Close()
	if ok, err := decoder.NextRecord(); !ok || err != nil {
		t.Fatalf("decode allocation: ok=%v err=%v", ok, err)
	}
	readDone := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		defer close(readDone)
		_, err := decoder.NextRecord()
		readErr <- err
	}()
	t.Cleanup(func() {
		_ = socketReader.Close()
		<-readDone
	})
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("decoder did not reach the missing free address")
	}
	// Match the shutdown watchdog closing a stalled stream. The 64-byte live
	// balance is now incomplete, so reader completion must fail, not return nil.
	_ = socketReader.Close()
	select {
	case err := <-readErr:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("incomplete free error = %v, want closed pipe", err)
		}
	case <-time.After(time.Second):
		t.Fatal("decoder remained blocked after socket close")
	}
	if got := decoder.FlushDelta(""); len(got) != 1 || got[0] != "tid 0 64" {
		t.Fatalf("incomplete free changed earlier allocations: %v", got)
	}
}

func TestResolveNativeSymbolPID(t *testing.T) {
	const (
		headerPID = int32(1)
		hostPID   = int32(12345)
	)

	if got := resolveNativeSymbolPID(Options{}, headerPID); got != headerPID {
		t.Fatalf("default symbol PID = %d, want %d", got, headerPID)
	}
	if got := resolveNativeSymbolPID(
		Options{NativeSymbolPID: hostPID},
		headerPID,
	); got != hostPID {
		t.Fatalf("overridden symbol PID = %d, want %d", got, hostPID)
	}
}

func TestNewStreamDecoderRejectsOversizedHeader(t *testing.T) {
	writer, reader := net.Pipe()
	defer writer.Close()
	defer reader.Close()

	header := make([]byte, 0, maxCStringBytes+50)
	header = append(header, []byte("memray\x00")...)
	header = binary.LittleEndian.AppendUint32(header, 12)
	header = binary.LittleEndian.AppendUint32(header, 0)
	header = append(header, 0, fileFormatAllAllocations)
	header = append(header, make([]byte, 32)...)
	header = append(header, bytes.Repeat([]byte{'x'}, maxCStringBytes+1)...)

	writeErr := make(chan error, 1)
	go func() {
		_, err := writer.Write(header)
		writeErr <- err
	}()

	_, _, err := NewStreamDecoder(reader, Options{})
	if err == nil || !strings.Contains(err.Error(), "C string exceeds") {
		t.Fatalf("NewStreamDecoder() error = %v, want oversized C string error", err)
	}

	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatalf("write header: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("header writer did not finish")
	}
}

func TestStreamDecoderHandlesOversizedSkipFrames(t *testing.T) {
	headerBytes := make([]byte, 0, 128)
	headerBytes = append(headerBytes, []byte("memray\x00")...)
	headerBytes = binary.LittleEndian.AppendUint32(headerBytes, 12)
	headerBytes = binary.LittleEndian.AppendUint32(headerBytes, 0)
	headerBytes = append(headerBytes, 0, fileFormatAllAllocations)
	headerBytes = append(headerBytes, make([]byte, 32)...)
	headerBytes = append(headerBytes, 0)
	headerBytes = binary.LittleEndian.AppendUint32(headerBytes, 1)
	headerBytes = binary.LittleEndian.AppendUint64(headerBytes, 1)
	headerBytes = binary.LittleEndian.AppendUint64(headerBytes, math.MaxUint64)
	headerBytes = append(headerBytes, 0, 0, 0)

	decoder, header, err := NewStreamDecoder(bytes.NewReader(headerBytes), Options{})
	if err != nil {
		t.Fatalf("NewStreamDecoder() error = %v", err)
	}
	frameID, err := packPythonFrameID(1, true)
	if err != nil {
		t.Fatalf("packPythonFrameID() error = %v", err)
	}
	decoder.rd.codeObjects[1] = codeObject{Func: "main"}
	frameIndex := decoder.rd.frameTree.getTraceIndex(0, frameID)

	got := decoder.rd.stackForLocation(locationKey{
		PythonFrameID: frameIndex,
		ThreadID:      header.MainTid,
	})
	if got != "" {
		t.Fatalf("stackForLocation() = %q, want empty stack", got)
	}
}
