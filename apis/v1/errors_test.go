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

package v1

import (
	"encoding/json"
	"testing"
)

func TestErrorResponseJSON(t *testing.T) {
	response := ErrorResponse{Error: Error{
		Code:    ErrorCodeProfilingDisabled,
		Message: "profiling is disabled",
	}}

	got, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal error response: %v", err)
	}
	want := `{"error":{"code":"profiling_disabled","message":"profiling is disabled"}}`
	if string(got) != want {
		t.Errorf("error response = %s, want %s", got, want)
	}
}

func TestResponseJSON(t *testing.T) {
	response := Response[map[string]string]{Data: map[string]string{"id": "profile-2026"}}

	got, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal success response: %v", err)
	}
	want := `{"data":{"id":"profile-2026"}}`
	if string(got) != want {
		t.Errorf("success response = %s, want %s", got, want)
	}
}
