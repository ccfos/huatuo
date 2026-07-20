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

import "testing"

func TestProfileDocumentStoreMapperUsesUniqueIDs(t *testing.T) {
	mapper := ProfileDocumentStoreMapper{}
	document := &Document{TracerID: "profile-task-2026"}

	first := mapper.ID(document)
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
}
