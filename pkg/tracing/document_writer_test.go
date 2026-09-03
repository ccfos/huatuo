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

func TestNewBaseDocumentIncludesVersion(t *testing.T) {
	document, err := newBaseDocument(DocumentOptions{
		Hostname: "node-a",
		Region:   "region-a",
		Version:  "v1.2.3",
	}, &WriteRequest{TracerName: "sched_tick", TracerTime: time.Now()})
	if err != nil {
		t.Fatalf("newBaseDocument() error = %v", err)
	}
	if document.Version != "v1.2.3" {
		t.Fatalf("document version = %q, want %q", document.Version, "v1.2.3")
	}

	fields, err := (DocumentStoreMapper{}).Fields(document)
	if err != nil {
		t.Fatalf("DocumentStoreMapper.Fields() error = %v", err)
	}
	if fields["version"] != "v1.2.3" {
		t.Fatalf("stored version = %v, want %q", fields["version"], "v1.2.3")
	}
}
