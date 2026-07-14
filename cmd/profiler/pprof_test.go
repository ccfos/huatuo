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

package main

import (
	"context"
	"strings"
	"testing"
)

func TestStartPprofServer(t *testing.T) {
	server, err := startPprofServer(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start pprof server: %v", err)
	}

	if err := server.Close(); err != nil {
		t.Fatalf("close pprof server: %v", err)
	}
}

func TestStartPprofServerListenError(t *testing.T) {
	const address = "invalid-address"

	server, err := startPprofServer(context.Background(), address)
	if err == nil {
		if server != nil {
			_ = server.Close()
		}
		t.Fatal("start pprof server unexpectedly succeeded")
	}

	want := "listen for pprof server on " + address + ":"
	if got := err.Error(); !strings.HasPrefix(got, want) {
		t.Fatalf("start pprof server error=%q, want prefix %q", got, want)
	}
}
