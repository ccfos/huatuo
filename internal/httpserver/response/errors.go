// Copyright 2025, 2026 The HuaTuo Authors
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

	v1 "github.com/ccfos/huatuo/apis/v1"
)

// APIError represents a standardized API error.
type APIError struct {
	Code    v1.ErrorCode
	Message string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("code=%s, message=%s", e.Code, e.Message)
}

// Predefined errors
var (
	// ErrInvalidRequest represents a bad request error.
	ErrInvalidRequest = &APIError{
		Code:    v1.ErrorCodeInvalidRequest,
		Message: "invalid request",
	}

	// ErrRouteNotFound indicates that the request path has no API route.
	ErrRouteNotFound = &APIError{
		Code:    v1.ErrorCodeRouteNotFound,
		Message: "route not found",
	}

	// ErrMethodNotAllowed indicates that the route rejects the HTTP method.
	ErrMethodNotAllowed = &APIError{
		Code:    v1.ErrorCodeMethodNotAllowed,
		Message: "method not allowed",
	}

	// ErrUnauthorized represents an authentication error.
	ErrUnauthorized = &APIError{
		Code:    v1.ErrorCodeUnauthorized,
		Message: "unauthorized",
	}

	// ErrForbidden represents a permission denied error.
	ErrForbidden = &APIError{
		Code:    v1.ErrorCodeForbidden,
		Message: "permission denied",
	}

	// ErrNotFound represents a resource not found error.
	ErrNotFound = &APIError{
		Code:    v1.ErrorCodeNotFound,
		Message: "not found",
	}

	// ErrConflict represents a conflict error (e.g., resource already exists).
	ErrConflict = &APIError{
		Code:    v1.ErrorCodeConflict,
		Message: "conflict",
	}

	// ErrInternal represents an internal server error.
	ErrInternal = &APIError{
		Code:    v1.ErrorCodeInternal,
		Message: "internal error",
	}

	// ErrRequestTooLarge indicates that the request body exceeds the limit.
	ErrRequestTooLarge = &APIError{
		Code:    v1.ErrorCodeRequestTooLarge,
		Message: "request body is too large",
	}

	// ErrUnsupportedMediaType indicates an unsupported request content type.
	ErrUnsupportedMediaType = &APIError{
		Code:    v1.ErrorCodeUnsupportedMediaType,
		Message: "request content type is not supported",
	}

	// ErrTooManyRequests represents a rate limit exceeded error.
	ErrTooManyRequests = &APIError{
		Code:    v1.ErrorCodeRateLimited,
		Message: "too many requests",
	}
)

// NewAPIError creates an APIError whose status is derived from its code.
func NewAPIError(code v1.ErrorCode, message string) *APIError {
	return &APIError{
		Code:    code,
		Message: message,
	}
}

// GetCode returns the application error code.
func (e *APIError) GetCode() v1.ErrorCode { return e.Code }

// GetMessage returns the error message.
func (e *APIError) GetMessage() string { return e.Message }

// WithMessage returns a copy of the error with a custom message.
func (e *APIError) WithMessage(message string) *APIError {
	return &APIError{
		Code:    e.Code,
		Message: message,
	}
}
