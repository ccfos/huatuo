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

func TestNewBaseDocumentIncludesNodeIP(t *testing.T) {
	document, err := newBaseDocument(DocumentOptions{
		Hostname: "node-a",
		Region:   "region-a",
		NodeIP:   "192.0.2.10",
	}, &WriteRequest{TracerName: "sched_tick", TracerTime: time.Now()})
	if err != nil {
		t.Fatalf("newBaseDocument() error = %v", err)
	}
	if document.NodeIP != "192.0.2.10" {
		t.Fatalf("document node IP = %q, want %q", document.NodeIP, "192.0.2.10")
	}

	fields, err := (DocumentStoreMapper{}).Fields(document)
	if err != nil {
		t.Fatalf("DocumentStoreMapper.Fields() error = %v", err)
	}
	if fields["node_ip"] != "192.0.2.10" {
		t.Fatalf("stored node IP = %v, want %q", fields["node_ip"], "192.0.2.10")
	}
}

func TestDocumentFieldsOmitEmptyNodeIP(t *testing.T) {
	fields, err := (DocumentStoreMapper{}).Fields(&Document{})
	if err != nil {
		t.Fatalf("DocumentStoreMapper.Fields() error = %v", err)
	}
	if _, ok := fields["node_ip"]; ok {
		t.Fatal("stored fields include node_ip, want it omitted")
	}
}
