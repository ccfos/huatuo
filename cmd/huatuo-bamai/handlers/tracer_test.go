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

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/tracing"
)

func TestTracerAPIError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		httpStatus int
	}{
		{
			name:       "not found",
			err:        fmt.Errorf("start: %w", tracing.ErrTracerNotFound),
			httpStatus: http.StatusNotFound,
		},
		{
			name:       "already running",
			err:        tracing.ErrTracerAlreadyRunning,
			httpStatus: http.StatusConflict,
		},
		{
			name:       "not running",
			err:        tracing.ErrTracerNotRunning,
			httpStatus: http.StatusConflict,
		},
		{
			name:       "manager closed",
			err:        tracing.ErrManagerClosed,
			httpStatus: http.StatusConflict,
		},
		{
			name:       "deadline exceeded",
			err:        context.DeadlineExceeded,
			httpStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tracerAPIError(tt.err)
			var apiErr *response.APIError
			if !errors.As(got, &apiErr) {
				t.Fatalf("tracerAPIError() type = %T, want *response.APIError", got)
			}
			if apiErr.HTTPStatus != tt.httpStatus {
				t.Errorf(
					"tracerAPIError().HTTPStatus = %d, want %d",
					apiErr.HTTPStatus,
					tt.httpStatus,
				)
			}
		})
	}
}
