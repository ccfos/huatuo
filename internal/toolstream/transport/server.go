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
	mutex       sync.Mutex
	waitGroup   sync.WaitGroup
	connections map[net.Conn]struct{}
	listener    net.Listener
	handler     func(*Session, ChunkMsg)
	cancel      context.CancelFunc
}

// Serve starts accepting connections from l in the background.
func Serve(l net.Listener, handler func(*Session, ChunkMsg)) (*Server, error) {
	if l == nil {
		return nil, fmt.Errorf("transport: listener must not be nil")
	}

	srv := &Server{
		listener:    l,
		connections: make(map[net.Conn]struct{}),
		handler:     handler,
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

			log.Warnf("accept: %v", err)
			continue
		}

		s.mutex.Lock()
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

		s.handler(sess, chunk)

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

// Close shuts down the server and waits for all goroutines to finish.
func (s *Server) Close() error {
	if s.cancel != nil {
		s.cancel()
	}

	var errs []error

	if err := s.listener.Close(); err != nil {
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
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	s.waitGroup.Wait()

	return errors.Join(errs...)
}
