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

	"github.com/rs/xid"
)

// DocumentCollection is the storage collection name for tracing documents.
const DocumentCollection = "tracing_documents"

// DocumentStoreMapper maps tracing documents to storage records.
type DocumentStoreMapper struct{}

// ProfileDocumentStoreMapper preserves every aggregation window for a profiling task.
type ProfileDocumentStoreMapper struct {
	DocumentStoreMapper
}

// ID returns a unique storage ID while tracer_id keeps snapshots queryable as one task.
func (ProfileDocumentStoreMapper) ID(_ *Document) string {
	return xid.New().String()
}

// profileTypeField is the path the profile query service filters on.
const profileTypeField = "tracer_data.flamedata.profile_type"

// Fields adds the profile type to the queryable set.
//
// Elasticsearch indexes the raw document, so this nested value is queryable
// there without being declared; every other backend only ever sees the fields
// named here, and would leave the column empty.
func (m ProfileDocumentStoreMapper) Fields(document *Document) (map[string]any, error) {
	fields, err := m.DocumentStoreMapper.Fields(document)
	if err != nil {
		return nil, err
	}
	if profileType := profileTypeOf(document.TracerData); profileType != "" {
		fields[profileTypeField] = profileType
	}

	return fields, nil
}

// Indexes declares the profile type alongside the shared tracing fields.
func (m ProfileDocumentStoreMapper) Indexes() []driver.Index {
	return append(m.DocumentStoreMapper.Indexes(), driver.Index{Field: profileTypeField})
}

// profileTypeOf reads tracer_data.flamedata.profile_type. TracerData arrives as
// decoded JSON, so it is a map here; anything else yields an empty type rather
// than an error, matching how the field behaves when a tracer omits it.
func profileTypeOf(tracerData any) string {
	data, ok := tracerData.(map[string]any)
	if !ok {
		return ""
	}
	flamedata, ok := data["flamedata"].(map[string]any)
	if !ok {
		return ""
	}
	profileType, _ := flamedata["profile_type"].(string)

	return profileType
}

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
		{Field: "uploaded_time", Kind: driver.KindTime},
		{Field: "time", Kind: driver.KindTime},
		{Field: "container_id"},
		{Field: "container_hostname"},
		{Field: "container_host_namespace"},
		{Field: "container_type"},
		{Field: "container_qos"},
		{Field: "tracer_name"},
		{Field: "tracer_id"},
		{Field: "tracer_time", Kind: driver.KindTime},
		{Field: "tracer_type"},
	}
}
