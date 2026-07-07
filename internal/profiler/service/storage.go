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

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/strutil"
	"huatuo-bamai/internal/profiler/timeutil"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

const (
	defaultESIndex                = "huatuo-bamai"
	profileMetadataCollection     = "profiling_metadata"
	profileFieldHostname          = "hostname"
	profileFieldRegion            = "region"
	profileFieldUploadedTime      = "uploaded_time"
	profileFieldTime              = "time"
	profileFieldContainerID       = "container_id"
	profileFieldContainerHostname = "container_hostname"
	profileFieldContainerHostNS   = "container_host_namespace"
	profileFieldContainerType     = "container_type"
	profileFieldContainerQOS      = "container_qos"
	profileFieldTracerName        = "tracer_name"
	profileFieldTracerID          = "tracer_id"
	profileFieldTracerTime        = "tracer_time"
	profileFieldTracerType        = "tracer_type"
	profileFieldProfileType       = "tracer_data.flamedata.profile_type"

	profileTimeLayout = "2006-01-02 15:04:05.000 -0700"
)

// ProfileDocument defines the document structure used in profiling storage.
type ProfileDocument struct {
	Hostname     string    `json:"hostname"`
	Region       string    `json:"region"`
	UploadedTime time.Time `json:"uploaded_time"`
	// equal to `TracerTime`, supported the old version.
	Time string `json:"time"`

	// container
	ContainerID            string `json:"container_id,omitempty"`
	ContainerHostname      string `json:"container_hostname,omitempty"`
	ContainerHostNamespace string `json:"container_host_namespace,omitempty"`
	ContainerType          string `json:"container_type,omitempty"`
	ContainerQOS           string `json:"container_qos,omitempty"`

	TracerName    string `json:"tracer_name,omitempty"`
	TracerID      string `json:"tracer_id,omitempty"`
	TracerTime    string `json:"tracer_time"`
	TracerRunType string `json:"tracer_type,omitempty"`

	TracerData struct {
		Flamedata struct {
			ProfileType string            `json:"profile_type,omitempty"`
			Profile     profilev1.Profile `json:"profile,omitempty"`
		} `json:"flamedata,omitempty"`
		// others
	} `json:"tracer_data,omitempty"`
}

// SearchFilter defines the search filter.
type SearchFilter struct {
	ID                string
	Hostname          string
	ContainerHostname string
	TracerID          string
	StartTime         time.Time
	EndTime           time.Time
	ProfileType       string
	Limit             int
}

// ProfileStorage implements profile document queries on top of the new storage backend.
type ProfileStorage struct {
	store *storage.Store[*ProfileDocument]
}

type profileDocumentMapper struct{}

// NewProfileStorage creates a profiling storage.
func NewProfileStorage(address, username, password, index string) (*ProfileStorage, error) {
	if index == "" {
		index = defaultESIndex
	}

	profileStore, err := storage.NewFromConfig[*ProfileDocument](context.Background(), &driver.Config{
		Driver:      "elasticsearch",
		ESAddresses: splitProfileStorageAddresses(address),
		ESUsername:  username,
		ESPassword:  password,
		ESIndex:     index,
	}, profileMetadataCollection, profileDocumentMapper{})
	if err != nil {
		return nil, err
	}

	log.Infof("Initialize profile storage successfully, driver: elasticsearch, index: %s", index)
	return &ProfileStorage{
		store: profileStore,
	}, nil
}

// SearchProfiles searches profiles by SearchFilter.
func (s *ProfileStorage) SearchProfiles(filter *SearchFilter) ([]*ProfileDocument, error) {
	query := buildProfileSearchQuery(filter)

	documents, err := s.store.Query(context.Background(), query)
	if err != nil {
		return nil, err
	}

	return documents, nil
}

// AggregationsByField gets aggregations by field.
func (s *ProfileStorage) AggregationsByField(filter *SearchFilter, field string) ([]string, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("profile storage is nil")
	}

	normalizedField, err := normalizeProfileAggregationField(field)
	if err != nil {
		return nil, err
	}

	terms, err := s.store.Values(
		context.Background(),
		normalizedField,
		buildProfileAggregationQuery(filter),
		normalizeProfileSearchLimit(filter),
	)
	if err != nil {
		return nil, err
	}

	return terms, nil
}

func (profileDocumentMapper) ID(document *ProfileDocument) string {
	return document.TracerID
}

func (profileDocumentMapper) Encode(document *ProfileDocument) ([]byte, error) {
	return json.Marshal(document)
}

func (profileDocumentMapper) Decode(data []byte) (*ProfileDocument, error) {
	var document ProfileDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}

	return &document, nil
}

