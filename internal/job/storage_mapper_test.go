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

package job

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/pkg/observation"
	"github.com/ccfos/huatuo/pkg/profiling"
)

func storedRecordWithDuration(t *testing.T, seconds int64) driver.Record {
	t.Helper()
	payload := storagePayload{
		SchemaVersion:   currentStorageSchemaVersion,
		ID:              "job-1",
		Kind:            KindProfiling,
		UserID:          "user-1",
		Hostname:        "node-1",
		DurationSeconds: seconds,
		Scope:           string(observation.ScopeHost),
		Spec:            Spec{Profiling: &profiling.Spec{Type: profiling.TypeCPU, Language: profiling.LanguageGo, Mode: profiling.ModeOnCPU}},
		Status:          StatusPending,
		CreatedAt:       time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 10, 24, 0, 0, 1, 0, time.UTC),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return driver.Record{ID: payload.ID, Data: data, Fields: map[string]any{"revision": int64(1)}}
}

func TestRecordMapperDecodeValidatesDurationBounds(t *testing.T) {
	maxSeconds := int64(math.MaxInt64) / int64(time.Second)
	tests := []struct {
		name    string
		seconds int64
		wantErr string
	}{
		{name: "valid minimum range", seconds: maxSeconds},
		{name: "valid ordinary value", seconds: 60},
		{name: "zero", seconds: 0, wantErr: "job duration"},
		{name: "negative", seconds: -1, wantErr: "job duration"},
		{name: "wraps to a valid duration", seconds: 36028797018964028, wantErr: "outside the supported range"},
		{name: "positive boundary", seconds: maxSeconds + 1, wantErr: "outside the supported range"},
		{name: "max int64", seconds: math.MaxInt64, wantErr: "outside the supported range"},
		{name: "negative boundary", seconds: -maxSeconds - 1, wantErr: "outside the supported range"},
		{name: "min int64", seconds: math.MinInt64, wantErr: "outside the supported range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (recordMapper{}).Decode(storedRecordWithDuration(t, tt.seconds))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Decode() error = %v", err)
				}
				want := time.Duration(tt.seconds) * time.Second
				if got == nil || got.Duration != want {
					t.Fatalf("Decode() duration = %v, want %v", got.Duration, want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Decode() error = %v, want %q", err, tt.wantErr)
			}
			if got != nil {
				t.Fatalf("Decode() job = %+v, want nil", got)
			}
		})
	}
}

func TestRecordMapperRejectsUnsupportedSchemaVersion(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{
			name:    "missing version",
			data:    "{\"id\":\"job-1\"}",
			wantErr: "unsupported schema version 0",
		},
		{
			name:    "different version",
			data:    "{\"schema_version\":2,\"id\":\"job-1\"}",
			wantErr: "unsupported schema version 2",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := (recordMapper{}).Decode(driver.Record{
				ID:   "job-1",
				Data: []byte(test.data),
			})
			if err == nil || err.Error() != test.wantErr {
				t.Fatalf("Decode() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
