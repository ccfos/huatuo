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
	"encoding/json"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage/driver"
)

// DocumentCollection is the storage collection name for tracing documents.
const DocumentCollection = "tracing_documents"

// DocumentStoreMapper maps tracing documents to storage records.
type DocumentStoreMapper struct{}

func tracingDocumentTimeValue(raw string, fallback time.Time) time.Time {
	if raw == "" {
		return fallback.UTC()
	}

	parsed, err := time.Parse(tracingDocumentTimeLayout, raw)
	if err != nil {
		log.Debugf("tracing: parse document time %q: %v", raw, err)
		return fallback.UTC()
	}

	return parsed.UTC()
}

func (DocumentStoreMapper) ID(document *Document) string {
	return document.TracerID
}

func (DocumentStoreMapper) Encode(document *Document) ([]byte, error) {
	return json.Marshal(document)
}

func (DocumentStoreMapper) Decode(data []byte) (*Document, error) {
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}

	return &document, nil
}

func (DocumentStoreMapper) Fields(document *Document) (map[string]any, error) {
	return map[string]any{
		// record_id mirrors tracer_id for backward compatibility with legacy index queries.
		"record_id":                document.TracerID,
		"hostname":                 document.Hostname,
		"region":                   document.Region,
		"uploaded_time":            document.UploadedTime,
		"time":                     tracingDocumentTimeValue(document.Time, document.UploadedTime),
		"container_id":             document.ContainerID,
		"container_hostname":       document.ContainerHostname,
		"container_host_namespace": document.ContainerHostNamespace,
		"container_type":           document.ContainerType,
		"container_qos":            document.ContainerQoS,
		"tracer_name":              document.TracerName,
		"tracer_id":                document.TracerID,
		"tracer_time":              tracingDocumentTimeValue(document.TracerTime, document.UploadedTime),
		"tracer_type":              document.TracerRunType,
	}, nil
}

func (DocumentStoreMapper) Indexes() []driver.Index {
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
	}
}
