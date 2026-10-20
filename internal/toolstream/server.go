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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

// Request is one frame and a snapshot of its connection metadata.
// Data is nil for control-only frames or decoding failures.
type Request[T any] struct {
	ServiceName string
	ToolName    string
	Version     string
	TaskID      string
	Data        *T
	End         bool
	Commit      bool
	// PriorErr contains failures from earlier frames only.
	PriorErr error
	// Err contains tool, protocol or decoding failures for this frame.
	Err error
}

type untypedHandler func(context.Context, *transport.Session, transport.ChunkMsg) error

// DefaultSockPath is the Unix socket path used by the default server.
const DefaultSockPath = "/var/run/huatuo-toolstream.sock"

var (
	// ErrNotInitialized is returned when Start or Close is called on a nil or zero-value Server.
	ErrNotInitialized = errors.New("toolstream: server not initialized")
	// ErrAlreadyStarted is returned by Start when the server is already running.
	ErrAlreadyStarted = errors.New("toolstream: server already started")
)

var (
	defaultServer *Server
	defaultOnce   sync.Once
)

// Server dispatches incoming tool events to per-tool typed handlers.
// It is safe for simultaneous use by multiple goroutines.
type Server struct {
	sockPath string

	// Handler registry:
	handlersMu sync.RWMutex
	handlers   map[string]untypedHandler

	// Lifecycle of the underlying transport:
	innerMu sync.Mutex
	inner   *transport.Server
}

// NewServer creates a Server that will listen on sockPath when Start is called.
func NewServer(sockPath string) (*Server, error) {
	if sockPath == "" {
		return nil, fmt.Errorf("socket path must not be empty")
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

// Register binds a typed handler for events from serviceName; safe for concurrent use.
func Register[T any](srv *Server, serviceName string, handler func(context.Context, *Request[T]) error) {
	srv.handlersMu.Lock()
	defer srv.handlersMu.Unlock()
	srv.handlers[serviceName] = func(ctx context.Context, session *transport.Session, chunk transport.ChunkMsg) error {
		request := &Request[T]{
			ServiceName: session.ServiceName, ToolName: session.ToolName, Version: session.Version,
			TaskID: session.TaskID, End: chunk.End, Commit: chunk.Commit, PriorErr: session.Err,
		}
		if chunk.Err != "" {
			request.Err = errors.New(chunk.Err)
		}
		if len(chunk.Data) != 0 {
			if err := json.Unmarshal(chunk.Data, &request.Data); err != nil {
				request.Data = nil
				request.Err = errors.Join(request.Err, fmt.Errorf("unmarshal for %s: %w", serviceName, err))
			} else if request.Data == nil {
				request.Err = errors.Join(request.Err, errors.New("event payload must not be null"))
			}
		}
		frameErr := request.Err
		return errors.Join(frameErr, handler(ctx, request))
	}
}

// RegisterDefault binds a typed handler on the default server.
func RegisterDefault[T any](serviceName string, handler func(context.Context, *Request[T]) error) {
	srv, err := NewServerDefault()
	if err != nil {
		log.Fatalf("default server: %v", err)
	}
	Register(srv, serviceName, handler)
}

// Start listens in the background; call Close to stop.
func (s *Server) Start() error {
	if s == nil || s.sockPath == "" {
		return ErrNotInitialized
	}

	s.innerMu.Lock()
	defer s.innerMu.Unlock()

	if s.inner != nil {
		return ErrAlreadyStarted
	}

	l, err := transport.ListenUDS(s.sockPath)
	if err != nil {
		return err
	}

	inner, err := transport.Serve(l, s.dispatch)
	if err != nil {
		_ = l.Close()
		return err
	}

	s.inner = inner
	return nil
}

// Close shuts down the server and waits for all goroutines to finish.
// Calling Close more than once is safe and a no-op after the first call.
func (s *Server) Close() error {
	if s == nil {
		return ErrNotInitialized
	}

	s.innerMu.Lock()
	defer s.innerMu.Unlock()

	if s.inner == nil {
		return nil
	}
	inner := s.inner
	s.inner = nil
	return inner.Close()
}

func (s *Server) dispatch(session *transport.Session, chunk transport.ChunkMsg) {
	s.handlersMu.RLock()
	handler := s.handlers[session.ServiceName]
	s.handlersMu.RUnlock()
	var err error
	if handler == nil {
		err = errors.New("no handler registered")
		if chunk.Err != "" {
			err = errors.Join(errors.New(chunk.Err), err)
		}
	} else {
		err = handler(session.Context, session, chunk)
	}
	if err != nil {
		session.Err = errors.Join(session.Err, err)
		log.Warnf("%s: handler: %v", session.ServiceName, err)
	}
}
