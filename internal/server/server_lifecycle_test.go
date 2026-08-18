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

package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServerStartReportsBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	srv := NewServer(nil)
	if err := srv.Start(listener.Addr().String()); err == nil {
		t.Fatal("Start() error = nil, want bind failure")
	}
}

func TestServerShutdownReleasesListener(t *testing.T) {
	srv := NewServer(nil)
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	addr := testServerAddr(t, srv)

	if err := srv.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listener was not released: %v", err)
	}
	_ = listener.Close()
}

func TestServerServesPProfOnAPIListener(t *testing.T) {
	srv := NewServer(&Config{EnablePProf: true})
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(t.Context()) })

	response, err := http.Get("http://" + testServerAddr(t, srv) + "/debug/pprof/")
	if err != nil {
		t.Fatalf("get pprof index: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read pprof index: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(body), "Types of profiles available") {
		t.Fatal("pprof index does not list profiles")
	}
}

func TestServerRejectsOperationsWhileStopping(t *testing.T) {
	srv := NewServer(nil)
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseHandler) })
	t.Cleanup(release)
	srv.MustRegisterRoutes("", []Route{{
		Method: http.MethodGet,
		Path:   "/block",
		Handler: func(ctx *Context) error {
			close(handlerStarted)
			select {
			case <-releaseHandler:
			case <-ctx.Request().Context().Done():
			}
			ctx.Status(http.StatusNoContent)
			return nil
		},
	}})

	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"http://"+testServerAddr(t, srv)+"/block",
		http.NoBody,
	)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			requestDone <- requestErr
			return
		}
		requestDone <- response.Body.Close()
	}()

	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("request handler did not start")
	}

	serveDone := srv.Done()
	shutdownCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- srv.Shutdown(shutdownCtx)
	}()

	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("server did not enter stopping state")
	}

	if err := srv.Shutdown(t.Context()); !errors.Is(err, ErrServerStopping) {
		t.Fatalf("concurrent Shutdown() error = %v, want %v", err, ErrServerStopping)
	}
	if err := srv.Start("127.0.0.1:0"); !errors.Is(err, ErrServerStopping) {
		t.Fatalf("Start() while stopping error = %v, want %v", err, ErrServerStopping)
	}

	release()
	if err := <-shutdownDone; err != nil {
		t.Fatalf("first Shutdown() error = %v", err)
	}
	if err := <-requestDone; err != nil {
		t.Fatalf("request error = %v", err)
	}

	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("restart after Shutdown() error = %v", err)
	}
	if err := srv.Shutdown(t.Context()); err != nil {
		t.Fatalf("second lifecycle Shutdown() error = %v", err)
	}
}

func testServerAddr(t *testing.T, srv *Server) string {
	t.Helper()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.activeExecution == nil {
		t.Fatal("server is not running")
	}
	return srv.activeExecution.listener.Addr().String()
}
