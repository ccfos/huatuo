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
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
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
	addr := srv.listener.Addr().String()

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

	response, err := http.Get("http://" + srv.listener.Addr().String() + "/debug/pprof/")
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
