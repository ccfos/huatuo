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

// Package pprof provides a standalone HTTP server for Go runtime profiles.
package pprof

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	"sync"
	"time"
)

const readHeaderTimeout = 5 * time.Second

// Server serves Go runtime profiles and owns its listener.
type Server struct {
	httpServer         *http.Server
	serveErr           <-chan error
	stopContextWatcher chan struct{}
	closeOnce          sync.Once
	closeErr           error
}

// Handler returns an isolated pprof HTTP handler.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", httppprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)
	return mux
}

// StartDefault serves pprof on 127.0.0.1:6000 until ctx is canceled or
// Server.Close is called. The caller must close the returned server.
func StartDefault(ctx context.Context) (*Server, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:6000")
	if err != nil {
		return nil, fmt.Errorf("listen for pprof server on 127.0.0.1:6000: %w", err)
	}

	return start(ctx, listener), nil
}

// start takes ownership of listener until the returned server is closed.
func start(ctx context.Context, listener net.Listener) *Server {
	httpServer := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	serveErr := make(chan error, 1)
	server := &Server{
		httpServer:         httpServer,
		serveErr:           serveErr,
		stopContextWatcher: make(chan struct{}),
	}

	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-server.stopContextWatcher:
		}
	}()

	return server
}

// Close stops the server and waits for its serving goroutine to exit.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopContextWatcher)
		s.closeErr = errors.Join(s.httpServer.Close(), <-s.serveErr)
	})
	return s.closeErr
}
