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
	"errors"
	"net/http"
)

type JSONWriter interface {
	JSON(code int, obj any)
}

type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func Success(w JSONWriter, data any) {
	w.JSON(http.StatusOK, Response{
		Code:    0,
		Message: "success",
		Data:    data,
	})
}

func Error(w JSONWriter, err error) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		w.JSON(apiErr.HTTPStatus, Response{
			Code:    apiErr.Code,
			Message: apiErr.Message,
			Data:    nil,
		})
		return
	}

	w.JSON(http.StatusInternalServerError, Response{
		Code:    ErrInternal.Code,
		Message: err.Error(),
		Data:    nil,
	})
}

func ErrorWithCode(w JSONWriter, status, code int, message string) {
	w.JSON(status, Response{
		Code:    code,
		Message: message,
		Data:    nil,
	})
}
