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

package handlers

import (
	"context"
	"errors"

	apiv1 "github.com/ccfos/huatuo/apis/v1"
	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/server/response"
)

// UpdateConfig applies one validated configuration batch and persists it.
func (h *NodeAPIHandler) UpdateConfig(
	_ context.Context,
	request nodeapi.UpdateConfigRequestObject,
) (nodeapi.UpdateConfigResponseObject, error) {
	values := make(map[string]any, len(request.Body.Config))
	for key, value := range request.Body.Config {
		values[key] = value
	}
	if err := h.updateConfig(values); err != nil {
		if errors.Is(err, config.ErrInvalidUpdate) {
			return nil, response.NewAPIError(apiv1.ErrorCodeInvalidRequest, err.Error())
		}
		log.WithError(err).Error("failed to persist config")
		return nil, response.ErrInternal.WithMessage("failed to persist config")
	}

	return nodeapi.UpdateConfig204Response{}, nil
}
