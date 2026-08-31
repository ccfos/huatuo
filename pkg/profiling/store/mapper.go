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

func (mapper) Decode(data []byte) (*Document, error) {
	var document Document
	if err := json.Unmarshal(data, &document); err != nil {
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
	return map[string]any{
		types.DocumentFieldHostname:               document.Hostname,
		types.DocumentFieldRegion:                 document.Region,
		types.DocumentFieldUploadedTimestamp:      document.UploadedTimestamp,
		types.DocumentFieldStartedTimestamp:       *document.StartedTimestamp,
		types.DocumentFieldContainerID:            document.ContainerID,
		types.DocumentFieldContainerHostname:      document.ContainerHostname,
		types.DocumentFieldContainerHostNamespace: document.ContainerHostNamespace,
		types.DocumentFieldContainerType:          document.ContainerType,
		types.DocumentFieldContainerQoS:           document.ContainerQoS,
		types.DocumentFieldTracerName:             document.TracerName,
		types.DocumentFieldTracerID:               document.TracerID,
		types.DocumentFieldTracerType:             document.TracerRunType,
		fieldProfileType:                          document.ProfileData.ProfileType,
	}, nil
}

func (mapper) Indexes() []driver.Index {
	return []driver.Index{
		{Field: types.DocumentFieldTracerID},
		{Field: types.DocumentFieldHostname},
		{Field: types.DocumentFieldRegion},
		{Field: types.DocumentFieldUploadedTimestamp},
		{Field: types.DocumentFieldStartedTimestamp},
		{Field: types.DocumentFieldContainerID},
		{Field: types.DocumentFieldContainerHostname},
		{Field: types.DocumentFieldContainerHostNamespace},
		{Field: types.DocumentFieldContainerType},
		{Field: types.DocumentFieldContainerQoS},
		{Field: types.DocumentFieldTracerName},
		{Field: types.DocumentFieldTracerType},
		{Field: fieldProfileType},
	}
}
