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

package types_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/pkg/types"
)

func nonZeroTimestamp() timeutil.Timestamp {
	return timeutil.Timestamp{Time: time.Now().UTC().Truncate(time.Second)}
}

func nonZeroTimestampPtr() *timeutil.Timestamp {
	ts := nonZeroTimestamp()

	return &ts
}

// TestDocumentValidate pins the storage-boundary timestamp invariant: the
// tracer run type selects which producer-owned timestamp a document must
// carry before it may be persisted.
func TestDocumentValidate(t *testing.T) {
	tests := []struct {
		name    string
		doc     *types.Document
		wantErr string // empty means the document must be accepted
	}{
		{
			name:    "nil document",
			doc:     nil,
			wantErr: "document is required",
		},
		{
			name: "missing uploaded timestamp",
			doc: &types.Document{
				TracerRunType:     types.TracerRunTypeEvent,
				ObservedTimestamp: nonZeroTimestampPtr(),
			},
			wantErr: "uploaded timestamp is required",
		},
		{
			name: "profiling requires started timestamp",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
				TracerRunType:     types.TracerRunTypeProfiling,
			},
			wantErr: "started timestamp is required",
		},
		{
			name: "tracing requires started timestamp",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
				TracerRunType:     types.TracerRunTypeTracing,
			},
			wantErr: "started timestamp is required",
		},
		{
			name: "autotracing requires started timestamp",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
				TracerRunType:     types.TracerRunTypeAutotracing,
			},
			wantErr: "started timestamp is required",
		},
		{
			name: "present but zero started timestamp is rejected",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
				TracerRunType:     types.TracerRunTypeProfiling,
				StartedTimestamp:  &timeutil.Timestamp{},
			},
			wantErr: "started timestamp is required",
		},
		{
			name: "event requires observed timestamp",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
				TracerRunType:     types.TracerRunTypeEvent,
			},
			wantErr: "observed timestamp is required",
		},
		{
			name: "present but zero observed timestamp is rejected",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
				TracerRunType:     types.TracerRunTypeEvent,
				ObservedTimestamp: &timeutil.Timestamp{},
			},
			wantErr: "observed timestamp is required",
		},
		{
			name: "empty tracer type is rejected",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
			},
			wantErr: "is not supported",
		},
		{
			name: "unknown tracer type is rejected",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
				TracerRunType:     "unknown-run-type",
			},
			wantErr: "is not supported",
		},
		{
			name: "profiling document is accepted",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
				TracerRunType:     types.TracerRunTypeProfiling,
				StartedTimestamp:  nonZeroTimestampPtr(),
			},
		},
		{
			name: "tracing document is accepted",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
				TracerRunType:     types.TracerRunTypeTracing,
				StartedTimestamp:  nonZeroTimestampPtr(),
			},
		},
		{
			name: "autotracing document is accepted",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
				TracerRunType:     types.TracerRunTypeAutotracing,
				StartedTimestamp:  nonZeroTimestampPtr(),
			},
		},
		{
			name: "event document is accepted without a started timestamp",
			doc: &types.Document{
				UploadedTimestamp: nonZeroTimestamp(),
				TracerRunType:     types.TracerRunTypeEvent,
				ObservedTimestamp: nonZeroTimestampPtr(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.doc.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Validate() error = nil, want %q", tt.wantErr)
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}
