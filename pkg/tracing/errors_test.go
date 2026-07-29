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
	"context"
	"errors"
	"testing"
)

func TestTracerErrorFormatting(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		want       string
		wantTarget error
	}{
		{
			name:       "state",
			err:        newTracerStateError(ErrTracerNotFound, "cpu"),
			want:       `"cpu": tracer not found`,
			wantTarget: ErrTracerNotFound,
		},
		{
			name:       "context",
			err:        newTracerContextError("stop", "cpu", context.DeadlineExceeded),
			want:       `stop "cpu": context deadline exceeded`,
			wantTarget: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("error = %q, want %q", got, tt.want)
			}
			if !errors.Is(tt.err, tt.wantTarget) {
				t.Errorf("errors.Is(%v, %v) = false, want true", tt.err, tt.wantTarget)
			}
		})
	}
}
