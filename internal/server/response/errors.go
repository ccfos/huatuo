// Copyright 2025 The HuaTuo Authors
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
	"fmt"
	"net/http"
)

// APIError represents a standardized API error.
type APIError struct {
	Code       int
	Message    string
	HTTPStatus int
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("code=%d, message=%s, http_status=%d", e.Code, e.Message, e.HTTPStatus)
}

// Predefined errors
var (
	// ErrInvalidRequest represents a bad request error.
	ErrInvalidRequest = &APIError{
		Code:       400,
		Message:    "invalid request",
		HTTPStatus: http.StatusBadRequest,
	}

	// ErrUnauthorized represents an authentication error.
	ErrUnauthorized = &APIError{
		Code:       401,
		Message:    "unauthorized",
		HTTPStatus: http.StatusUnauthorized,
	}

	// ErrForbidden represents a permission denied error.
	ErrForbidden = &APIError{
		Code:       403,
		Message:    "permission denied",
		HTTPStatus: http.StatusForbidden,
	}

	// ErrNotFound represents a resource not found error.
	ErrNotFound = &APIError{
		Code:       404,
		Message:    "not found",
		HTTPStatus: http.StatusNotFound,
	}

	// ErrConflict represents a conflict error (e.g., resource already exists).
	ErrConflict = &APIError{
		Code:       409,
		Message:    "conflict",
		HTTPStatus: http.StatusConflict,
	}

	// ErrInternal represents an internal server error.
	ErrInternal = &APIError{
		Code:       500,
		Message:    "internal error",
		HTTPStatus: http.StatusInternalServerError,
	}

	// ErrTooManyRequests represents a rate limit exceeded error.
	ErrTooManyRequests = &APIError{
		Code:       429,
		Message:    "too many requests",
		HTTPStatus: http.StatusTooManyRequests,
	}
)

// NewAPIError creates a new APIError with the given parameters.
func NewAPIError(code int, message string, httpStatus int) *APIError {
	return &APIError{
		Code:       code,
		Message:    message,
		HTTPStatus: httpStatus,
	}
}

// GetHTTPStatus returns the HTTP status code.
func (e *APIError) GetHTTPStatus() int { return e.HTTPStatus }

// GetCode returns the application error code.
func (e *APIError) GetCode() int { return e.Code }

// GetMessage returns the error message.
func (e *APIError) GetMessage() string { return e.Message }

// WithMessage returns a copy of the error with a custom message.
func (e *APIError) WithMessage(message string) *APIError {
	return &APIError{
		Code:       e.Code,
		Message:    message,
		HTTPStatus: e.HTTPStatus,
	}
}
