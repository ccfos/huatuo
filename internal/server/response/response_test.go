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
	"testing"
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
	resp, ok := w.body.(Response)
	if !ok {
		t.Fatalf("body is not Response: %T", w.body)
	}
	if resp.Code != 0 {
		t.Errorf("resp.Code = %d, want 0", resp.Code)
	}
	if resp.Message != "success" {
		t.Errorf("resp.Message = %q, want %q", resp.Message, "success")
	}
	if resp.Data != data {
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
	resp, ok := w.body.(Response)
	if !ok {
		t.Fatalf("body is not Response: %T", w.body)
	}
	if resp.Data != data {
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

	Error(w, apiErr)

	if w.statusCode != http.StatusNotFound {
		t.Errorf("statusCode = %d, want %d", w.statusCode, http.StatusNotFound)
	}
	resp, ok := w.body.(Response)
	if !ok {
		t.Fatalf("body is not Response: %T", w.body)
	}
	if resp.Code != 404 {
		t.Errorf("resp.Code = %d, want 404", resp.Code)
	}
	if resp.Message != "not found" {
		t.Errorf("resp.Message = %q, want %q", resp.Message, "not found")
	}
}

func TestErrorWithPlainError(t *testing.T) {
	w := &testResponseWriter{}
	plainErr := errors.New("something went wrong")

	Error(w, plainErr)

	if w.statusCode != http.StatusInternalServerError {
		t.Errorf("statusCode = %d, want %d", w.statusCode, http.StatusInternalServerError)
	}
	resp, ok := w.body.(Response)
	if !ok {
		t.Fatalf("body is not Response: %T", w.body)
	}
	if resp.Code != 500 {
		t.Errorf("resp.Code = %d, want 500", resp.Code)
	}
	if resp.Message != "something went wrong" {
		t.Errorf("resp.Message = %q, want %q", resp.Message, "something went wrong")
	}
}

func TestErrorWithCode(t *testing.T) {
	w := &testResponseWriter{}

	ErrorWithCode(w, http.StatusBadRequest, 40001, "missing required field")

	if w.statusCode != http.StatusBadRequest {
		t.Errorf("statusCode = %d, want %d", w.statusCode, http.StatusBadRequest)
	}
	resp, ok := w.body.(Response)
	if !ok {
		t.Fatalf("body is not Response: %T", w.body)
	}
	if resp.Code != 40001 {
		t.Errorf("resp.Code = %d, want 40001", resp.Code)
	}
	if resp.Message != "missing required field" {
		t.Errorf("resp.Message = %q, want %q", resp.Message, "missing required field")
	}
}
