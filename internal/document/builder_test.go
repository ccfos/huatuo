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

package document

import (
	"testing"
	"time"

	"github.com/ccfos/huatuo/pkg/types"
)

func TestBuilderBuildsSharedMetadata(t *testing.T) {
	startedTimestamp := time.Date(2026, 8, 28, 10, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	builder := &Builder{region: "cn-north", hostname: "node-1"}
	document, err := builder.Build(&Input{
		TracerName:       "profiler",
		TracerID:         "job-1",
		StartedTimestamp: startedTimestamp,
		TracerRunType:    types.TracerRunTypeProfiling,
	})
	if err != nil {
		t.Fatalf("Builder.Build() error = %v", err)
	}
	if document.Hostname != "node-1" || document.Region != "cn-north" {
		t.Fatalf("node metadata = (%q, %q)", document.Hostname, document.Region)
	}
	if document.TracerID != "job-1" || document.TracerName != "profiler" {
		t.Fatalf("tracer metadata = (%q, %q)", document.TracerID, document.TracerName)
	}
	if document.StartedTimestamp == nil {
		t.Fatal("document started timestamp is nil")
	}
	if want := startedTimestamp.UTC(); !document.StartedTimestamp.Equal(want) {
		t.Fatalf("document started timestamp = %v, want %v", document.StartedTimestamp, want)
	}
	if !document.UploadedTimestamp.IsZero() {
		t.Fatalf("document uploaded timestamp = %v, want zero", document.UploadedTimestamp)
	}
}

func TestNilBuilderIsRejected(t *testing.T) {
	if _, err := (*Builder)(nil).Build(&Input{}); err == nil {
		t.Fatal("Builder.Build() error = nil")
	}
}
