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
	"testing"
)

func TestProfileDocumentStoreMapperUsesUniqueIDs(t *testing.T) {
	mapper := ProfileDocumentStoreMapper{}
	document := &Document{TracerID: "profile-task-2026"}

	fields, err := mapper.Fields(document)
	if err != nil {
		t.Fatalf("Fields() error = %v", err)
	}
	first := mapper.ID(document)
	if fields["profile_storage_id"] != first {
		t.Fatalf("profile_storage_id field = %v, want %q", fields["profile_storage_id"], first)
	}
	encoded, err := mapper.Encode(document)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	var source Document
	if err := json.Unmarshal(encoded, &source); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if source.ProfileStorageID != first {
		t.Fatalf("encoded profile storage ID = %q, want %q", source.ProfileStorageID, first)
	}

	if _, err := mapper.Fields(document); err != nil {
		t.Fatalf("second Fields() error = %v", err)
	}
	second := mapper.ID(document)
	if first == "" || second == "" {
		t.Fatal("ProfileDocumentStoreMapper.ID() returned an empty ID")
	}
	if first == second {
		t.Fatalf("ProfileDocumentStoreMapper.ID() returned duplicate ID %q", first)
	}
	if document.TracerID != "profile-task-2026" {
		t.Fatalf("ProfileDocumentStoreMapper.ID() changed tracer ID to %q", document.TracerID)
	}

	foundIndex := false
	for _, index := range mapper.Indexes() {
		if index.Field == "profile_storage_id" {
			foundIndex = true
			break
		}
	}
	if !foundIndex {
		t.Fatal("ProfileDocumentStoreMapper.Indexes() is missing profile_storage_id")
	}
}
