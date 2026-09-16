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
	"testing"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
	"github.com/ccfos/huatuo/internal/pod"
	serverresponse "github.com/ccfos/huatuo/internal/server/response"
)

const testContainerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGetContainer(t *testing.T) {
	handler := &NodeAPIHandler{
		containerByID: func(id string) (*pod.Container, error) {
			if id != testContainerID {
				t.Fatalf("container ID = %q, want %q", id, testContainerID)
			}
			return &pod.Container{
				ID: testContainerID,
				CgroupCss: map[string]uint64{
					"cpu":    0xffff888012345678,
					"memory": 0,
				},
			}, nil
		},
	}

	responseObject, err := handler.GetContainer(t.Context(), nodeapi.GetContainerRequestObject{
		ContainerID: testContainerID,
	})
	if err != nil {
		t.Fatalf("GetContainer() error = %v", err)
	}
	response, ok := responseObject.(nodeapi.GetContainer200JSONResponse)
	if !ok {
		t.Fatalf("GetContainer() response type = %T", responseObject)
	}
	if response.Data.ID != testContainerID {
		t.Errorf("GetContainer() ID = %q, want %q", response.Data.ID, testContainerID)
	}
	if got := response.Data.CgroupCSS["cpu"]; got != "0xffff888012345678" {
		t.Errorf("GetContainer() cpu CSS = %q, want %q", got, "0xffff888012345678")
	}
	if _, ok := response.Data.CgroupCSS["memory"]; ok {
		t.Error("GetContainer() returned a zero memory CSS address")
	}
}

func TestGetContainerNotFound(t *testing.T) {
	handler := &NodeAPIHandler{
		containerByID: func(string) (*pod.Container, error) { return nil, nil },
	}

	_, err := handler.GetContainer(t.Context(), nodeapi.GetContainerRequestObject{
		ContainerID: testContainerID,
	})
	var apiErr *serverresponse.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("GetContainer() error = %v, want *response.APIError", err)
	}
	if apiErr.Code != nodeapi.ErrorCodeContainerNotFound {
		t.Errorf(
			"GetContainer() error code = %q, want %q",
			apiErr.Code,
			nodeapi.ErrorCodeContainerNotFound,
		)
	}
}
