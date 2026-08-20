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

package response

import (
	"net/http"
	"testing"
)

func TestPredefinedAPIErrorsExposeTheirContract(t *testing.T) {
	tests := []struct {
		name string
		err  *APIError
		code int
		status int
		message string
	}{
		{name: "invalid request", err: ErrInvalidRequest, code: 400, status: http.StatusBadRequest, message: "invalid request"},
		{name: "unauthorized", err: ErrUnauthorized, code: 401, status: http.StatusUnauthorized, message: "unauthorized"},
		{name: "forbidden", err: ErrForbidden, code: 403, status: http.StatusForbidden, message: "permission denied"},
		{name: "not found", err: ErrNotFound, code: 404, status: http.StatusNotFound, message: "not found"},
		{name: "conflict", err: ErrConflict, code: 409, status: http.StatusConflict, message: "conflict"},
		{name: "internal", err: ErrInternal, code: 500, status: http.StatusInternalServerError, message: "internal error"},
		{name: "rate limit", err: ErrTooManyRequests, code: 429, status: http.StatusTooManyRequests, message: "too many requests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.GetCode() != tt.code || tt.err.GetHTTPStatus() != tt.status || tt.err.GetMessage() != tt.message {
				t.Errorf("APIError = %#v, want code=%d status=%d message=%q", tt.err, tt.code, tt.status, tt.message)
			}
		})
	}
}

func TestAPIErrorWithMessageCopiesOriginal(t *testing.T) {
	original := NewAPIError(499, "original", http.StatusConflict)
	got := original.WithMessage("updated")
	if got == original {
		t.Fatal("WithMessage() returned the original pointer")
	}
	if got.Code != original.Code || got.HTTPStatus != original.HTTPStatus || got.Message != "updated" {
		t.Errorf("WithMessage() = %#v", got)
	}
	if original.Message != "original" {
		t.Errorf("original message = %q, want unchanged", original.Message)
	}
}
