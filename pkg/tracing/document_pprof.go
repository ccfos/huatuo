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
	"fmt"
	"time"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/storage/driver"
)

// PprofDocumentStoreMapper encodes profiling documents for profile-native
// backends. Elasticsearch continues to use ProfileDocumentStoreMapper and JSON.
type PprofDocumentStoreMapper struct {
	ProfileDocumentStoreMapper
}

// Encode serializes the embedded pprof profile as protobuf.
func (PprofDocumentStoreMapper) Encode(document *Document) ([]byte, error) {
	profileData, err := profileDataFromDocument(document)
	if err != nil {
		return nil, err
	}
	return profileData.Profile.MarshalVT()
}

// Decode is unsupported because the Pyroscope backend is write-only.
func (PprofDocumentStoreMapper) Decode([]byte) (*Document, error) {
	return nil, driver.ErrUnsupportedOp
}

// Fields adds profile timestamps and type to the document metadata.
func (PprofDocumentStoreMapper) Fields(document *Document) (map[string]any, error) {
	fields, err := (ProfileDocumentStoreMapper{}).Fields(document)
	if err != nil {
		return nil, err
	}

	profileData, err := profileDataFromDocument(document)
	if err != nil {
		return nil, err
	}
	fields["profile_type"] = profileData.ProfileType

	start := time.Unix(0, profileData.Profile.TimeNanos).UTC()
	if profileData.Profile.TimeNanos == 0 {
		start = tracingDocumentTimeValue(document.TracerTime, document.UploadedTime)
	}
	fields["profile_start_time"] = start

	end := start.Add(time.Duration(profileData.Profile.DurationNanos))
	if !end.After(start) {
		end = start.Add(time.Second)
	}
	fields["profile_end_time"] = end

	return fields, nil
}

// Indexes lists metadata consumed by the Pyroscope ingest backend.
func (PprofDocumentStoreMapper) Indexes() []driver.Index {
	indexes := (ProfileDocumentStoreMapper{}).Indexes()
	return append(indexes,
		driver.Index{Field: "profile_type"},
		driver.Index{Field: "profile_start_time"},
		driver.Index{Field: "profile_end_time"},
	)
}

func profileDataFromDocument(document *Document) (*profiler.ProfileData, error) {
	if document == nil {
		return nil, fmt.Errorf("pprof document is nil")
	}

	var profileData *profiler.ProfileData
	switch value := document.TracerData.(type) {
	case *profiler.ProfileData:
		profileData = value
	case profiler.ProfileData:
		profileData = &value
	case interface{ ProfileData() *profiler.ProfileData }:
		profileData = value.ProfileData()
	default:
		return nil, fmt.Errorf(
			"pprof document tracer_data has unsupported type %T",
			document.TracerData,
		)
	}
	if profileData == nil {
		return nil, fmt.Errorf("pprof document is missing tracer_data.flamedata")
	}
	return profileData, nil
}