func (profileDocumentMapper) Fields(document *ProfileDocument) (map[string]any, error) {
	return map[string]any{
		profileFieldHostname:          document.Hostname,
		profileFieldRegion:            document.Region,
		profileFieldUploadedTime:      document.UploadedTime,
		profileFieldTime:              parseProfileDocumentTime(document.Time, document.UploadedTime),
		profileFieldContainerID:       document.ContainerID,
		profileFieldContainerHostname: document.ContainerHostname,
		profileFieldContainerHostNS:   document.ContainerHostNamespace,
		profileFieldContainerType:     document.ContainerType,
		profileFieldContainerQOS:      document.ContainerQOS,
		profileFieldTracerName:        document.TracerName,
		profileFieldTracerID:          document.TracerID,
		profileFieldTracerTime:        parseProfileDocumentTime(document.TracerTime, document.UploadedTime),
		profileFieldTracerType:        document.TracerRunType,
		profileFieldProfileType:       document.TracerData.Flamedata.ProfileType,
	}, nil
}

func (profileDocumentMapper) Indexes() []driver.Index {
	return []driver.Index{
		{Field: profileFieldTracerID},
		{Field: profileFieldHostname},
		{Field: profileFieldRegion},
		{Field: profileFieldUploadedTime},
		{Field: profileFieldTime},
		{Field: profileFieldContainerID},
		{Field: profileFieldContainerHostname},
		{Field: profileFieldContainerHostNS},
		{Field: profileFieldContainerType},
		{Field: profileFieldContainerQOS},
		{Field: profileFieldTracerName},
		{Field: profileFieldTracerTime},
		{Field: profileFieldTracerType},
		{Field: profileFieldProfileType},
	}
}

func buildProfileSearchQuery(filter *SearchFilter) driver.Query {
	query := buildProfileAggregationQuery(filter)
	query.Limit = normalizeProfileSearchLimit(filter)
	query.Sorts = []driver.Sort{
		{Field: profileFieldTime, Desc: true},
	}
	return query
}

func buildProfileAggregationQuery(filter *SearchFilter) driver.Query {
	query := driver.Query{
		Filters: make([]driver.Filter, 0, 6),
	}

	if filter == nil {
		return query
	}

	if !filter.StartTime.IsZero() {
		query.Filters = append(query.Filters, driver.Filter{
			Field: profileFieldTime,
			Op:    driver.OpGte,
			Value: filter.StartTime.UTC(),
		})
	}
	if !filter.EndTime.IsZero() {
		query.Filters = append(query.Filters, driver.Filter{
			Field: profileFieldTime,
			Op:    driver.OpLte,
			Value: filter.EndTime.UTC(),
		})
	}

	switch {
	case filter.TracerID != "" || filter.ID != "":
		id := filter.TracerID
		if id == "" {
			id = filter.ID
		}
		query.Filters = append(query.Filters, driver.Filter{
			Field: profileFieldTracerID,
			Op:    driver.OpEq,
			Value: id,
		})
	case filter.Hostname != "":
		query.Filters = append(
			query.Filters,
			driver.Filter{
				Field: profileFieldHostname,
				Op:    driver.OpEq,
				Value: filter.Hostname,
			},
			driver.Filter{
				Field: profileFieldContainerHostname,
				Op:    driver.OpEq,
				Value: "",
			},
		)
	case filter.ContainerHostname != "":
		query.Filters = append(query.Filters, driver.Filter{
			Field: profileFieldContainerHostname,
			Op:    driver.OpEq,
			Value: filter.ContainerHostname,
		})
	}

	if filter.ProfileType != "" {
		query.Filters = append(query.Filters, driver.Filter{
			Field: profileFieldProfileType,
			Op:    driver.OpEq,
			Value: filter.ProfileType,
		})
	}

	if filter.TracerID != "" {
		query.Filters = append(query.Filters, driver.Filter{
			Field: profileFieldTracerID,
			Op:    driver.OpEq,
			Value: filter.TracerID,
		})
	}

	return query
}

func normalizeProfileAggregationField(field string) (string, error) {
	switch field {
	case "id":
		return profileFieldTracerID, nil
	case profileFieldRegion,
		profileFieldHostname,
		profileFieldContainerHostname,
		profileFieldContainerHostNS,
		profileFieldContainerID,
		profileFieldContainerType,
		profileFieldContainerQOS,
		profileFieldTracerName,
		profileFieldTracerID,
		profileFieldTracerType,
		profileFieldProfileType:
		return field, nil
	default:
		return "", fmt.Errorf("invalid aggregation field: %q", field)
	}
}

func normalizeProfileSearchLimit(filter *SearchFilter) int {
	if filter == nil || filter.Limit <= 0 {
		return 100
	}
	return filter.Limit
}

func parseProfileDocumentTime(raw string, fallback time.Time) time.Time {
	return timeutil.ParseWithFallback(raw, profileTimeLayout, fallback)
}

func splitProfileStorageAddresses(raw string) []string {
	return strutil.SplitCommaList(raw)
}
