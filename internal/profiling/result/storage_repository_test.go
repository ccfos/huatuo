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

package result

import (
	"context"
	"testing"
	"time"

	profileservice "huatuo-bamai/internal/profiler/service"
)

type stubProfileStore struct {
	documents []*profileservice.ProfileDocument
	limit     int
	offset    int
}

func (s *stubProfileStore) GetProfilesByTracerIDPage(
	_ context.Context,
	_ string,
	limit int,
	offset int,
) ([]*profileservice.ProfileDocument, error) {
	s.limit = limit
	s.offset = offset
	return s.documents, nil
}

type stubPublicationReader struct {
	published bool
}

func (s *stubPublicationReader) IsPublished(context.Context, string) (bool, error) {
	return s.published, nil
}

func TestStorageRepositoryMapsStoredProfileWithoutCopyingProto(t *testing.T) {
	uploadedAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	document := &profileservice.ProfileDocument{
		Hostname:          "node-1",
		Region:            "region-1",
		UploadedTime:      uploadedAt,
		TracerTime:        "2026-08-24 11:59:00.000 +0000",
		ContainerID:       "container-1",
		ContainerHostname: "workload-1",
		ContainerType:     "docker",
		ContainerQOS:      "burstable",
	}
	document.TracerData.Flamedata.ProfileType = "cpu"
	store := &stubProfileStore{documents: []*profileservice.ProfileDocument{document}}
	repository, err := NewStorageRepository(store, &stubPublicationReader{published: true})
	if err != nil {
		t.Fatalf("NewStorageRepository() error = %v", err)
	}

	profiles, err := repository.List(t.Context(), "job-1", 11, 7)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.limit != 11 || store.offset != 7 {
		t.Fatalf("Store page = (%d, %d)", store.limit, store.offset)
	}
	if len(profiles) != 1 {
		t.Fatalf("List() count = %d, want 1", len(profiles))
	}
	got := profiles[0]
	if got.Profile != &document.TracerData.Flamedata.Profile {
		t.Fatal("List() copied the protobuf profile instead of retaining its pointer")
	}
	if got.Hostname != document.Hostname || got.ProfileType != "cpu" {
		t.Fatalf("mapped profile = %+v", got)
	}
	if want := time.Date(2026, 8, 24, 11, 59, 0, 0, time.UTC); !got.CapturedAt.Equal(want) {
		t.Fatalf("CapturedAt = %s, want %s", got.CapturedAt, want)
	}
}

func TestStorageRepositoryRejectsNilDocument(t *testing.T) {
	repository, err := NewStorageRepository(
		&stubProfileStore{documents: []*profileservice.ProfileDocument{nil}},
		&stubPublicationReader{},
	)
	if err != nil {
		t.Fatalf("NewStorageRepository() error = %v", err)
	}
	if _, err := repository.List(t.Context(), "job-1", 1, 0); err == nil {
		t.Fatal("List() error = nil")
	}
}
