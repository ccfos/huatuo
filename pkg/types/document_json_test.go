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

package types_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/timeutil"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestDocumentJSONTimestampFormat(t *testing.T) {
	for _, legacy := range []string{"2026-09-17T08:00:00+08:00", "2026-09-17T00:00:00.123Z", "2026-09-17T00:00:00.123456789Z"} {
		t.Run(legacy, func(t *testing.T) {
			var metadata types.Document
			raw := `{"hostname":"host","uploaded_timestamp":"` + legacy + `","started_timestamp":"` + legacy + `","observed_timestamp":"` + legacy + `","kernel_observed_timestamp":"` + legacy + `"}`
			if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
				t.Fatal(err)
			}
			want := metadata.UploadedTimestamp.UTC().Format("2006-01-02T15:04:05.000000000Z")
			for _, test := range []struct {
				name    string
				value   any
				payload string
			}{
				{"metadata", &metadata, ""},
				{"tracing", &tracingstore.Document{Document: metadata, TracerData: map[string]string{"message": "kept"}}, "tracer_data"},
				{"profiling", &profilingstore.Document{Document: metadata, ProfileData: &profilingstore.ProfileData{ProfileType: "cpu"}}, "profile_data"},
			} {
				t.Run(test.name, func(t *testing.T) {
					encoded, err := json.Marshal(test.value)
					if err != nil {
						t.Fatal(err)
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(encoded, &fields); err != nil {
						t.Fatal(err)
					}
					for _, field := range []string{"uploaded_timestamp", "started_timestamp", "observed_timestamp", "kernel_observed_timestamp"} {
						if string(fields[field]) != `"`+want+`"` {
							t.Fatalf("%s = %s, want %s", field, fields[field], want)
						}
					}
					if string(fields["hostname"]) != `"host"` {
						t.Fatalf("metadata lost: %s", encoded)
					}
					if test.payload != "" && len(fields[test.payload]) == 0 {
						t.Fatalf("payload lost: %s", encoded)
					}
					var decoded types.Document
					if err := json.Unmarshal(encoded, &decoded); err != nil {
						t.Fatal(err)
					}
					if !decoded.UploadedTimestamp.Equal(metadata.UploadedTimestamp.Time) {
						t.Fatal("timestamp changed")
					}
				})
			}
		})
	}
}

func TestDocumentJSONOptionalTimestamps(t *testing.T) {
	raw, err := json.Marshal(&types.Document{UploadedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"started_timestamp", "observed_timestamp", "kernel_observed_timestamp"} {
		if _, ok := fields[field]; ok {
			t.Fatalf("absent timestamp %s serialized", field)
		}
	}
}

func BenchmarkDocumentJSON(b *testing.B) {
	now := time.Date(2026, 9, 17, 0, 0, 0, 123000000, time.UTC)
	document := tracingstore.Document{Document: types.Document{Hostname: "host", UploadedTimestamp: timeutil.Timestamp{Time: now}, ObservedTimestamp: &timeutil.Timestamp{Time: now}, KernelObservedTimestamp: &timeutil.Timestamp{Time: now}}, TracerData: map[string]string{"message": "event"}}
	legacy := struct {
		Hostname                string     `json:"hostname"`
		UploadedTimestamp       time.Time  `json:"uploaded_timestamp"`
		ObservedTimestamp       *time.Time `json:"observed_timestamp,omitempty"`
		KernelObservedTimestamp *time.Time `json:"kernel_observed_timestamp,omitempty"`
		Region                  string     `json:"region"`
		TracerData              any        `json:"tracer_data,omitempty"`
	}{"host", now, &now, &now, "", document.TracerData}
	for _, test := range []struct {
		name   string
		encode func() ([]byte, error)
	}{
		{"legacy", func() ([]byte, error) { return json.Marshal(&legacy) }}, {"canonical", func() ([]byte, error) { return json.Marshal(&document) }},
	} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := test.encode(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
