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

	capnp "capnproto.org/go/capnp/v3"

	"huatuo-bamai/internal/log"
)

// Server accepts connections and dispatches ChunkMsg events to a caller-supplied handler.
type Server struct {
	mutex          sync.Mutex
	connectionWG   sync.WaitGroup
	connections    map[net.Conn]struct{}
	listener       net.Listener
	handler        func(*Session, ChunkMsg)
	cancel         context.CancelFunc
	acceptDone     chan struct{}
	drainDone      chan struct{}
	handlersDone   chan struct{}
	listenerOnce   sync.Once
	listenerErr    error
	accepting      bool
	forceClosing   bool
	activeHandlers int
}

var (
	// ErrDrainTimeout reports that active streams did not drain before the
	// caller's context expired; their sockets have been force-closed.
	ErrDrainTimeout = errors.New("transport: drain timed out")
	// ErrHandlersActive reports that Close released network resources while a
	// caller-owned handler was still running.
	ErrHandlersActive = errors.New("transport: close incomplete: handlers still active")
)

// Serve starts accepting connections from l in the background.
func Serve(l net.Listener, handler func(*Session, ChunkMsg)) (*Server, error) {
	if l == nil {
		return nil, fmt.Errorf("transport: listener must not be nil")
	}

	srv := &Server{
		listener:     l,
		connections:  make(map[net.Conn]struct{}),
		handler:      handler,
		accepting:    true,
		acceptDone:   make(chan struct{}),
		drainDone:    make(chan struct{}),
		handlersDone: make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	srv.cancel = cancel
	go func() {
		defer close(srv.acceptDone)
		srv.acceptLoop(ctx)
	}()
	go func() {
		// Waiting for acceptDone first guarantees that no Add can race with Wait.
		<-srv.acceptDone
		srv.connectionWG.Wait()
		close(srv.drainDone)
	}()

	return srv, nil
}

// QuiesceAndDrain stops accepting connections and waits for active streams.
func (s *Server) QuiesceAndDrain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("transport: drain context must not be nil")
	}

	listenerErr := s.stopAccepting()

	select {
	case <-s.drainDone:
		return listenerErr
	case <-ctx.Done():
		closeErr, _ := s.forceCloseConnections()
		return errors.Join(
			listenerErr,
			closeErr,
			ErrDrainTimeout,
			fmt.Errorf("transport: drain active streams: %w", ctx.Err()),
		)
	}
}

// Close releases network resources. It returns ErrHandlersActive rather than
// waiting indefinitely for a handler that does not honor shutdown.
func (s *Server) Close() error {
	if s.cancel != nil {
		s.cancel()
	}

	listenerErr := s.stopAccepting()
	closeErr, activeHandlers := s.forceCloseConnections()
	if activeHandlers > 0 {
		select {
		case <-s.handlersDone:
			return errors.Join(listenerErr, closeErr)
		default:
			return errors.Join(listenerErr, closeErr, ErrHandlersActive)
		}
	}

	// forceClosing prevents decoded frames from starting new handlers. Connection
	// goroutines may still be unwinding, but none can reach caller-owned storage.
	<-s.handlersDone
	return errors.Join(listenerErr, closeErr)
}

func (s *Server) stopAccepting() error {
	s.mutex.Lock()
	s.accepting = false
	s.mutex.Unlock()

	s.listenerOnce.Do(func() {
		s.listenerErr = s.listener.Close()
		if errors.Is(s.listenerErr, net.ErrClosed) {
			s.listenerErr = nil
		}
	})
	return s.listenerErr
}

func (s *Server) forceCloseConnections() (error, int) {
	s.mutex.Lock()
	if !s.forceClosing {
		s.forceClosing = true
		if s.activeHandlers == 0 {
			close(s.handlersDone)
		}
	}
	activeHandlers := s.activeHandlers
	conns := make([]net.Conn, 0, len(s.connections))
	for conn := range s.connections {
		conns = append(conns, conn)
		delete(s.connections, conn)
	}
	s.mutex.Unlock()

	var errs []error
	for _, conn := range conns {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("transport: close connection: %w", err))
		}
	}

	return errors.Join(errs...), activeHandlers
}

func (s *Server) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.mutex.Lock()
			accepting := s.accepting
			s.mutex.Unlock()
			if ctx.Err() != nil || !accepting || errors.Is(err, net.ErrClosed) {
				return
			}

			log.Warnf("accept: %v", err)
			continue
		}

		s.mutex.Lock()
		if !s.accepting {
			s.mutex.Unlock()
			_ = conn.Close()
			continue
		}
		s.connections[conn] = struct{}{}
		s.mutex.Unlock()

		s.connectionWG.Add(1)

		go func() {
			defer func() {
				s.mutex.Lock()
				_, ok := s.connections[conn]
				delete(s.connections, conn)
				s.mutex.Unlock()

				if ok {
					_ = conn.Close()
				}

				s.connectionWG.Done()
			}()

			s.handleConn(ctx, conn)
		}()
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	frameDecoder := capnp.NewDecoder(conn)

	firstMsg, err := frameDecoder.Decode()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			log.Warnf("connect: %v", err)
		}

		return
	}

	sess, err := parseSession(firstMsg)
	if err != nil {
		log.Warnf("connect: %v", err)
		return
	}

	// Empty ToolName violates the protocol: with no name the connection cannot
	// be routed to any handler. Log at Error level so the producer side notices.
	if sess.ToolName == "" {
		log.Errorf("connect: empty tool name, closing connection")
		return
	}

	log.Infof("connected tool=%s version=%s task_id=%s",
		sess.ToolName, sess.Version, sess.TaskID)
	defer log.Infof("disconnected tool=%s task_id=%s",
		sess.ToolName, sess.TaskID)

	for {
		if ctx.Err() != nil {
			return
		}

		msg, err := frameDecoder.Decode()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Warnf("%s: recv: %v", sess.ToolName, err)
			}

			return
		}

		chunk, err := parseChunk(msg)
		if err != nil {
			log.Warnf("%s: recv: %v", sess.ToolName, err)
			return
		}

		if !s.callHandler(sess, chunk) {
			return
		}

		if chunk.End {
			return
		}
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

	toolName, _ := connect.ToolName()
	version, _ := connect.Version()
	taskID, _ := connect.TaskID()

	return &Session{
		ToolName: toolName,
		Version:  version,
		TaskID:   taskID,
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
		Data:  data,
		Flush: chunk.Flush(),
		End:   chunk.End(),
		Err:   errStr,
	}, nil
}

func (s *Server) callHandler(sess *Session, chunk ChunkMsg) bool {
	s.mutex.Lock()
	if s.forceClosing {
		s.mutex.Unlock()
		return false
	}
	s.activeHandlers++
	s.mutex.Unlock()

	defer func() {
		s.mutex.Lock()
		s.activeHandlers--
		if s.forceClosing && s.activeHandlers == 0 {
			close(s.handlersDone)
		}
		s.mutex.Unlock()
	}()

	s.handler(sess, chunk)
	return true
}
