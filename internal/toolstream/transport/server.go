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

package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	capnp "capnproto.org/go/capnp/v3"

	"github.com/ccfos/huatuo/internal/log"
)

// Server accepts connections and dispatches ChunkMsg events to a caller-supplied handler.
type Server struct {
	tasks            map[[2]string]*Session
	connContext      func(context.Context, net.Conn) (context.Context, error)
	mutex            sync.Mutex
	waitGroup        sync.WaitGroup
	connections      map[net.Conn]struct{}
	listener         net.Listener
	handler          func(*Session, ChunkMsg)
	cancel           context.CancelFunc
	maxMessageSize   uint64
	handshakeTimeout time.Duration
	frameReadTimeout time.Duration
	decodeBudget     *DecodeBudget
	onReceiveError   func(error)
	shutdownOnce     sync.Once
	shutdownDone     chan struct{}
	shutdownErr      error
}

// Serve starts accepting connections from l in the background.
func Serve(l net.Listener, handler func(*Session, ChunkMsg)) (*Server, error) {
	return ServeWithOptions(l, handler, ServerOptions{})
}

// ServerOptions configures protocol decoding.
type ServerOptions struct {
	// ConnContext binds trusted connection metadata before reading the handshake.
	// An error rejects the connection. The returned context must derive from ctx.
	ConnContext      func(ctx context.Context, conn net.Conn) (context.Context, error)
	MaxMessageSize   uint64
	HandshakeTimeout time.Duration
	// FrameReadTimeout starts at the first byte, not while the connection is idle.
	FrameReadTimeout time.Duration
	DecodeBudget     *DecodeBudget
	OnReceiveError   func(error)
}

