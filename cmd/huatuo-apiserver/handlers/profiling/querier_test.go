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

package profiling

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	profileService "huatuo-bamai/internal/profiler/service"
)

func TestProfileQueryHTTPError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "invalid query",
			err:         errors.Join(profileService.ErrInvalidQuery, errors.New("bad selector")),
			wantStatus:  http.StatusBadRequest,
			wantMessage: "bad selector",
		},
		{
			name:        "not found",
			err:         profileService.ErrProfilesAbsent,
			wantStatus:  http.StatusNotFound,
			wantMessage: "profiles not found",
		},
		{
			name: "selection too large",
			err: errors.Join(
				profileService.ErrProfileQueryLimitExceeded,
				errors.New("10001 documents"),
			),
			wantStatus:  http.StatusUnprocessableEntity,
			wantMessage: "10001 documents",
		},
		{
			name:        "internal",
			err:         errors.New("elasticsearch unavailable"),
			wantStatus:  http.StatusInternalServerError,
			wantMessage: "internal error",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, message := profileQueryHTTPError(test.err)
			if status != test.wantStatus {
				t.Fatalf("status = %d, want %d", status, test.wantStatus)
			}
			if !strings.Contains(message, test.wantMessage) {
				t.Fatalf("message = %q, want it to contain %q", message, test.wantMessage)
			}
		})
	}
}
