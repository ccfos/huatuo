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

//go:build integration

package bpfmock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.json")
	for _, tc := range []struct {
		name, input string
		count       int
		wantError   bool
	}{
		{name: "empty"},
		{name: "records", input: `{"tracer_data":{"pid":42}}` + "\n" + `{"tracer_data":{"pid":43}}`, count: 2},
		{name: "malformed", input: `{"tracer_data":`, wantError: true},
		{name: "wrong field type", input: `{"tracer_data":{"pid":"wrong"}}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.input), 0o600); err != nil {
				t.Fatal(err)
			}
			docs, err := ReadDocuments[struct {
				PID uint32 `json:"pid"`
			}](path)
			if (err != nil) != tc.wantError || len(docs) != tc.count {
				t.Fatalf("ReadDocuments count=%d error=%v", len(docs), err)
			}
			if tc.count == 2 && (docs[0].Data.PID != 42 || docs[1].Data.PID != 43) {
				t.Fatal("incorrect ES payload decoding")
			}
		})
	}
}

func TestMetadata(t *testing.T) {
	valid := Document[struct{}]{TracerName: "softlockup", TracerType: "event", TracerID: "id", Hostname: "node", Region: "test", Observed: "2026-10-13T00:00:00Z", Uploaded: "2026-10-13T00:00:01Z"}
	if err := valid.ValidateMetadata("softlockup"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Document[struct{}]){
		func(d *Document[struct{}]) { d.TracerName = "wrong" },
		func(d *Document[struct{}]) { d.TracerType = "metric" },
		func(d *Document[struct{}]) { d.TracerID = "" },
		func(d *Document[struct{}]) { d.Hostname = "" },
		func(d *Document[struct{}]) { d.Region = "" },
		func(d *Document[struct{}]) { d.Observed = "invalid" },
		func(d *Document[struct{}]) { d.Uploaded = "" },
	} {
		doc := valid
		change(&doc)
		if err := doc.ValidateMetadata("softlockup"); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
}
