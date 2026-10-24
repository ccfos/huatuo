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

// Package vsock implements the Sandbox write-only tracing Storage backend.
package vsock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	capnp "capnproto.org/go/capnp/v3"

	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

func waitConnected(t *testing.T, b *Backend) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.mutex.Lock()
		ready := b.connected
		b.mutex.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("sender did not connect")
}

// TestBackendInit 验证写入型后端接受集合和索引初始化，无需创建本地资源。
func TestBackendInit(t *testing.T) {
	backend := &Backend{}
	if err := backend.Init(t.Context(), "checkout-traces", []driver.Index{}); err != nil {
		t.Fatal(err)
	}
}

func TestQueueAndCloseDrain(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 4)
	server, err := transport.Serve(listener, func(session *transport.Session, chunk transport.ChunkMsg) {
		if session.ToolName != TracingTool {
			t.Errorf("tool=%q", session.ToolName)
		}
		if len(chunk.Data) > 0 {
			received <- string(chunk.Data)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	b, err := New(Options{ToolName: TracingTool, DialContext: func(ctx context.Context, _ uint32) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitConnected(t, b)
	data := []byte(`{"tracer_id":"guest-observation"}`)
	if err := b.Save(context.Background(), driver.Record{Data: data}, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	data[0] = '!'
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got != `{"tracer_id":"guest-observation"}` {
			t.Errorf("received %q", got)
		}
	case <-ctx.Done():
		t.Fatal("queued document was not drained")
	}
	if err := b.Save(ctx, driver.Record{Data: data}, driver.SaveOptions{}); !errors.Is(err, ErrClosed) {
		t.Errorf("save after close=%v", err)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestQueueLimitsAndUnsupportedGuarantees(t *testing.T) {
	b := &Backend{
		options:   Options{MaxMessageSize: 128, MaxPendingBytes: 128},
		connected: true,
		notify:    make(chan struct{}, 1),
	}
	rec := driver.Record{Data: []byte(`{"value":"bounded"}`)}
	if err := b.Save(
		context.Background(),
		rec,
		driver.SaveOptions{WaitForVisibility: true},
	); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("visibility=%v", err)
	}
	if err := b.Save(context.Background(), rec, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(context.Background(), rec, driver.SaveOptions{}); err == nil {
		t.Error("queue overflow accepted")
	}
	if err := b.Save(context.Background(), driver.Record{Data: make([]byte, 129)}, driver.SaveOptions{}); err == nil {
		t.Error("oversized message accepted")
	}
	b.connected = false
	if err := b.Save(context.Background(), rec, driver.SaveOptions{}); err == nil {
		t.Error("disconnected message accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Save(ctx, rec, driver.SaveOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation=%v", err)
	}
}

func TestCloseCancelsHandshakeAndDial(t *testing.T) {
	for _, handshake := range []bool{false, true} {
		t.Run(map[bool]string{false: "dial", true: "handshake"}[handshake], func(t *testing.T) {
			entered := make(chan struct{})
			b, err := New(Options{ToolName: TracingTool, DialContext: func(ctx context.Context, _ uint32) (net.Conn, error) {
				close(entered)
				if !handshake {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				client, server := net.Pipe()
				t.Cleanup(func() { _ = server.Close() })
				return client, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if err := b.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCloseDeadlineInterruptsBlockedSend(t *testing.T) {
	release := make(chan struct{})
	b, err := New(Options{ToolName: TracingTool, DialContext: func(_ context.Context, _ uint32) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			message, err := capnp.NewDecoder(server).Decode()
			if err == nil {
				message.Release()
			}
			<-release
		}()
		return client, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer close(release)
	waitConnected(t, b)
	if err := b.Save(context.Background(), driver.Record{Data: []byte(`{"value":1}`)}, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("close=%v", err)
	}
}

// TestReconnectAndConcurrentSave 验证首次连接失败后的重连、并发保存全部成功，
// 并在关闭排空后逐条核对接收内容，拒绝丢失、重复和额外的文档。
func TestReconnectAndConcurrentSave(t *testing.T) {
	const documentCount = 16
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var receivedMutex sync.Mutex
	received := make(map[string]int)
	server, err := transport.Serve(listener, func(_ *transport.Session, chunk transport.ChunkMsg) {
		if len(chunk.Data) == 0 {
			return
		}
		receivedMutex.Lock()
		received[string(chunk.Data)]++
		receivedMutex.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	var mutex sync.Mutex
	var attempts int
	backend, err := New(Options{
		ToolName: ProfilingTool, RetryInterval: time.Millisecond,
		DialContext: func(ctx context.Context, _ uint32) (net.Conn, error) {
			mutex.Lock()
			attempts++
			attempt := attempts
			mutex.Unlock()
			if attempt == 1 {
				return nil, errors.New("host restarting")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = backend.Close(ctx)
	})
	waitConnected(t, backend)
	expected := make(map[string]int, documentCount)
	results := make(chan error, documentCount)
	for index := range documentCount {
		document := fmt.Sprintf(`{"document":"checkout-%d"}`, index)
		expected[document] = 1
		go func() {
			results <- backend.Save(t.Context(), driver.Record{Data: []byte(document)}, driver.SaveOptions{})
		}()
	}
	for range documentCount {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent save: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent save did not finish")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := backend.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Closing the sender drains writes, but the receiver may still be dispatching them.
	for {
		receivedMutex.Lock()
		isComplete := len(received) == len(expected)
		receivedMutex.Unlock()
		if isComplete {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("receiver did not receive every document")
		case <-time.After(time.Millisecond):
		}
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	receivedMutex.Lock()
	defer receivedMutex.Unlock()
	if len(received) != len(expected) {
		t.Fatalf("received %d distinct documents, want %d", len(received), len(expected))
	}
	for document, count := range expected {
		if received[document] != count {
			t.Fatalf("document %q received %d times, want %d", document, received[document], count)
		}
	}
}

// TestSaveAdmissionBeforeEncoding 验证断线、关闭、队列已满和其他编码占满容量时，
// 大载荷被拒绝且不分配载荷大小的缓冲区，不改变已有的容量计数。
func TestSaveAdmissionBeforeEncoding(t *testing.T) {
	const payloadSize = 8 << 20
	frameSize, err := transport.ChunkFrameSize(payloadSize)
	if err != nil {
		t.Fatal(err)
	}
	record := driver.Record{Data: bytes.Repeat([]byte("d"), payloadSize)}
	for _, tc := range []struct {
		name              string
		connected, closed bool
		pending, encoding int
	}{
		{name: "disconnected"},
		{name: "closed", connected: true, closed: true},
		{name: "queue-full", connected: true, pending: frameSize},
		{name: "encoding-full", connected: true, encoding: frameSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &Backend{
				options:   Options{MaxMessageSize: frameSize, MaxPendingBytes: frameSize},
				connected: tc.connected, closed: tc.closed, pendingBytes: tc.pending, encodingBytes: tc.encoding,
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			err := backend.Save(t.Context(), record, driver.SaveOptions{})
			runtime.ReadMemStats(&after)
			if err == nil {
				t.Fatal("unavailable capacity accepted")
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= payloadSize {
				t.Fatalf("rejected payload allocated %d bytes", allocated)
			}
			if backend.pendingBytes != tc.pending || backend.encodingBytes != tc.encoding || len(backend.pending) != 0 {
				t.Fatalf("rejection changed queue accounting: pending=%d encoding=%d", backend.pendingBytes, backend.encodingBytes)
			}
		})
	}
}

// TestSaveExactCapacity 验证刚好填满容量的帧成功排队、预留计数被释放，
// 后续写入被拒绝，并且编码结果不引用调用方的可变缓冲区。
func TestSaveExactCapacity(t *testing.T) {
	record := driver.Record{Data: []byte("checkout-document")}
	frame, err := transport.EncodeChunkFrame(record.Data)
	if err != nil {
		t.Fatal(err)
	}
	backend := &Backend{
		options:   Options{MaxMessageSize: len(frame), MaxPendingBytes: len(frame)},
		connected: true, notify: make(chan struct{}, 1),
	}
	if err := backend.Save(t.Context(), record, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	record.Data[0] = '!'
	if backend.pendingBytes != len(frame) || backend.encodingBytes != 0 || len(backend.pending) != 1 ||
		!bytes.Equal(backend.pending[0].frame, frame) {
		t.Fatal("accepted frame or byte accounting is incorrect")
	}
	if err := backend.Save(t.Context(), record, driver.SaveOptions{}); err == nil {
		t.Fatal("full queue accepted document")
	}
}

func BenchmarkSaveRejected(b *testing.B) {
	record := driver.Record{Data: make([]byte, 8<<20)}
	backend := &Backend{options: Options{MaxMessageSize: 16 << 20, MaxPendingBytes: 64 << 20}}
	b.ReportAllocs()
	for b.Loop() {
		if err := backend.Save(b.Context(), record, driver.SaveOptions{}); err == nil {
			b.Fatal("disconnected save succeeded")
		}
	}
}

// TestConcurrentSaveCapacity 验证多个生产者竞争有限容量时，仅允许容量内的帧入队，
// 拒绝其余写入，且所有编码结束后没有遗留的预留计数。
func TestConcurrentSaveCapacity(t *testing.T) {
	const capacity = 2
	record := driver.Record{Data: bytes.Repeat([]byte("d"), 1<<20)}
	frameSize, err := transport.ChunkFrameSize(len(record.Data))
	if err != nil {
		t.Fatal(err)
	}
	backend := &Backend{
		options:   Options{MaxMessageSize: frameSize, MaxPendingBytes: capacity * frameSize},
		connected: true, notify: make(chan struct{}, 1),
	}
	results := make(chan error, 16)
	start := make(chan struct{})
	for range cap(results) {
		go func() {
			<-start
			results <- backend.Save(t.Context(), record, driver.SaveOptions{})
		}()
	}
	close(start)
	var accepted int
	for range cap(results) {
		if <-results == nil {
			accepted++
		}
	}
	if accepted != capacity || len(backend.pending) != capacity || backend.encodingBytes != 0 ||
		backend.pendingBytes != capacity*frameSize {
		t.Fatalf("accepted=%d queued=%d pending=%d encoding=%d", accepted, len(backend.pending),
			backend.pendingBytes, backend.encodingBytes)
	}
}

// TestBackendUnsupportedOperations 验证六个不支持的读取、删除及查询接口
// 返回统一哨兵错误；Query 不得调用消费回调。
func TestBackendUnsupportedOperations(t *testing.T) {
	backend := &Backend{}
	for _, tc := range []struct {
		name string
		call func(t *testing.T) error
	}{
		{"Get", func(t *testing.T) error {
			_, err := backend.Get(t.Context(), "checkout-trace")
			return err
		}},
		{"Delete", func(t *testing.T) error { return backend.Delete(t.Context(), "checkout-trace") }},
		{"DeleteByQuery", func(t *testing.T) error {
			_, err := backend.DeleteByQuery(t.Context(), driver.DeleteQuery{})
			return err
		}},
		{"Query", func(t *testing.T) error {
			return backend.Query(t.Context(), driver.Query{}, func([]driver.Record) error {
				t.Fatal("unsupported query invoked consumer")
				return nil
			})
		}},
		{"Count", func(t *testing.T) error {
			_, err := backend.Count(t.Context(), driver.Query{})
			return err
		}},
		{"Values", func(t *testing.T) error {
			_, err := backend.Values(t.Context(), "tracer_name", driver.Query{}, 10)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(t); !errors.Is(err, driver.ErrUnsupportedOp) {
				t.Fatalf("error=%v, want ErrUnsupportedOp", err)
			}
		})
	}
}
