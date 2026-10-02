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

	v1 "github.com/ccfos/huatuo/apis/v1"
)

func TestPredefinedAPIErrorsExposeTheirContract(t *testing.T) {
	tests := []struct {
		name    string
		err     *APIError
		code    v1.ErrorCode
		status  int
		message string
	}{
		{name: "invalid request", err: ErrInvalidRequest, code: v1.ErrorCodeInvalidRequest, status: http.StatusBadRequest, message: "invalid request"},
		{name: "route not found", err: ErrRouteNotFound, code: v1.ErrorCodeRouteNotFound, status: http.StatusNotFound, message: "route not found"},
		{name: "method not allowed", err: ErrMethodNotAllowed, code: v1.ErrorCodeMethodNotAllowed, status: http.StatusMethodNotAllowed, message: "method not allowed"},
		{name: "unauthorized", err: ErrUnauthorized, code: v1.ErrorCodeUnauthorized, status: http.StatusUnauthorized, message: "unauthorized"},
		{name: "forbidden", err: ErrForbidden, code: v1.ErrorCodeForbidden, status: http.StatusForbidden, message: "permission denied"},
		{name: "not found", err: ErrNotFound, code: v1.ErrorCodeNotFound, status: http.StatusNotFound, message: "not found"},
		{name: "conflict", err: ErrConflict, code: v1.ErrorCodeConflict, status: http.StatusConflict, message: "conflict"},
		{name: "internal", err: ErrInternal, code: v1.ErrorCodeInternal, status: http.StatusInternalServerError, message: "internal error"},
		{name: "request too large", err: ErrRequestTooLarge, code: v1.ErrorCodeRequestTooLarge, status: http.StatusRequestEntityTooLarge, message: "request body is too large"},
		{name: "unsupported media type", err: ErrUnsupportedMediaType, code: v1.ErrorCodeUnsupportedMediaType, status: http.StatusUnsupportedMediaType, message: "request content type is not supported"},
		{name: "rate limit", err: ErrTooManyRequests, code: v1.ErrorCodeRateLimited, status: http.StatusTooManyRequests, message: "too many requests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.GetCode() != tt.code || tt.err.GetMessage() != tt.message {
				t.Errorf("APIError = %#v, want code=%q message=%q", tt.err, tt.code, tt.message)
			}
			if status, ok := LegacyHTTPStatusForErrorCode(tt.err.GetCode()); !ok || status != tt.status {
				t.Errorf("LegacyHTTPStatusForErrorCode(%q) = (%d, %t), want (%d, true)", tt.code, status, ok, tt.status)
			}
		})
	}
}

func TestAPIErrorWithMessageCopiesOriginal(t *testing.T) {
	original := NewAPIError(v1.ErrorCodeConflict, "original")
	got := original.WithMessage("updated")
	if got == original {
		t.Fatal("WithMessage() returned the original pointer")
	}
	if got.Code != original.Code || got.Message != "updated" {
		t.Errorf("WithMessage() = %#v", got)
	}
	if original.Message != "original" {
		t.Errorf("original message = %q, want unchanged", original.Message)
	}
}
