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

	"github.com/google/go-cmp/cmp"
)

func TestDocumentStoreMapperEncodeDecode(t *testing.T) {
	mapper := DocumentStoreMapper{}
	want := &Document{
		Hostname:     "node-a",
		Region:       "cn-east",
		UploadedTime: time.Date(2026, time.July, 23, 10, 30, 0, 0, time.UTC),
		TracerID:     "trace-1",
		TracerName:   "iotracing",
		TracerData:   "ready",
	}

	encoded, err := mapper.Encode(want)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	got, err := mapper.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Decode(Encode(document)) mismatch (-want +got):\n%s", diff)
	}
}

func TestDocumentStoreMapperFieldsNormalizesTimes(t *testing.T) {
	fallback := time.Date(2026, time.July, 23, 10, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	document := &Document{
		Hostname:               "node-a",
		Region:                 "cn-east",
		UploadedTime:           fallback,
		Time:                   "2026-07-23 10:00:00.000 +0800",
		ContainerID:            "container-1",
		ContainerHostname:      "worker",
		ContainerHostNamespace: "default",
		ContainerType:          "containerd",
		ContainerQoS:           "burstable",
		TracerName:             "iotracing",
		TracerID:               "trace-1",
		TracerTime:             "not-a-time",
		TracerRunType:          TracerRunTypeEvent,
	}

	got, err := (DocumentStoreMapper{}).Fields(document)
	if err != nil {
		t.Fatalf("Fields() error = %v", err)
	}
	wantTime, err := time.Parse(tracingDocumentTimeLayout, document.Time)
	if err != nil {
		t.Fatalf("time.Parse() error = %v", err)
	}
	want := map[string]any{
		"record_id":                "trace-1",
		"hostname":                 "node-a",
		"region":                   "cn-east",
		"uploaded_time":            fallback,
		"time":                     wantTime.UTC(),
		"container_id":             "container-1",
		"container_hostname":       "worker",
		"container_host_namespace": "default",
		"container_type":           "containerd",
		"container_qos":            "burstable",
		"tracer_name":              "iotracing",
		"tracer_id":                "trace-1",
		"tracer_time":              fallback.UTC(),
		"tracer_type":              TracerRunTypeEvent,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Fields() mismatch (-want +got):\n%s", diff)
	}
}
