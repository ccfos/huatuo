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
	"net"
	"net/http"
	"testing"
	"time"

	httpGin "github.com/gin-gonic/gin"
)

func TestServerCloseAfterListenerExitClosesActiveRequests(t *testing.T) {
	srv := NewServer(nil)
	started := make(chan struct{})
	canceled := make(chan struct{})
	srv.engine.GET("/blocked", func(ctx *httpGin.Context) {
		close(started)
		<-ctx.Request.Context().Done()
		close(canceled)
	})
	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	execution := srv.activeExecution
	srv.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	clientDone := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		_ = execution.httpServer.Close()
		<-clientDone
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+execution.listener.Addr().String()+"/blocked", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(clientDone)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("request did not reach the handler")
	}
	if err := execution.listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := srv.Wait(ctx); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Wait() = %v, want listener error", err)
	}
	if err := srv.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Close() = %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Close left the request from the failed listener running")
	}
}
