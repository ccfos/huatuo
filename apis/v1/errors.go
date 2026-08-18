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

package v1

// ErrorCode identifies an API error independently of its human-readable message.
type ErrorCode string

const (
	ErrorCodeInvalidRequest    ErrorCode = "invalid_request"
	ErrorCodeUnauthorized      ErrorCode = "unauthorized"
	ErrorCodeForbidden         ErrorCode = "forbidden"
	ErrorCodeNotFound          ErrorCode = "not_found"
	ErrorCodeConflict          ErrorCode = "conflict"
	ErrorCodeRateLimited       ErrorCode = "rate_limited"
	ErrorCodeInternal          ErrorCode = "internal_error"
	ErrorCodeProfilingDisabled ErrorCode = "profiling_disabled"
)

// Error describes an API error returned over the wire.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// ErrorResponse is the error response envelope. It remains separate from Error
// so request-level metadata can be added without changing the error payload.
type ErrorResponse struct {
	Error Error `json:"error"`
}
