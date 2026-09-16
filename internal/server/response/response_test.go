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
	"errors"
	"net/http"
	"reflect"
	"testing"

	v1 "github.com/ccfos/huatuo/apis/v1"
	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
	serverapi "github.com/ccfos/huatuo/apis/v1/server"
)

type testResponseWriter struct {
	statusCode int
	body       any
	headers    map[string]string
}

func (w *testResponseWriter) JSON(code int, obj any) {
	w.statusCode = code
	w.body = obj
}

func (w *testResponseWriter) Status(code int) {
	w.statusCode = code
}

func (w *testResponseWriter) Header(key, val string) {
	if w.headers == nil {
		w.headers = make(map[string]string)
	}
	w.headers[key] = val
}

func TestSuccess(t *testing.T) {
	w := &testResponseWriter{}
	data := map[string]string{"key": "value"}

	Success(w, data)

	if w.statusCode != http.StatusOK {
		t.Errorf("statusCode = %d, want %d", w.statusCode, http.StatusOK)
	}
	resp, ok := w.body.(v1.Response[any])
	if !ok {
		t.Fatalf("body type = %T, want v1.Response[any]", w.body)
	}
	if !reflect.DeepEqual(resp.Data, data) {
		t.Errorf("resp.Data = %v, want %v", resp.Data, data)
	}
}

func TestCreated(t *testing.T) {
	w := &testResponseWriter{}
	location := "/tasks/task-123"
	data := map[string]string{"id": "task-123"}

	Created(w, location, data)

	if w.statusCode != http.StatusCreated {
		t.Errorf("statusCode = %d, want %d", w.statusCode, http.StatusCreated)
	}
	if w.headers["Location"] != location {
		t.Errorf("Location header = %q, want %q", w.headers["Location"], location)
	}
	resp, ok := w.body.(v1.Response[any])
	if !ok {
		t.Fatalf("body type = %T, want v1.Response[any]", w.body)
	}
	if !reflect.DeepEqual(resp.Data, data) {
		t.Errorf("resp.Data = %v, want %v", resp.Data, data)
	}
}

func TestNoContent(t *testing.T) {
	w := &testResponseWriter{}

	NoContent(w)

	if w.statusCode != http.StatusNoContent {
		t.Errorf("statusCode = %d, want %d", w.statusCode, http.StatusNoContent)
	}
}

func TestErrorWithAPIError(t *testing.T) {
	w := &testResponseWriter{}
	apiErr := ErrNotFound

	Error(w, apiErr, LegacyHTTPStatusForErrorCode)

	if w.statusCode != http.StatusNotFound {
		t.Errorf("statusCode = %d, want %d", w.statusCode, http.StatusNotFound)
	}
	resp, ok := w.body.(v1.ErrorResponse)
	if !ok {
		t.Fatalf("body type = %T, want v1.ErrorResponse", w.body)
	}
	if resp.Error.Code != v1.ErrorCodeNotFound {
		t.Errorf("error code = %q, want %q", resp.Error.Code, v1.ErrorCodeNotFound)
	}
	if resp.Error.Message != "not found" {
		t.Errorf("error message = %q, want %q", resp.Error.Message, "not found")
	}
}

func TestErrorWithPlainError(t *testing.T) {
	w := &testResponseWriter{}
	plainErr := errors.New("something went wrong")

	Error(w, plainErr, LegacyHTTPStatusForErrorCode)

	if w.statusCode != http.StatusInternalServerError {
		t.Errorf("statusCode = %d, want %d", w.statusCode, http.StatusInternalServerError)
	}
	resp, ok := w.body.(v1.ErrorResponse)
	if !ok {
		t.Fatalf("body type = %T, want v1.ErrorResponse", w.body)
	}
	if resp.Error.Code != v1.ErrorCodeInternal {
		t.Errorf("error code = %q, want %q", resp.Error.Code, v1.ErrorCodeInternal)
	}
	if resp.Error.Message != "internal error" {
		t.Errorf("error message = %q, want %q", resp.Error.Message, "internal error")
	}
}