// ServeWithOptions starts accepting connections from l in the background.
func ServeWithOptions(
	l net.Listener,
	handler func(*Session, ChunkMsg),
	options ServerOptions,
) (*Server, error) {
	if l == nil {
		return nil, fmt.Errorf("transport: listener must not be nil")
	}

	if handler == nil {
		return nil, errors.New("transport: handler must not be nil")
	}
	if options.HandshakeTimeout < 0 || options.FrameReadTimeout < 0 {
		return nil, errors.New("transport: timeouts must not be negative")
	}
	if (options.DecodeBudget != nil || options.FrameReadTimeout > 0) && options.MaxMessageSize < capnpWordSize {
		return nil, errors.New("transport: limited decoding requires a max message size of at least 8 bytes")
	}
	srv := &Server{
		connContext:      options.ConnContext,
		listener:         l,
		connections:      make(map[net.Conn]struct{}),
		handler:          handler,
		maxMessageSize:   options.MaxMessageSize,
		handshakeTimeout: options.HandshakeTimeout,
		frameReadTimeout: options.FrameReadTimeout,
		decodeBudget:     options.DecodeBudget,
		onReceiveError:   options.OnReceiveError,
		shutdownDone:     make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	srv.cancel = cancel
	srv.waitGroup.Add(1)

	go func() {
		defer srv.waitGroup.Done()
		srv.acceptLoop(ctx)
	}()

	return srv, nil
}

func (s *Server) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			log.Warnf("toolstream: accept: %v", err)
			continue
		}

		s.mutex.Lock()
		// Accept may return just as shutdown snapshots the active connections.
		if ctx.Err() != nil {
			s.mutex.Unlock()
			_ = conn.Close()
			return
		}
		s.connections[conn] = struct{}{}
		s.mutex.Unlock()

		s.waitGroup.Add(1)

		go func() {
			defer func() {
				s.mutex.Lock()
				_, ok := s.connections[conn]
				delete(s.connections, conn)
				s.mutex.Unlock()

				if ok {
					_ = conn.Close()
				}

				s.waitGroup.Done()
			}()

			s.handleConn(ctx, conn)
		}()
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	if s.connContext != nil {
		var err error
		ctx, err = s.connContext(ctx, conn)
		if err != nil {
			s.reportReceiveError(err)
			return
		}
	}
	if ctx == nil {
		s.reportReceiveError(errors.New("transport: connection context must not be nil"))
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Cancellation must also interrupt an idle read, not just budget waits.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	frameDecoder := newMessageDecoder(conn, s.maxMessageSize, s.decodeBudget)
	if s.frameReadTimeout > 0 {
		frameDecoder.frameReadTimeout = s.frameReadTimeout
	}

	handshakeContext := ctx
	cancelHandshake := func() {}
	if s.handshakeTimeout > 0 {
		var cancel context.CancelFunc
		handshakeContext, cancel = context.WithTimeout(ctx, s.handshakeTimeout)
		cancelHandshake = cancel
		if err := conn.SetReadDeadline(time.Now().Add(s.handshakeTimeout)); err != nil {
			cancelHandshake()
			s.reportReceiveError(err)
			return
		}
	}
	firstMsg, err := frameDecoder.Decode(handshakeContext)
	cancelHandshake()
	if err != nil {
		s.reportReceiveError(err)
		if !errors.Is(err, io.EOF) {
			log.Warnf("toolstream: connect: %v", err)
		}

		return
	}
	if s.handshakeTimeout > 0 {
		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			firstMsg.Release()
			s.reportReceiveError(err)
			return
		}
	}

	sess, err := parseSession(firstMsg.message)
	firstMsg.Release()
	if err != nil {
		s.reportReceiveError(err)
		log.Warnf("toolstream: connect: %v", err)
		return
	}

	// Empty ServiceName violates the protocol: with no name the connection cannot
	// be routed to any handler. Log at Error level so the producer side notices.
	if sess.ServiceName == "" {
		s.reportReceiveError(errors.New("transport: empty service name"))
		log.Errorf("connect: empty service name, closing connection")
		return
	}

	sess.Context = ctx

	// Ownership lasts through handler completion and ACK, including a peer disconnect.
	var ownershipErr error
	if sess.TaskID != "" {
		key := [2]string{sess.ServiceName, sess.TaskID}
		s.mutex.Lock()
		if s.tasks == nil {
			s.tasks = make(map[[2]string]*Session)
		}
		if s.tasks[key] != nil {
			ownershipErr = fmt.Errorf("toolstream: session %s/%s already belongs to another connection", sess.ServiceName, sess.TaskID)
			sess.Err = ownershipErr
		} else {
			s.tasks[key] = sess
		}
		s.mutex.Unlock()
		if ownershipErr == nil {
			defer func() {
				s.mutex.Lock()
				delete(s.tasks, key)
				s.mutex.Unlock()
			}()
		}
	}

	log.Infof("connected service=%s version=%s task_id=%s",
		sess.ServiceName, sess.Version, sess.TaskID)
	defer log.Infof("disconnected service=%s task_id=%s",
		sess.ServiceName, sess.TaskID)

	for {
		if ctx.Err() != nil {
			return
		}

		msg, err := frameDecoder.Decode(ctx)
		if err != nil {
			s.reportReceiveError(err)
			if !errors.Is(err, io.EOF) {
				log.Warnf("%s: recv: %v", sess.ServiceName, err)
			}

			return
		}

		chunk, err := parseChunk(msg.message)
		if err != nil {
			msg.Release()
			s.reportReceiveError(err)
			log.Warnf("%s: recv: %v", sess.ServiceName, err)
			return
		}

		if chunk.Commit && (!chunk.End || chunk.Err != "") {
			chunk.Err = "commit requires end and no tool error"
			chunk.End = true
		}
		if ownershipErr == nil && ctx.Err() == nil {
			s.handler(sess, chunk)
		}
		end := chunk.End
		commit := chunk.Commit
		msg.Release()
		if commit {
			if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return
			}
			if err := sendCommitResult(conn, sess.Err); err != nil {
				log.Warnf("%s: commit acknowledgement: %v", sess.ServiceName, err)
			}
		}

		if end {
			return
		}
	}
}

func (s *Server) reportReceiveError(err error) {
	if s.onReceiveError != nil &&
		!errors.Is(err, io.EOF) &&
		!errors.Is(err, net.ErrClosed) {
		s.onReceiveError(err)
	}
}

