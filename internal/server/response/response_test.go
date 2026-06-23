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

type testWriter struct {
	status  int
	headers map[string]string
	body    any
}

func (w *testWriter) JSON(code int, obj any) {
	w.status = code
	w.body = obj
}

func (w *testWriter) Status(code int) {
	w.status = code
}

func (w *testWriter) Header(key, val string) {
	if w.headers == nil {
		w.headers = map[string]string{}
	}
	w.headers[key] = val
}

func TestSuccess(t *testing.T) {
	w := &testWriter{}

	Success(w, map[string]string{"id": "task-1"})

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.status, http.StatusOK)
	}
	resp, ok := w.body.(Response)
	if !ok {
		t.Fatalf("body type = %T, want Response", w.body)
	}
	if resp.Code != 0 || resp.Message != "success" {
		t.Fatalf("response = %+v, want success response", resp)
	}
	if resp.Data == nil {
		t.Fatalf("response data is nil")
	}
}

func TestCreated(t *testing.T) {
	w := &testWriter{}

	Created(w, "/tasks/task-1", "created")

	if w.status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", w.status, http.StatusCreated)
	}
	if w.headers["Location"] != "/tasks/task-1" {
		t.Fatalf("Location header = %q, want %q", w.headers["Location"], "/tasks/task-1")
	}
}

func TestNoContent(t *testing.T) {
	w := &testWriter{}

	NoContent(w)

	if w.status != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.status, http.StatusNoContent)
	}
	if w.body != nil {
		t.Fatalf("body = %#v, want nil", w.body)
	}
}

func TestErrorUsesAPIError(t *testing.T) {
	w := &testWriter{}

	Error(w, ErrNotFound.WithMessage("task not found"))

	if w.status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.status, http.StatusNotFound)
	}
	resp, ok := w.body.(Response)
	if !ok {
		t.Fatalf("body type = %T, want Response", w.body)
	}
	if resp.Code != ErrNotFound.Code || resp.Message != "task not found" {
		t.Fatalf("response = %+v, want not found response", resp)
	}
}

func TestErrorUsesInternalForPlainError(t *testing.T) {
	w := &testWriter{}

	Error(w, errors.New("boom"))

	if w.status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.status, http.StatusInternalServerError)
	}
	resp, ok := w.body.(Response)
	if !ok {
		t.Fatalf("body type = %T, want Response", w.body)
	}
	if resp.Code != ErrInternal.Code || resp.Message != "boom" {
		t.Fatalf("response = %+v, want internal error response", resp)
	}
}