func TestErrorWithCode(t *testing.T) {
	w := &testResponseWriter{}

	ErrorWithCode(
		w,
		LegacyHTTPStatusForErrorCode,
		v1.ErrorCodeInvalidRequest,
		"missing required field",
	)

	if w.statusCode != http.StatusBadRequest {
		t.Errorf("statusCode = %d, want %d", w.statusCode, http.StatusBadRequest)
	}
	resp, ok := w.body.(v1.ErrorResponse)
	if !ok {
		t.Fatalf("body type = %T, want v1.ErrorResponse", w.body)
	}
	if resp.Error.Code != v1.ErrorCodeInvalidRequest {
		t.Errorf("error code = %q, want %q", resp.Error.Code, v1.ErrorCodeInvalidRequest)
	}
	if resp.Error.Message != "missing required field" {
		t.Errorf("error message = %q, want %q", resp.Error.Message, "missing required field")
	}
}

func TestErrorWithUnknownCodeFallsBackToInternal(t *testing.T) {
	w := &testResponseWriter{}

	Error(w, NewAPIError("unknown_code", "sensitive details"), LegacyHTTPStatusForErrorCode)

	if w.statusCode != http.StatusInternalServerError {
		t.Errorf("statusCode = %d, want %d", w.statusCode, http.StatusInternalServerError)
	}
	resp, ok := w.body.(v1.ErrorResponse)
	if !ok {
		t.Fatalf("body type = %T, want v1.ErrorResponse", w.body)
	}
	if resp.Error.Code != v1.ErrorCodeInternal {
		t.Errorf("error code = %q, want %q", resp.Error.Code, v1.ErrorCodeInternal)
	}
	if resp.Error.Message != ErrInternal.Message {
		t.Errorf("error message = %q, want %q", resp.Error.Message, ErrInternal.Message)
	}
}

func TestInjectedHTTPStatusMapper(t *testing.T) {
	tests := []struct {
		name       string
		mapper     HTTPStatusMapper
		code       v1.ErrorCode
		wantStatus int
		wantOK     bool
	}{
		{
			name:       "shared",
			mapper:     LegacyHTTPStatusForErrorCode,
			code:       v1.ErrorCodeUnauthenticated,
			wantStatus: http.StatusUnauthorized,
			wantOK:     true,
		},
		{
			name:       "shared route",
			mapper:     serverapi.HTTPStatusForErrorCode,
			code:       v1.ErrorCodeRouteNotFound,
			wantStatus: http.StatusNotFound,
			wantOK:     true,
		},
		{
			name:       "server",
			mapper:     serverapi.HTTPStatusForErrorCode,
			code:       serverapi.ErrorCodeJobNotFound,
			wantStatus: http.StatusNotFound,
			wantOK:     true,
		},
		{
			name:       "node",
			mapper:     nodeapi.HTTPStatusForErrorCode,
			code:       nodeapi.ErrorCodeServiceNotImplemented,
			wantStatus: http.StatusNotImplemented,
			wantOK:     true,
		},
		{
			name:       "legacy",
			mapper:     LegacyHTTPStatusForErrorCode,
			code:       v1.ErrorCodeRateLimited,
			wantStatus: http.StatusTooManyRequests,
			wantOK:     true,
		},
		{name: "unknown", mapper: serverapi.HTTPStatusForErrorCode, code: "unknown_code"},
		{name: "nil mapper", code: v1.ErrorCodeInvalidRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStatus, gotOK := statusForErrorCode(tt.mapper, tt.code)
			if gotStatus != tt.wantStatus {
				t.Errorf("HTTPStatusForErrorCode(%q) = %d, want %d", tt.code, gotStatus, tt.wantStatus)
			}
			if gotOK != tt.wantOK {
				t.Errorf("HTTPStatusForErrorCode(%q) ok = %v, want %v", tt.code, gotOK, tt.wantOK)
			}
		})
	}
}

func TestChainHTTPStatusMappers(t *testing.T) {
	mapper := ChainHTTPStatusMappers(
		serverapi.HTTPStatusForErrorCode,
		LegacyHTTPStatusForErrorCode,
	)

	status, ok := mapper(serverapi.ErrorCodeJobNotFound)
	if !ok || status != http.StatusNotFound {
		t.Errorf("server status = %d/%v, want %d/true", status, ok, http.StatusNotFound)
	}
	status, ok = mapper(v1.ErrorCodeRateLimited)
	if !ok || status != http.StatusTooManyRequests {
		t.Errorf("legacy status = %d/%v, want %d/true", status, ok, http.StatusTooManyRequests)
	}
	status, ok = mapper("unknown_code")
	if ok || status != 0 {
		t.Errorf("unknown status = %d/%v, want 0/false", status, ok)
	}
}
