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

// Package toolstream provides typed event dispatch over a Unix-domain-socket transport.
package toolstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/toolstream/transport"
)

// Session carries per-connection metadata from the Connect handshake.
type Session struct {
	*transport.Session
}

// untypedHandler is the codec-erased internal dispatch signature.
type untypedHandler func(sess *Session, payload []byte) error

// DefaultSockPath is the Unix socket path used by the default server.
const DefaultSockPath = "/var/run/huatuo-toolstream-default.sock"

// ErrNotInitialized is returned when Start or Close is called on a nil or zero-value Server.
var ErrNotInitialized = errors.New("toolstream: server not initialized")

var (
	defaultServer *Server
	defaultOnce   sync.Once
)

// Server dispatches incoming tool events to per-tool typed handlers.
type Server struct {
	sockPath string
	mu       sync.RWMutex
	handlers map[string]untypedHandler
	inner    *transport.Server
}

// NewServer creates a Server that will listen on sockPath when Start is called.
func NewServer(sockPath string) (*Server, error) {
	if sockPath == "" {
		return nil, fmt.Errorf("toolstream: socket path must not be empty")
	}

	return &Server{
		sockPath: sockPath,
		handlers: make(map[string]untypedHandler),
	}, nil
}

// NewServerDefault returns the package-level singleton Server listening on DefaultSockPath.
func NewServerDefault() (*Server, error) {
	var initErr error
	defaultOnce.Do(func() {
		s, err := NewServer(DefaultSockPath)
		if err != nil {
			initErr = err
			return
		}
		defaultServer = s
	})
	if initErr != nil {
		return nil, initErr
	}
	return defaultServer, nil
}

// Register binds a typed handler for events from toolName; safe for concurrent use.
func Register[T any](
	srv *Server,
	toolName string,
	handler func(sess *Session, event T) error,
) {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	srv.handlers[toolName] = func(sess *Session, payload []byte) error {
		var ev T
		if err := json.Unmarshal(payload, &ev); err != nil {
			return fmt.Errorf("toolstream: unmarshal for %s: %w", toolName, err)
		}

		return handler(sess, ev)
	}
}

// RegisterDefault binds a typed handler for events from toolName on the default server.
func RegisterDefault[T any](toolName string, handler func(sess *Session, event T) error) {
	srv, err := NewServerDefault()
	if err != nil {
		panic(fmt.Sprintf("toolstream: default server: %v", err))
	}
	Register(srv, toolName, handler)
}

// Start listens in the background; call Close to stop.
func (s *Server) Start() error {
	if s == nil || s.sockPath == "" {
		return ErrNotInitialized
	}

	l, err := transport.ListenUDS(s.sockPath)
	if err != nil {
		return fmt.Errorf("toolstream: %w", err)
	}

	inner, err := transport.Serve(l, s.dispatch)
	if err != nil {
		return fmt.Errorf("toolstream: %w", err)
	}

	s.inner = inner
	return nil
}

// Close shuts down the server and waits for all goroutines to finish.
func (s *Server) Close() error {
	if s == nil {
		return ErrNotInitialized
	}

	if s.inner == nil {
		return nil
	}

	return s.inner.Close()
}

func (s *Server) dispatch(tsess *transport.Session, chunk transport.ChunkMsg) {
	if chunk.Err != "" {
		log.Warnf("toolstream: %s: tool error: %s", tsess.ToolName, chunk.Err)
		return
	}

	if chunk.End || len(chunk.Data) == 0 {
		return
	}

	s.mu.RLock()
	handler := s.handlers[tsess.ToolName]
	s.mu.RUnlock()

	if handler == nil {
		log.Warnf("toolstream: %s: no handler", tsess.ToolName)
		return
	}

	sess := &Session{Session: tsess}
	if err := handler(sess, chunk.Data); err != nil {
		log.Warnf("toolstream: %s: handler: %v", tsess.ToolName, err)
	}
}
