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

package tracing

import (
	"testing"
	"time"
)

// TestNewBaseDocumentEmptyContainerID pins the contract the net_rx/net_tx
// latency resolvers rely on when they yield ("", true) for an un-attributable
// or host-netns event: an empty ContainerID must store the document with no
// error and the payload preserved, rather than fail or silently drop it. This
// is also load-bearing for host-level link-status events (netdev_events.go),
// which call tracing.Save with no ContainerID at all.
func TestNewBaseDocumentEmptyContainerID(t *testing.T) {
	payload := map[string]any{"lat_ms": float64(12.34)}
	req := &WriteRequest{
		TracerName: "net_tx_latency",
		TracerTime: time.Now(),
		TracerData: payload,
		// ContainerID intentionally empty.
	}

	doc, err := newBaseDocument(DocumentOptions{}, req)
	if err != nil {
		t.Fatalf("newBaseDocument with empty ContainerID returned error: %v", err)
	}
	if doc == nil {
		t.Fatal("newBaseDocument returned nil document")
	}

	// latency payload preserved, not silently lost
	if doc.TracerName != "net_tx_latency" {
		t.Errorf("TracerName = %q, want net_tx_latency", doc.TracerName)
	}
	if doc.TracerData == nil {
		t.Error("TracerData lost on empty ContainerID")
	}

	// container-enrichment fields stay empty (no container was resolved)
	for _, field := range []string{
		doc.ContainerID, doc.ContainerHostname, doc.ContainerType, doc.ContainerQoS,
	} {
		if field != "" {
			t.Errorf("container field = %q, want empty (no container resolved)", field)
		}
	}
}
