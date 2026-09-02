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

	v1 "huatuo-bamai/apis/v1"

	"github.com/go-playground/validator/v10"
)

// BindingError maps request decoding and validation failures to the API error
// contract while retaining body-limit failures as HTTP 413 responses.
func BindingError(err error) *APIError {
	return BindingErrorWithMessage(err, "")
}

// BindingErrorWithMessage uses fallback for ordinary binding failures while
// still giving body-limit failures their protocol-specific status and limit.
func BindingErrorWithMessage(err error, fallback string) *APIError {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return NewAPIError(
			v1.ErrorCodeInvalidRequest,
			fmt.Sprintf("request body exceeds %d bytes", maxBytesError.Limit),
			http.StatusRequestEntityTooLarge,
		)
	}

	if fallback != "" {
		return ErrInvalidRequest.WithMessage(fallback)
	}

	var validationErrors validator.ValidationErrors
	if errors.As(err, &validationErrors) && len(validationErrors) > 0 {
		return ErrInvalidRequest.WithMessage(validationErrors[0].Namespace())
	}

	if err == nil {
		return ErrInvalidRequest
	}
	return ErrInvalidRequest.WithMessage(err.Error())
}
