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
	"errors"
	"net/http"

	v1 "github.com/ccfos/huatuo/apis/v1"
)

// HTTPStatusMapper resolves an API error code to its request-level status.
type HTTPStatusMapper func(code v1.ErrorCode) (int, bool)

// JSONWriter is the minimal interface required for writing JSON responses.
// *server.Context implements this interface.
type JSONWriter interface {
	JSON(code int, obj any)
}

// Success sends a successful response with HTTP 200 status code.
func Success(w JSONWriter, data any) {
	w.JSON(http.StatusOK, v1.Response[any]{
		Data: data,
	})
}

// Created sends a 201 Created response with a Location header pointing at the new resource.
func Created(w interface {
	JSONWriter
	Header(key, val string)
}, location string, data any,
) {
	w.Header("Location", location)
	w.JSON(http.StatusCreated, v1.Response[any]{
		Data: data,
	})
}

// NoContent sends a 204 No Content response with no body.
func NoContent(w interface{ Status(code int) }) {
	w.Status(http.StatusNoContent)
}

// Error sends an error response.
// If err exposes an API code, the generated directory determines its status.
// Otherwise, it returns HTTP 500 Internal Server Error.
func Error(w JSONWriter, err error, statusForCode HTTPStatusMapper) {
	var apiErr interface {
		GetCode() v1.ErrorCode
		GetMessage() string
	}
	if errors.As(err, &apiErr) {
		status, ok := statusForErrorCode(statusForCode, apiErr.GetCode())
		if !ok {
			writeInternalError(w)
			return
		}
		w.JSON(status, v1.ErrorResponse{
			Error: v1.Error{
				Code:    apiErr.GetCode(),
				Message: apiErr.GetMessage(),
			},
		})
		return
	}

	writeInternalError(w)
}

// ErrorWithCode sends an API error using the status assigned to its code.
func ErrorWithCode(
	w JSONWriter,
	statusForCode HTTPStatusMapper,
	code v1.ErrorCode,
	message string,
) {
	Error(w, NewAPIError(code, message), statusForCode)
}

// LegacyHTTPStatusForErrorCode resolves shared codes and compatibility codes.
func LegacyHTTPStatusForErrorCode(code v1.ErrorCode) (int, bool) {
	if status, ok := v1.HTTPStatusForErrorCode(code); ok {
		return status, true
	}

	switch code {
	case v1.ErrorCodeNotFound:
		return http.StatusNotFound, true
	case v1.ErrorCodeConflict:
		return http.StatusConflict, true
	case v1.ErrorCodeRateLimited:
		return http.StatusTooManyRequests, true
	case v1.ErrorCodeProfilingDisabled:
		return http.StatusServiceUnavailable, true
	default:
		return 0, false
	}
}

// ChainHTTPStatusMappers returns the first successful mapper result.
func ChainHTTPStatusMappers(mappers ...HTTPStatusMapper) HTTPStatusMapper {
	return func(code v1.ErrorCode) (int, bool) {
		for _, mapper := range mappers {
			if mapper == nil {
				continue
			}
			if status, ok := mapper(code); ok {
				return status, true
			}
		}
		return 0, false
	}
}

func statusForErrorCode(mapper HTTPStatusMapper, code v1.ErrorCode) (int, bool) {
	if mapper == nil {
		return 0, false
	}
	return mapper(code)
}

func writeInternalError(w JSONWriter) {
	w.JSON(http.StatusInternalServerError, v1.ErrorResponse{
		Error: v1.Error{
			Code:    ErrInternal.Code,
			Message: ErrInternal.Message,
		},
	})
}
