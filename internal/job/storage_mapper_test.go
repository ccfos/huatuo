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
	"testing"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

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
