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

package pprof

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestServerServesProfiles(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for pprof server: %v", err)
	}
	server, err := Start(context.Background(), listener)
	if err != nil {
		t.Fatalf("Start() error=%v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close pprof server: %v", err)
		}
	})

	response, err := http.Get("http://" + listener.Addr().String() + "/debug/pprof/")
	if err != nil {
		t.Fatalf("get pprof index: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read pprof index: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pprof index status=%d, want %d", response.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(body), "Types of profiles available") {
		t.Fatal("pprof index does not list profiles")
	}
}

func TestServerCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for pprof server: %v", err)
	}
	server, err := Start(context.Background(), listener)
	if err != nil {
		t.Fatalf("Start() error=%v", err)
	}

	if err := server.Close(); err != nil {
		t.Fatalf("first Close() error=%v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second Close() error=%v", err)
	}
}

func TestStartRejectsNilListener(t *testing.T) {
	t.Parallel()

	_, err := Start(context.Background(), nil)
	if err == nil || err.Error() != "start pprof server: listener is nil" {
		t.Fatalf("Start() error=%v, want nil listener error", err)
	}
}
