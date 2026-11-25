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

	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/pkg/observation"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/rs/xid"
)

const (
	// Collection is the storage collection for profiling aggregation windows.
	Collection = "profiling_metadata"

	fieldProfileType = "profile_data.profile_type"
)

type mapper struct{}

func (mapper) ID(*Document) string {
	return xid.New().String()
}

func (mapper) Encode(document *Document) ([]byte, error) {
	return json.Marshal(document)
}

func (mapper) Decode(record driver.Record) (*Document, error) {
	var document Document
	if err := json.Unmarshal(record.Data, &document); err != nil {
		return nil, err
	}
	if err := document.validate(); err != nil {
		return nil, err
	}
	return &document, nil
}

func (mapper) Fields(document *Document) (map[string]any, error) {
	if err := document.validate(); err != nil {
		return nil, err
	}
	fields := map[string]any{
		types.DocumentFieldUploadedTimestamp: document.UploadedTimestamp,
		types.DocumentFieldStartedTimestamp:  *document.StartedTimestamp,
		types.DocumentFieldTracerName:        document.TracerName,
		types.DocumentFieldTracerID:          document.TracerID,
		types.DocumentFieldTracerType:        document.TracerRunType,
		fieldProfileType:                     document.ProfileData.ProfileType,
	}
	document.Metadata.AddFields(fields)

	return fields, nil
}

func (mapper) Indexes() []driver.Index {
	return []driver.Index{
		{Field: types.DocumentFieldTracerID},
		{Field: observation.FieldHostname},
		{Field: observation.FieldRegion},
		{Field: types.DocumentFieldUploadedTimestamp},
		{Field: types.DocumentFieldStartedTimestamp},
		{Field: observation.FieldContainerID},
		{Field: observation.FieldContainerName},
		{Field: observation.FieldContainerHostname},
		{Field: observation.FieldContainerNamespace},
		{Field: observation.FieldContainerType},
		{Field: observation.FieldContainerQoS},
		{Field: types.DocumentFieldTracerName},
		{Field: types.DocumentFieldTracerType},
		{Field: fieldProfileType},
	}
}