// parseSession parses the Connect frame and returns the session metadata.
func parseSession(msg *capnp.Message) (*Session, error) {
	root, err := ReadRootMessage(msg)
	if err != nil {
		return nil, fmt.Errorf("transport: decode: %w", err)
	}

	if root.Which() != Message_Which_connect {
		return nil, fmt.Errorf("transport: unexpected frame type %s", root.Which())
	}

	connect, err := root.Connect()
	if err != nil {
		return nil, fmt.Errorf("transport: decode connect: %w", err)
	}

	if connect.ProtoVersion() != ProtocolVersion {
		return nil, fmt.Errorf("unsupported toolstream protocol %d; require %d", connect.ProtoVersion(), ProtocolVersion)
	}
	serviceName, err := connect.ServiceName()
	if err != nil {
		return nil, fmt.Errorf("transport: connect service name: %w", err)
	}
	toolName, err := connect.ToolName()
	if err != nil {
		return nil, fmt.Errorf("transport: connect tool name: %w", err)
	}
	version, err := connect.Version()
	if err != nil {
		return nil, fmt.Errorf("transport: connect version: %w", err)
	}
	taskID, err := connect.TaskID()
	if err != nil {
		return nil, fmt.Errorf("transport: connect task id: %w", err)
	}

	return &Session{
		ServiceName:  serviceName,
		ToolName:     toolName,
		Version:      version,
		TaskID:       taskID,
		ProtoVersion: connect.ProtoVersion(),
	}, nil
}

// parseChunk parses a Chunk message from a decoded frame.
func parseChunk(msg *capnp.Message) (ChunkMsg, error) {
	root, err := ReadRootMessage(msg)
	if err != nil {
		return ChunkMsg{}, fmt.Errorf("transport: chunk root: %w", err)
	}

	if root.Which() != Message_Which_chunk {
		return ChunkMsg{}, fmt.Errorf("transport: expected chunk, got %s", root.Which())
	}

	chunk, err := root.Chunk()
	if err != nil {
		return ChunkMsg{}, fmt.Errorf("transport: decode chunk: %w", err)
	}

	data, err := chunk.Data()
	if err != nil {
		return ChunkMsg{}, fmt.Errorf("transport: chunk data: %w", err)
	}

	errStr, err := chunk.Error()
	if err != nil {
		return ChunkMsg{}, fmt.Errorf("transport: chunk error: %w", err)
	}

	return ChunkMsg{
		Data:   data,
		Commit: chunk.Commit(),
		End:    chunk.End(),
		Err:    errStr,
	}, nil
}

// Close shuts down the server and waits for all goroutines to finish.
func (s *Server) Close() error {
	return s.Shutdown(context.Background())
}

// Shutdown cancels handlers and closes connections, waiting at most until ctx
// expires. A timed-out handler may still be running; callers must not close its
// dependencies until a subsequent Shutdown or Close confirms completion.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() {
		s.cancel()
		go func() {
			s.shutdownErr = s.closeConnections()
			close(s.shutdownDone)
		}()
	})

	select {
	case <-s.shutdownDone:
		return s.shutdownErr
	default:
	}
	select {
	case <-s.shutdownDone:
		return s.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) closeConnections() error {
	var errs []error

	if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		errs = append(errs, err)
	}

	// snapshot under lock, close outside to avoid holding the lock during I/O
	s.mutex.Lock()
	conns := make([]net.Conn, 0, len(s.connections))

	for c := range s.connections {
		conns = append(conns, c)
		delete(s.connections, c)
	}

	s.mutex.Unlock()

	// closing each conn unblocks handleConn goroutines stuck in Decode
	for _, c := range conns {
		if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}

	s.waitGroup.Wait()

	return errors.Join(errs...)
}

func sendCommitResult(conn net.Conn, failure error) error {
	msg, seg, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		return err
	}
	defer msg.Release()
	root, err := NewRootMessage(seg)
	if err != nil {
		return err
	}
	result, err := root.NewResult()
	if err != nil {
		return err
	}
	if failure != nil {
		if err := result.SetError(failure.Error()); err != nil {
			return err
		}
	}
	return capnp.NewEncoder(conn).Encode(msg)
}
