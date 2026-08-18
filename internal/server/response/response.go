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

	v1 "huatuo-bamai/apis/v1"
)

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
// If err is an APIError, it uses the error's HTTP status code.
// Otherwise, it returns HTTP 500 Internal Server Error.
func Error(w JSONWriter, err error) {
	var apiErr interface {
		GetHTTPStatus() int
		GetCode() v1.ErrorCode
		GetMessage() string
	}
	if errors.As(err, &apiErr) {
		w.JSON(apiErr.GetHTTPStatus(), v1.ErrorResponse{
			Error: v1.Error{
				Code:    apiErr.GetCode(),
				Message: apiErr.GetMessage(),
			},
		})
		return
	}

	w.JSON(http.StatusInternalServerError, v1.ErrorResponse{
		Error: v1.Error{
			Code:    ErrInternal.Code,
			Message: ErrInternal.Message,
		},
	})
}

// ErrorWithCode sends an error response with a custom HTTP status code and error code.
func ErrorWithCode(w JSONWriter, status int, code v1.ErrorCode, message string) {
	w.JSON(status, v1.ErrorResponse{
		Error: v1.Error{
			Code:    code,
			Message: message,
		},
	})
}
