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

package profiling

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/server"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
)

const protobufBodyLimit = int64(96)

func startProtoBodyLimitServer(t *testing.T, invoked *bool) string {
	t.Helper()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("release reserved address: %v", err)
	}

	srv := server.NewServer(&server.Config{MaxBodyBytes: protobufBodyLimit})
	srv.MustRegisterRoutes("", []server.Route{{
		Method: http.MethodPost,
		Path:   "/profiles",
		Handler: func(ctx *server.Context) error {
			return handleProto(
				ctx,
				"test_profile_types",
				func(
					context.Context,
					*querierv1.ProfileTypesRequest,
				) (*querierv1.ProfileTypesResponse, error) {
					*invoked = true
					return &querierv1.ProfileTypesResponse{}, nil
				},
			)
		},
	}})
	if err := srv.Start(address); err != nil {
		t.Fatalf("Start(%q) error=%v", address, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error=%v", err)
		}
	})
	return "http://" + address + "/profiles"
}

func requireProtoAPIError(
	t *testing.T,
	resp *http.Response,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	defer resp.Body.Close()

	var body v1.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("status=%d, want %d; body=%+v", resp.StatusCode, wantStatus, body)
	}
	if body.Error.Code != v1.ErrorCodeInvalidRequest {
		t.Fatalf("error code=%q, want %q", body.Error.Code, v1.ErrorCodeInvalidRequest)
	}
	if body.Error.Message != wantMessage {
		t.Fatalf("error message=%q, want %q", body.Error.Message, wantMessage)
	}
}

func TestHandleProtoReportsBodyLimit(t *testing.T) {
	invoked := false
	endpoint := startProtoBodyLimitServer(t, &invoked)

	resp, err := http.Post(
		endpoint,
		"application/protobuf",
		strings.NewReader(strings.Repeat("x", int(protobufBodyLimit)+1)),
	)
	if err != nil {
		t.Fatalf("POST oversized protobuf error=%v", err)
	}
	requireProtoAPIError(
		t,
		resp,
		http.StatusRequestEntityTooLarge,
		"request body exceeds 96 bytes",
	)
	if invoked {
		t.Fatal("profile query was invoked for an oversized request")
	}
}

func TestHandleProtoKeepsMalformedBodyAsBadRequest(t *testing.T) {
	invoked := false
	endpoint := startProtoBodyLimitServer(t, &invoked)

	resp, err := http.Post(
		endpoint,
		"application/protobuf",
		strings.NewReader("not protobuf"),
	)
	if err != nil {
		t.Fatalf("POST malformed protobuf error=%v", err)
	}
	requireProtoAPIError(t, resp, http.StatusBadRequest, "invalid protobuf request")
	if invoked {
		t.Fatal("profile query was invoked for a malformed request")
	}
}
