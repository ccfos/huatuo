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

package handlers

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/server"

	httpGin "github.com/gin-gonic/gin"
)

// A server-wide WriteTimeout must not terminate event streams: the watch
// handler clears the per-request write deadline and relies on keepalive
// failures for liveness instead.
func TestWatchSurvivesWriteTimeout(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	engine := httpGin.New()
	server.NewRoot(engine, "").POST("/v1/events/watch", NewEventsHandler(2, 1).watch)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	// 200ms WriteTimeout kills any stream that still carries one long
	// before the first 1s keepalive ping.
	httpServer := &http.Server{
		Handler:           engine.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      200 * time.Millisecond,
	}
	go func() {
		_ = httpServer.Serve(listener)
	}()
	t.Cleanup(func() { _ = httpServer.Close() })

	type watchResult struct {
		pings int
		err   error
	}
	results := make(chan watchResult, 1)
	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(
			"http://"+listener.Addr().String()+"/v1/events/watch",
			"application/json", strings.NewReader("{}"),
		)
		if err != nil {
			results <- watchResult{err: err}
			return
		}
		defer resp.Body.Close()

		reader := bufio.NewReader(resp.Body)
		deadline := time.Now().Add(3 * time.Second)
		pings := 0
		for time.Now().Before(deadline) {
			line, err := reader.ReadString('\n')
			if err != nil {
				results <- watchResult{pings: pings, err: err}
				return
			}
			if strings.HasPrefix(line, ": ping") {
				pings++
				if pings >= 2 {
					results <- watchResult{pings: pings}
					return
				}
			}
		}
		results <- watchResult{pings: pings, err: errors.New("keepalive deadline reached")}
	}()

	select {
	case result := <-results:
		if result.err != nil {
			t.Fatalf("stream ended early after %d ping lines: %v", result.pings, result.err)
		}
		if result.pings < 2 {
			t.Fatalf("stream survived only %d keepalive pings", result.pings)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("watch stream did not deliver two keepalive pings in time")
	}
}
