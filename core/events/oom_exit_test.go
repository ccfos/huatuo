/*
 * Copyright 2026 The HuaTuo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package events

import (
	"context"
	"reflect"
	"testing"
	"time"

	"huatuo-bamai/internal/bpf/abi"
)

func TestOOMExitEventCacheCorrelatesAndDecodes(t *testing.T) {
	cache := newOOMExitEventCache()
	event := &abi.OOMExitEvent{
		VictimTGID:  42,
		Timestamp:   101,
		GoBuildInfo: 1,
	}
	event.CmdlineLen = uint16(copy(
		event.VictimCmdline[:], "python3\x00worker.py\x00"))
	event.EnvironLen = uint16(copy(
		event.VictimEnviron[:],
		"ROLE=worker\x00GITHUB_TOKEN=top-secret\x00EMPTY=\x00"))
	cache.store(event)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := cache.wait(canceled, 42, 999); got != nil {
		t.Fatalf("wrong timestamp matched: %#v", got)
	}
	got := cache.wait(context.Background(), 42, 101)
	if got == nil {
		t.Fatal("matching exit context is nil")
	}
	if got.cmdline != "python3 worker.py" {
		t.Fatalf("cmdline = %q", got.cmdline)
	}
	if !reflect.DeepEqual(got.environ, []string{
		"ROLE=worker",
		"GITHUB_TOKEN=" + oomEnvironRedactedValue,
		"EMPTY=",
	}) {
		t.Fatalf("environ = %q", got.environ)
	}
	if !got.goBuildInfo {
		t.Fatal("Go buildinfo marker was not preserved")
	}
}

func TestRedactOOMEnviron(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ROLE=worker", "ROLE=worker"},
		{"GOMEMLIMIT=8GiB", "GOMEMLIMIT=8GiB"},
		{"EMPTY=", "EMPTY="},
		{"EMBEDDED_EQUALS=a=b=c", "EMBEDDED_EQUALS=a=b=c"},
		{"DB_PASSWORD=value", "DB_PASSWORD=" + oomEnvironRedactedValue},
		{"LEGACY_PASSWD=value", "LEGACY_PASSWD=" + oomEnvironRedactedValue},
		{"SERVICE_TOKEN=value", "SERVICE_TOKEN=" + oomEnvironRedactedValue},
		{"JWT_SECRET=value", "JWT_SECRET=" + oomEnvironRedactedValue},
		{"CLOUD_CREDENTIAL=value", "CLOUD_CREDENTIAL=" + oomEnvironRedactedValue},
		{"API_KEY=value", "API_KEY=" + oomEnvironRedactedValue},
		{"ACCESS_KEY_ID=value", "ACCESS_KEY_ID=" + oomEnvironRedactedValue},
		{"PRIVATE_KEY_DATA=value", "PRIVATE_KEY_DATA=" + oomEnvironRedactedValue},
		{"HTTP_COOKIE=value", "HTTP_COOKIE=" + oomEnvironRedactedValue},
		{"USER_SESSION=value", "USER_SESSION=" + oomEnvironRedactedValue},
		{"proxy_auth=value", "proxy_auth=" + oomEnvironRedactedValue},
		{"EMPTY_TOKEN=", "EMPTY_TOKEN=" + oomEnvironRedactedValue},
		{"DATABASE_URL=value", "DATABASE_URL=" + oomEnvironRedactedValue},
		{"DB_URL=value", "DB_URL=" + oomEnvironRedactedValue},
		{"REDIS_URL=value", "REDIS_URL=" + oomEnvironRedactedValue},
		{"MONGO_URI=value", "MONGO_URI=" + oomEnvironRedactedValue},
		{"DATABASE_DSN=value", "DATABASE_DSN=" + oomEnvironRedactedValue},
		{"CONNECTION_STRING=value", "CONNECTION_STRING=" + oomEnvironRedactedValue},
		{"SERVICE_ENDPOINT=value", "SERVICE_ENDPOINT=" + oomEnvironRedactedValue},
		{"MALFORMED_SECRET", oomEnvironRedactedValue},
		{"=missing-name", oomEnvironRedactedValue},
	}

	for _, test := range tests {
		original := []string{test.input}
		got := redactOOMEnviron(original)
		if !reflect.DeepEqual(got, []string{test.want}) {
			t.Errorf("redactOOMEnviron(%q) = %q, want %q",
				test.input, got, test.want)
		}
		if original[0] != test.input {
			t.Errorf("input mutated: %q", original)
		}
		if gotAgain := redactOOMEnviron(got); !reflect.DeepEqual(gotAgain, got) {
			t.Errorf("redaction is not idempotent: %q", gotAgain)
		}
	}
}

func TestOOMExitEventCachePreservesTruncation(t *testing.T) {
	cache := newOOMExitEventCache()
	event := &abi.OOMExitEvent{
		VictimTGID:   7,
		Timestamp:    202,
		CmdlineFlags: oomCaptureTrunc,
		EnvironFlags: oomCaptureTrunc,
	}
	event.CmdlineLen = uint16(copy(event.VictimCmdline[:], "node"))
	event.EnvironLen = uint16(copy(event.VictimEnviron[:], "A=B\x00"))
	cache.store(event)

	got := cache.wait(context.Background(), 7, 202)
	if got == nil || !got.cmdlineTruncated || !got.environTruncated {
		t.Fatalf("truncation flags were not preserved: %#v", got)
	}
}

func TestOOMExitEventCacheWakesConcurrentWaiters(t *testing.T) {
	cache := newOOMExitEventCache()
	key := newOOMExitKey(9, 303)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	var sharedReady chan struct{}
	for range 2 {
		if got := cache.wait(canceled, 9, 303); got != nil {
			t.Fatalf("unexpected exit context: %#v", got)
		}
		cache.mu.Lock()
		entry := cache.entries[key]
		cache.mu.Unlock()
		if entry == nil {
			t.Fatal("shared ready channel was not registered")
		}
		if sharedReady == nil {
			sharedReady = entry.ready
		} else if entry.ready != sharedReady {
			t.Fatal("waits did not share the ready channel")
		}
	}

	results := make(chan *oomExitContext, 2)
	for range 2 {
		go func() {
			results <- cache.wait(context.Background(), 9, 303)
		}()
	}

	event := &abi.OOMExitEvent{
		VictimTGID: 9,
		Timestamp:  303,
	}
	event.CmdlineLen = uint16(copy(event.VictimCmdline[:], "java\x00"))
	cache.store(event)

	for range 2 {
		select {
		case got := <-results:
			if got == nil || got.cmdline != "java" {
				t.Fatalf("correlated context = %#v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent waiter was not woken")
		}
	}
}
