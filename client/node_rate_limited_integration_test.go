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

package client_test

import (
	"context"
	"errors"
	"net"
	"testing"

	apiv1 "github.com/ccfos/huatuo/apis/v1"
	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
	"github.com/ccfos/huatuo/client"
	"github.com/ccfos/huatuo/internal/server"
	"github.com/ccfos/huatuo/internal/server/response"
)

// TestNodeClientRecognizesRateLimitedThrottle wires the real node-agent HTTP
// server (the same rate limiter and ErrorStatusMapper chain configured in
// cmd/huatuo-bamai/handlers/server.go) to the real Node client. It guards the
// cross-module contract that the client preserves the "rate_limited" error code
// the server emits for a 429 throttle, instead of misclassifying it as a
// protocol violation.
func TestNodeClientRecognizesRateLimitedThrottle(t *testing.T) {
	srv := server.NewServer(&server.Config{
		RateLimit: &server.RateLimitConfig{RequestsPerSecond: 1, Burst: 1},
		ErrorStatusMapper: response.ChainHTTPStatusMappers(
			nodeapi.HTTPStatusForErrorCode,
			response.LegacyHTTPStatusForErrorCode,
		),
	})
	addr := startServerOnFreePort(t, srv)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	node, err := client.NewNode(&client.NodeConfig{})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}
	address := client.NodeAddress{HostPort: addr}

	// The first request consumes the limiter's burst token and reaches routing
	// (no operation route exists), returning the known "route_not_found" code.
	// This confirms the limiter passed the request through rather than throttling.
	_, firstErr := node.GetOperation(context.Background(), address, "req-1")
	var nodeErr *client.NodeError
	if !errors.As(firstErr, &nodeErr) || nodeErr.Code != apiv1.ErrorCodeRouteNotFound {
		t.Fatalf(
			"first GetOperation() error = %v, want code %q",
			firstErr,
			apiv1.ErrorCodeRouteNotFound,
		)
	}

	// The second request trips the per-key limiter and must surface as a
	// retryable "rate_limited" code, not client_protocol.
	_, secondErr := node.GetOperation(context.Background(), address, "req-2")
	if !errors.As(secondErr, &nodeErr) {
		t.Fatalf("second GetOperation() error = %v, want *client.NodeError", secondErr)
	}
	if nodeErr.Code != apiv1.ErrorCodeRateLimited {
		t.Fatalf(
			"second GetOperation() code = %q, want %q (429 misclassified as protocol)",
			nodeErr.Code,
			apiv1.ErrorCodeRateLimited,
		)
	}
}

func startServerOnFreePort(t *testing.T, srv *server.Server) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved port: %v", err)
	}
	if err := srv.Start(addr); err != nil {
		t.Fatalf("start server: %v", err)
	}
	return addr
}
