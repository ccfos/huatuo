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

package profiler

import (
	"encoding/json"
	"time"

	"huatuo-bamai/internal/profiler/timeutil"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/tracing"
)

// MetadataCollection is the storage collection name for profiling metadata documents.
const MetadataCollection = "profiling_metadata"

// ProfilingDocumentMapper maps profiling metadata documents to storage records.
type ProfilingDocumentMapper struct{}

func (ProfilingDocumentMapper) ID(document *tracing.Document) string {
	return document.TracerID
}

func (ProfilingDocumentMapper) Encode(document *tracing.Document) ([]byte, error) {
	return json.Marshal(document)
}

func (ProfilingDocumentMapper) Decode(data []byte) (*tracing.Document, error) {
	var document tracing.Document
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}

	return &document, nil
}

func (ProfilingDocumentMapper) Fields(document *tracing.Document) (map[string]any, error) {
	return map[string]any{
		"record_id":                document.TracerID,
		"hostname":                 document.Hostname,
		"region":                   document.Region,
		"uploaded_time":            document.UploadedTime,
		"time":                     profilingMetadataTimeValue(document.Time, document.UploadedTime),
		"container_id":             document.ContainerID,
		"container_hostname":       document.ContainerHostname,
		"container_host_namespace": document.ContainerHostNamespace,
		"container_type":           document.ContainerType,
		"container_qos":            document.ContainerQoS,
		"tracer_name":              document.TracerName,
		"tracer_id":                document.TracerID,
		"tracer_time":              profilingMetadataTimeValue(document.TracerTime, document.UploadedTime),
		"tracer_type":              document.TracerRunType,
		"profile_type":             extractProfilingMetadataProfileType(document.TracerData),
	}, nil
}

func (ProfilingDocumentMapper) Indexes() []driver.Index {
	return []driver.Index{
		{Field: "record_id"},
		{Field: "hostname"},
		{Field: "region"},
		{Field: "uploaded_time"},
		{Field: "time"},
		{Field: "container_id"},
		{Field: "container_hostname"},
		{Field: "container_host_namespace"},
		{Field: "container_type"},
		{Field: "container_qos"},
		{Field: "tracer_name"},
		{Field: "tracer_id"},
		{Field: "tracer_time"},
		{Field: "tracer_type"},
		{Field: "profile_type"},
	}
}

const profilingMetadataTimeLayout = "2006-01-02 15:04:05.000 -0700"

func profilingMetadataTimeValue(raw string, fallback time.Time) time.Time {
	return timeutil.ParseWithFallback(raw, profilingMetadataTimeLayout, fallback)
}

func extractProfilingMetadataProfileType(tracerData any) string {
	if tracerData == nil {
		return ""
	}

	raw, err := json.Marshal(tracerData)
	if err != nil {
		return ""
	}

	var payload struct {
		FlameData struct {
			ProfileType string `json:"profile_type,omitempty"`
		} `json:"flamedata,omitempty"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}

	return payload.FlameData.ProfileType
}
