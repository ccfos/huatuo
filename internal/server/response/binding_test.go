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
	"fmt"
	"net/http"
	"strings"
	"testing"

	v1 "huatuo-bamai/apis/v1"

	"github.com/go-playground/validator/v10"
)

func TestBindingErrorClassifiesBodyLimit(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "direct",
			err:  &http.MaxBytesError{Limit: 4096},
		},
		{
			name: "wrapped",
			err:  fmt.Errorf("decode request: %w", &http.MaxBytesError{Limit: 4096}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := BindingErrorWithMessage(test.err, "invalid protobuf request")
			if got.HTTPStatus != http.StatusRequestEntityTooLarge {
				t.Fatalf("HTTPStatus=%d, want %d", got.HTTPStatus,
					http.StatusRequestEntityTooLarge)
			}
			if got.Code != v1.ErrorCodeInvalidRequest {
				t.Fatalf("Code=%q, want %q", got.Code, v1.ErrorCodeInvalidRequest)
			}
			if got.Message != "request body exceeds 4096 bytes" {
				t.Fatalf("Message=%q, want body limit", got.Message)
			}
		})
	}
}

func TestBindingErrorPreservesNonLimitBehavior(t *testing.T) {
	t.Run("fallback", func(t *testing.T) {
		got := BindingErrorWithMessage(errors.New("decode failed"), "invalid protobuf request")
		if got.HTTPStatus != http.StatusBadRequest || got.Message != "invalid protobuf request" {
			t.Fatalf("BindingErrorWithMessage()=%+v, want fallback HTTP 400", got)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		got := BindingError(errors.New("unexpected EOF"))
		if got.HTTPStatus != http.StatusBadRequest || got.Message != "unexpected EOF" {
			t.Fatalf("BindingError()=%+v, want original decode error", got)
		}
	})

	t.Run("nil", func(t *testing.T) {
		got := BindingError(nil)
		if got != ErrInvalidRequest {
			t.Fatalf("BindingError(nil)=%+v, want ErrInvalidRequest", got)
		}
	})
}

func TestBindingErrorUsesFirstValidationNamespace(t *testing.T) {
	type request struct {
		Name string `validate:"required"`
	}

	err := validator.New().Struct(request{})
	got := BindingError(err)
	if got.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("HTTPStatus=%d, want %d", got.HTTPStatus, http.StatusBadRequest)
	}
	if !strings.HasSuffix(got.Message, ".Name") {
		t.Fatalf("Message=%q, want first validation namespace", got.Message)
	}
}
