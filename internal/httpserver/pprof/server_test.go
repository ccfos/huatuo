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
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStartDefaultAddressInUse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:6000")
	if err != nil && !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("occupy default pprof address: %v", err)
	}

	// An existing listener already provides the conflict; close only our own.
	if listener != nil {
		t.Cleanup(func() {
			if err := listener.Close(); err != nil {
				t.Errorf("close default-address listener: %v", err)
			}
		})
	}

	server, err := StartDefault(t.Context())
	if server != nil {
		if err := server.Close(); err != nil {
			t.Errorf("close unexpected pprof server: %v", err)
		}
		t.Fatal("StartDefault() on an occupied address returned a server, want nil")
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("StartDefault() on an occupied address error=%v, want EADDRINUSE", err)
	}

	want := "listen for pprof server on 127.0.0.1:6000:"
	if got := err.Error(); !strings.HasPrefix(got, want) {
		t.Fatalf("StartDefault() error=%q, want prefix %q", got, want)
	}
}

func TestServerServesProfiles(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for pprof server: %v", err)
	}

	server, err := Start(t.Context(), listener)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("start pprof server: %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close pprof server: %v", err)
		}
	})

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/debug/pprof/")
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for pprof server: %v", err)
	}

	address := listener.Addr().String()
	server := start(t.Context(), listener)
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("close pprof server: %v", err)
		}
	})

	if err := server.Close(); err != nil {
		t.Fatalf("first Close() error=%v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second Close() error=%v", err)
	}

	// Rebinding the same address proves Close released the listener.
	listener, err = net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listen on %s after Close: %v", address, err)
	}

	restarted := start(t.Context(), listener)
	if err := restarted.Close(); err != nil {
		t.Fatalf("close restarted pprof server: %v", err)
	}
}
