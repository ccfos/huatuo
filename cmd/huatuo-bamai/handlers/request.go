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

package handlers

import (
	"errors"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"

	"github.com/go-playground/validator/v10"
)

func handleBindError(ctx *server.Context, err error) {
	var validationError *validator.ValidationErrors
	if errors.As(err, &validationError) {
		response.ErrorWithCode(
			ctx,
			ctx.ErrorStatusMapper(),
			v1.ErrorCodeInvalidRequest,
			(*validationError)[0].Namespace(),
		)
		return
	}
	response.ErrorWithCode(
		ctx,
		ctx.ErrorStatusMapper(),
		v1.ErrorCodeInvalidRequest,
		err.Error(),
	)
}
