//go:build integration

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

package guestbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/storage/vsock"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
)

// The fixture exercises the native Elasticsearch encoder and bulk flush without
// claiming validation against an actual Elasticsearch server.
func newProfileFixture(t *testing.T) (*profilingstore.Store, <-chan []byte) {
	t.Helper()
	records := make(chan []byte, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if r.URL.Path == "/" {
			_, _ = fmt.Fprint(w, `{"version":{"number":"8.12.0"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/_bulk") || r.URL.Path == "/_bulk" {
			scanner := bufio.NewScanner(r.Body)
			scanner.Buffer(make([]byte, 4096), 2<<20)
			var items []map[string]any
			for scanner.Scan() {
				if !scanner.Scan() {
					t.Error("incomplete bulk document")
					break
				}
				records <- append([]byte(nil), scanner.Bytes()...)
				items = append(items, map[string]any{"index": map[string]any{"status": 201, "_id": "profile-window"}})
			}
			if err := scanner.Err(); err != nil {
				t.Errorf("bulk read: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": false, "items": items})
			return
		}
		http.Error(w, "unexpected profile storage request", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	store, err := profilingstore.NewFromConfig(
		t.Context(),
		profilingstore.Config{Addresses: []string{server.URL}, Index: "kata_profiles"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	return store, records
}

func TestTypedProfileIngress(t *testing.T) {
	store, records := newProfileFixture(t)
	receiver := &DocumentReceiver{Hostname: "trusted-node", Region: "region", Profiling: store}
	ctx := context.WithValue(
		t.Context(),
		peerIdentityKey{},
		PeerIdentity{SandboxID: "orders-vm", Namespace: "application"},
	)
	raw := []byte(
		`{"tracer_id":"profile-window","tracer_name":"profiler","tracer_type":"profiling",` +
			`"started_timestamp":"2026-10-16T01:00:00Z","profile_data":{"profile_type":"cpu",` +
			`"profile":{"time_nanos":9007199254740993,"sample":[{"value":[9007199254740993]}]}}}`,
	)
	if err := receiver.Handle(ctx, vsock.ProfilingTool, raw); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-records:
		document, err := profilingstore.DecodeDocument(data)
		if err != nil {
			t.Fatal(err)
		}
		if document.ProfileData.Profile.Sample[0].Value[0] != 9007199254740993 || document.Hostname != "trusted-node" {
			t.Errorf("profile=%+v", document)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("profile did not reach native bulk storage")
	}
}
