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
	serverresponse "github.com/ccfos/huatuo/internal/httpserver/response"
	"github.com/ccfos/huatuo/internal/pod"
)

const testContainerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGetContainer(t *testing.T) {
	handler := &NodeAPIHandler{
		containerCSSBySubsys: func(id, subsys string) (pod.CgroupCSSRef, error) {
			if id != testContainerID || subsys != "cpu" {
				t.Fatalf("CSS lookup = (%q, %q), want (%q, cpu)", id, subsys, testContainerID)
			}
			return pod.CgroupCSSRef{CSSAddr: 0xffff888012345678, CgroupID: 42}, nil
		},
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
	if response.Data.CgroupCSSRefs == nil ||
		(*response.Data.CgroupCSSRefs)["cpu"].CgroupID != "0x000000000000002a" {
		t.Errorf("GetContainer() CPU CSS instance = %v, want cgroup ID 42", response.Data.CgroupCSSRefs)
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

func TestGetContainerOmitsUnverifiedCSSRef(t *testing.T) {
	for _, test := range []struct {
		name string
		ref  pod.CgroupCSSRef
		err  error
	}{
		{name: "address mismatch", ref: pod.CgroupCSSRef{CSSAddr: 0x200, CgroupID: 7}},
		{name: "zero cgroup ID", ref: pod.CgroupCSSRef{CSSAddr: 0x100}},
		{name: "lookup failure", err: errors.New("cache unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := &NodeAPIHandler{
				containerByID: func(string) (*pod.Container, error) {
					return &pod.Container{
						ID:        testContainerID,
						CgroupCss: map[string]uint64{"cpu": 0x100},
					}, nil
				},
				containerCSSBySubsys: func(string, string) (pod.CgroupCSSRef, error) {
					return test.ref, test.err
				},
			}
			result, err := handler.GetContainer(t.Context(), nodeapi.GetContainerRequestObject{
				ContainerID: testContainerID,
			})
			if err != nil {
				t.Fatal(err)
			}
			response := result.(nodeapi.GetContainer200JSONResponse)
			if response.Data.CgroupCSSRefs != nil {
				t.Fatalf("unverified CSS ref published: %+v", response.Data.CgroupCSSRefs)
			}
			if response.Data.CgroupCSS["cpu"] != "0x0000000000000100" {
				t.Fatalf("legacy CSS address missing: %+v", response.Data.CgroupCSS)
			}
		})
	}
}
