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

package null

import (
	"reflect"
	"testing"
	"time"

	"huatuo-bamai/internal/storage/types"
)

func TestStorageClientWrite(t *testing.T) {
	tests := []struct {
		name   string
		client *StorageClient
		doc    *types.Document
	}{
		{
			name:   "nil document",
			client: &StorageClient{},
			doc:    nil,
		},
		{
			name:   "empty document",
			client: &StorageClient{},
			doc:    &types.Document{},
		},
		{
			name:   "filled document",
			client: &StorageClient{},
			doc: &types.Document{
				Hostname:     "host-1",
				Region:       "cn",
				UploadedTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				Time:         "2026-01-01 00:00:00.000 +0000",
				TracerName:   "cpu",
				TracerID:     "task-1",
				TracerTime:   "2026-01-01 00:00:00.000 +0000",
				TracerData:   "payload",
			},
		},
		{
			name:   "nil receiver",
			client: nil,
			doc: &types.Document{
				TracerName: "memory",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var before *types.Document
			if tt.doc != nil {
				snapshot := *tt.doc
				before = &snapshot
			}

			if err := tt.client.Write(tt.doc); err != nil {
				t.Errorf("Write() returned unexpected error: %v", err)
			}

			if before != nil && !reflect.DeepEqual(*before, *tt.doc) {
				t.Errorf("Write() should not mutate input document, before=%+v, after=%+v", *before, *tt.doc)
			}
		})
	}
}
