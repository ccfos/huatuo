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

package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"capnproto.org/go/capnp/v3"

	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/stretchr/testify/assert"
)

func TestBPFFilesystemIOLayout(t *testing.T) {
	t.Parallel()

	var record bpfFilesystemIO

	assert.Equal(t, uintptr(376), unsafe.Sizeof(record))
	assert.Equal(t, uintptr(0), unsafe.Offsetof(record.TGID))
	assert.Equal(t, uintptr(4), unsafe.Offsetof(record.PathInitialized))
	assert.Equal(t, uintptr(8), unsafe.Offsetof(record.DevID))
	assert.Equal(t, uintptr(16), unsafe.Offsetof(record.FsWriteBytes))
	assert.Equal(t, uintptr(64), unsafe.Offsetof(record.Latency))
	assert.Equal(t, uintptr(104), unsafe.Offsetof(record.Comm))
	assert.Equal(t, uintptr(120), unsafe.Offsetof(record.PathSegs))
}

func TestBPFFilesystemIOPathName(t *testing.T) {
	t.Parallel()

	t.Run("missing path", func(t *testing.T) {
		record := bpfFilesystemIO{Ino: 1}

		assert.Empty(t, record.PathName())
	})

	t.Run("captured path", func(t *testing.T) {
		record := bpfFilesystemIO{Ino: 1}
		copy(record.PathSegs[0][:], "io")
		copy(record.PathSegs[1][:], "huatuo-iotracing.test")
		copy(record.PathSegs[2][:], "/")

		assert.Equal(t, "/huatuo-iotracing.test/io", record.PathName())
	})
}

func TestReportAfterCollectionCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := toolstream.NewClient(&toolstream.ClientOptions{
		SockPath:    path,
		ServiceName: "iotracing",
		Version:     "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	traceErr := errors.New("collection failed after snapshot")
	if err := writeToolstreamReport(ctx, client, &types.IOTracingSnapshot{}, traceErr); !errors.Is(err, traceErr) || errors.Is(err, toolstream.ErrOutputCanceled) {
		t.Fatalf("report = %v, want collection error only", err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	decoder := capnp.NewDecoder(peer)
	message, err := decoder.Decode()
	if err != nil {
		t.Fatal(err)
	}
	message.Release()
	for _, wantEnd := range []bool{false, true} {
		message, err := decoder.Decode()
		if err != nil {
			t.Fatal(err)
		}
		root, err := transport.ReadRootMessage(message)
		if err != nil {
			message.Release()
			t.Fatal(err)
		}
		chunk, err := root.Chunk()
		if err != nil {
			message.Release()
			t.Fatal(err)
		}
		gotEnd := chunk.End()
		message.Release()
		if gotEnd != wantEnd {
			t.Fatalf("End = %v, want %v", gotEnd, wantEnd)
		}
	}
}

func TestReportDeadlineUnblocksWrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := toolstream.NewClient(&toolstream.ClientOptions{
		SockPath:    path,
		ServiceName: "iotracing",
		Version:     "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	done := make(chan error, 1)
	go func() {
		done <- writeToolstreamReport(t.Context(), client, &types.IOTracingSnapshot{
			StallStacks: []types.IOScheduleEvent{{Comm: strings.Repeat("x", 1<<20)}},
		}, nil)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, toolstream.ErrOutputTimeout) || errors.Is(err, toolstream.ErrOutputCanceled) {
			t.Fatalf("report = %v, want timeout", err)
		}
	case <-time.After(toolstream.DefaultSendTimeout + time.Second):
		_ = client.Close()
		<-done
		t.Fatal("report finalization did not return after the send timeout")
	}
}
