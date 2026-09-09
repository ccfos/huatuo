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
	"math"
	"net"
	"strings"
	"testing"
	"time"
)

// TODO: Add recorded allocation/free and malformed/truncated stream fixtures
// so changes to the private Memray stream ABI are detected before release.

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
