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

package store

import (
	"encoding/json"

	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/types"
)

const (
	// Collection is the storage collection for tracing documents.
	Collection = "tracing_documents"

	// fieldRecordID mirrors tracer_id for compatibility with existing queries.
	fieldRecordID = "record_id"
)

// Mapper maps tracing documents to backend records.
type Mapper struct{}

func (Mapper) ID(document *Document) string {
	return document.TracerID
}

func (Mapper) Encode(document *Document) ([]byte, error) {
	if err := document.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(document)
}

func (Mapper) Decode(data []byte) (*Document, error) {
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if err := document.validate(); err != nil {
		return nil, err
	}
	return &document, nil
}

func (Mapper) Fields(document *Document) (map[string]any, error) {
	if err := document.validate(); err != nil {
		return nil, err
	}
	fields := map[string]any{
		fieldRecordID:                             document.TracerID,
		types.DocumentFieldHostname:               document.Hostname,
		types.DocumentFieldRegion:                 document.Region,
		types.DocumentFieldUploadedTimestamp:      document.UploadedTimestamp,
		types.DocumentFieldContainerID:            document.ContainerID,
		types.DocumentFieldContainerHostname:      document.ContainerHostname,
		types.DocumentFieldContainerHostNamespace: document.ContainerHostNamespace,
		types.DocumentFieldContainerType:          document.ContainerType,
		types.DocumentFieldContainerQoS:           document.ContainerQoS,
		types.DocumentFieldTracerName:             document.TracerName,
		types.DocumentFieldTracerID:               document.TracerID,
		types.DocumentFieldTracerType:             document.TracerRunType,
	}
	if document.StartedTimestamp != nil {
		fields[types.DocumentFieldStartedTimestamp] = *document.StartedTimestamp
	}
	if document.ObservedTimestamp != nil {
		fields[types.DocumentFieldObservedTimestamp] = *document.ObservedTimestamp
	}
	return fields, nil
}

func (Mapper) Indexes() []driver.Index {
	return []driver.Index{
		{Field: fieldRecordID},
		{Field: types.DocumentFieldHostname},
		{Field: types.DocumentFieldRegion},
		{Field: types.DocumentFieldUploadedTimestamp},
		{Field: types.DocumentFieldStartedTimestamp},
		{Field: types.DocumentFieldObservedTimestamp},
		{Field: types.DocumentFieldContainerID},
		{Field: types.DocumentFieldContainerHostname},
		{Field: types.DocumentFieldContainerHostNamespace},
		{Field: types.DocumentFieldContainerType},
		{Field: types.DocumentFieldContainerQoS},
		{Field: types.DocumentFieldTracerName},
		{Field: types.DocumentFieldTracerID},
		{Field: types.DocumentFieldTracerType},
	}
}
