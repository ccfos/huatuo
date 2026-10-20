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
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

const (
	DefaultPort   = 19705
	TracingTool   = "kata-tracing-document"
	ProfilingTool = "kata-profiling-document"
)

var ErrClosed = errors.New("vsock storage: closed")

// Options bounds the best-effort Guest queue and network operations.
type Options struct {
	Port            uint32
	ToolName        string
	Version         string
	MaxMessageSize  int
	MaxPendingBytes int
	IOTimeout       time.Duration
	RetryInterval   time.Duration
	DialContext     func(context.Context, uint32) (net.Conn, error)
}

type queuedDocument struct {
	frame []byte
}

// Backend is a write-only, byte-bounded, reconnecting document sender.
// Documents accepted while connected may still be lost: the protocol has no ACK.
type Backend struct {
	options       Options
	mutex         sync.Mutex
	pending       []queuedDocument
	pendingBytes  int
	encodingBytes int
	connection    uint64
	connected     bool
	closed        bool
	notify        chan struct{}
	cancel        context.CancelFunc
	done          chan struct{}
}

// New starts a sender. Separate instances carry tracing and profiling documents.
//
//nolint:gocritic // Options is copied once at startup and remains immutable.
func New(options Options) (*Backend, error) {
	if options.ToolName != TracingTool && options.ToolName != ProfilingTool {
		return nil, fmt.Errorf("vsock storage: unsupported document tool %q", options.ToolName)
	}
	if options.Port == 0 {
		options.Port = DefaultPort
	}
	if options.Version == "" {
		options.Version = "unknown"
	}
	if options.MaxMessageSize == 0 {
		options.MaxMessageSize = 16 << 20
	}
	if options.MaxPendingBytes == 0 {
		options.MaxPendingBytes = 64 << 20
	}
	if options.IOTimeout == 0 {
		options.IOTimeout = 10 * time.Second
	}
	if options.RetryInterval == 0 {
		options.RetryInterval = time.Second
	}
	if options.Port == ^uint32(
		0,
	) ||
		options.MaxMessageSize < 128 ||
		options.MaxPendingBytes < options.MaxMessageSize ||
		options.IOTimeout < 0 ||
		options.RetryInterval < 0 {
		return nil, errors.New("vsock storage: invalid port, byte limits or timeouts")
	}
	if options.DialContext == nil {
		options.DialContext = dialHost
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Backend{options: options, notify: make(chan struct{}, 1), cancel: cancel, done: make(chan struct{})}
	go b.run(ctx)
	return b, nil
}

func (*Backend) Init(context.Context, string, []driver.Index) error { return nil }

// Save copies data only after reserving queue space. Conditional writes and
// visibility guarantees cannot be implemented by an unacknowledged stream.
func (b *Backend) Save(ctx context.Context, rec driver.Record, options driver.SaveOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.Mode != driver.SaveModeUpsert || len(options.Conditions) > 0 || options.WaitForVisibility {
		return driver.ErrUnsupportedOp
	}
	// Reject oversized JSON before encoding allocates a temporary frame.
	if len(rec.Data) > b.options.MaxMessageSize {
		return fmt.Errorf("vsock storage: message too large")
	}
	frameSize, err := transport.ChunkFrameSize(len(rec.Data))
	if err != nil {
		return err
	}
	if frameSize > b.options.MaxMessageSize {
		return fmt.Errorf("vsock storage: frame too large")
	}
	b.mutex.Lock()
	if b.closed {
		b.mutex.Unlock()
		return ErrClosed
	}
	if !b.connected {
		b.mutex.Unlock()
		return fmt.Errorf("vsock storage: disconnected; document dropped")
	}
	if frameSize > b.options.MaxPendingBytes-b.pendingBytes-b.encodingBytes {
		b.mutex.Unlock()
		return fmt.Errorf("vsock storage: queue full; document dropped")
	}
	connection := b.connection
	b.encodingBytes += frameSize
	b.mutex.Unlock()

	frame, err := transport.EncodeChunkFrame(rec.Data)
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.encodingBytes -= frameSize
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.closed {
		return ErrClosed
	}
	if !b.connected || b.connection != connection {
		return fmt.Errorf("vsock storage: connection changed; document dropped")
	}
	if len(frame) != frameSize {
		return fmt.Errorf("vsock storage: encoded frame size %d differs from reserved %d", len(frame), frameSize)
	}
	b.pending = append(b.pending, queuedDocument{frame: frame})
	b.pendingBytes += len(frame)
	b.signal()
	return nil
}

func (b *Backend) signal() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

func (b *Backend) run(ctx context.Context) {
	defer close(b.done)
	defer b.cancel()
	for ctx.Err() == nil {
		b.mutex.Lock()
		closed := b.closed
		b.mutex.Unlock()
		if closed {
			return
		}
		dialCtx, cancel := context.WithTimeout(ctx, b.options.IOTimeout)
		conn, err := b.options.DialContext(dialCtx, b.options.Port)
		cancel()
		if err == nil {
			err = b.send(ctx, conn)
		}
		if ctx.Err() != nil || err == nil {
			return
		}
		log.Warnf("vsock storage: connection ended: %v", err)
		timer := time.NewTimer(b.options.RetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-b.notify:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (b *Backend) send(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(b.options.IOTimeout))
	client, err := transport.NewClientWithConnContext(
		ctx,
		conn,
		transport.ClientMetadata{ToolName: b.options.ToolName, Version: b.options.Version, TaskID: ""},
	)
	if err != nil {
		return err
	}
	defer client.Close()
	b.mutex.Lock()
	b.connection++
	b.connected = true
	b.mutex.Unlock()
	defer func() {
		b.mutex.Lock()
		dropped := len(b.pending)
		b.connected = false
		b.pending = nil
		b.pendingBytes = 0
		b.mutex.Unlock()
		if dropped > 0 {
			log.Warnf("vsock storage: connection lost; dropped %d documents", dropped)
		}
	}()
	for {
		b.mutex.Lock()
		if len(b.pending) == 0 {
			closed := b.closed
			b.mutex.Unlock()
			if closed {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-b.notify:
				continue
			}
		}
		item := b.pending[0]
		b.mutex.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(b.options.IOTimeout))
		if err := client.SendFrame(item.frame); err != nil {
			return err
		}
		b.mutex.Lock()
		b.pending[0] = queuedDocument{}
		b.pending = b.pending[1:]
		b.pendingBytes -= len(item.frame)
		b.mutex.Unlock()
	}
}

// Close rejects producers, drains the queue, then interrupts network work on
// deadline. It always joins the writer before returning; repeated Close is safe.
func (b *Backend) Close(ctx context.Context) error {
	b.mutex.Lock()
	b.closed = true
	empty := len(b.pending) == 0
	b.mutex.Unlock()
	if empty {
		b.cancel()
	}
	b.signal()
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		b.cancel()
		<-b.done
		return ctx.Err()
	}
}

func (*Backend) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrUnsupportedOp
}
func (*Backend) Delete(context.Context, string) error { return driver.ErrUnsupportedOp }
func (*Backend) DeleteByQuery(context.Context, driver.DeleteQuery) (int64, error) {
	return 0, driver.ErrUnsupportedOp
}

func (*Backend) Query(context.Context, driver.Query, func([]driver.Record) error) error {
	return driver.ErrUnsupportedOp
}

func (*Backend) Count(context.Context, driver.Query) (int64, error) {
	return 0, driver.ErrUnsupportedOp
}

func (*Backend) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, driver.ErrUnsupportedOp
}

var _ driver.Backend = (*Backend)(nil)
