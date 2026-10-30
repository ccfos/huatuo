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

package mcp

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ccfos/huatuo/internal/mcp/tool"
)

type writeDeadlineListener struct {
	net.Listener
	deadlines chan time.Time
}

func (l *writeDeadlineListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &writeDeadlineConn{Conn: conn, deadlines: l.deadlines}, nil
}

type writeDeadlineConn struct {
	net.Conn
	deadlines chan time.Time
}

func (c *writeDeadlineConn) SetWriteDeadline(deadline time.Time) error {
	if err := c.Conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	select {
	case c.deadlines <- deadline:
	default:
	}
	return nil
}

func TestHTTPWriteDeadlineOverridesOuterServerThroughGin(t *testing.T) {
	for _, test := range []struct {
		name, method              string
		eventTimeout, promTimeout time.Duration
		want                      time.Duration
	}{
		{name: "default POST", method: http.MethodPost, want: 65 * time.Second},
		{name: "longer Prometheus", method: http.MethodPost, eventTimeout: time.Second, promTimeout: 2 * time.Second, want: 7 * time.Second},
		{name: "longer events DELETE", method: http.MethodDelete, eventTimeout: 3 * time.Second, promTimeout: time.Second, want: 8 * time.Second},
		{name: "GET notification stream", method: http.MethodGet},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, err := NewHandler(&Config{
				EventConfig: tool.EventConfig{ToolTimeout: test.eventTimeout},
				Prometheus:  tool.PrometheusConfig{ToolTimeout: test.promTimeout},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = server.Shutdown(t.Context()) }()
			router := gin.New()
			router.Any(Endpoint, func(ctx *gin.Context) {
				server.ServeHTTP(ctx.Writer, ctx.Request)
			})
			httpServer := httptest.NewUnstartedServer(router)
			deadlines := make(chan time.Time, 16)
			httpServer.Listener = &writeDeadlineListener{Listener: httpServer.Listener, deadlines: deadlines}
			httpServer.Config.WriteTimeout = 100 * time.Millisecond
			httpServer.Start()
			defer httpServer.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, test.method, httpServer.URL+Endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			before := time.Now()
			response, err := httpServer.Client().Do(request)
			after := time.Now()
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("HTTP status=%d", response.StatusCode)
			}
			var applied time.Time
			// Both deadlines must reach the socket before response headers arrive.
			for range 2 {
				select {
				case applied = <-deadlines:
				default:
					t.Fatal("MCP did not override the outer write deadline before responding")
				}
			}
			if test.want == 0 {
				if !applied.IsZero() {
					t.Fatalf("GET stream retained write deadline: %s", applied)
				}
			} else if applied.Before(before.Add(test.want)) || applied.After(after.Add(test.want)) {
				t.Fatalf("write deadline=%s, want request entry plus %s", applied, test.want)
			}
		})
	}
}
