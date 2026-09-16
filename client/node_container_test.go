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

package client

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
)

const nodeTestContainerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestFetchContainer(t *testing.T) {
	var observedName string
	client, err := NewNode(&NodeConfig{
		Observe: func(name string, _ time.Duration, err error) {
			if err != nil {
				t.Errorf("observer error = %v", err)
			}
			observedName = name
		},
		HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodGet ||
				request.URL.String() != "http://node-1:21970/v1/containers/"+nodeTestContainerID {
				t.Fatalf("request = %s %s", request.Method, request.URL)
			}
			if authorization := request.Header.Get("Authorization"); authorization != "" {
				t.Errorf("Authorization header = %q, want empty", authorization)
			}
			return nodeJSONResponse(
				http.StatusOK,
				fmt.Sprintf(
					`{"data":{"id":%q,"cgroup_css":{"cpu":"0xffff888012345678"}}}`,
					nodeTestContainerID,
				),
			), nil
		})},
	})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	metadata, err := client.FetchContainer(
		t.Context(),
		NodeAddress{HostPort: "node-1:21970"},
		nodeTestContainerID,
	)
	if err != nil {
		t.Fatalf("FetchContainer() error = %v", err)
	}
	if metadata.ID != nodeTestContainerID {
		t.Errorf("FetchContainer() ID = %q, want %q", metadata.ID, nodeTestContainerID)
	}
	if got := metadata.CgroupCSS["cpu"]; got != "0xffff888012345678" {
		t.Errorf("FetchContainer() CPU CSS = %q", got)
	}
	if observedName != "container.fetch" {
		t.Errorf("observed operation = %q", observedName)
	}
}

func TestFetchContainerReturnsAPIError(t *testing.T) {
	client, err := NewNode(&NodeConfig{
		HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nodeJSONResponse(
				http.StatusNotFound,
				`{"error":{"code":"container_not_found","message":"container was not found"}}`,
			), nil
		})},
	})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	_, err = client.FetchContainer(
		t.Context(),
		NodeAddress{HostPort: "node-1:19704"},
		nodeTestContainerID,
	)
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("FetchContainer() error = %v, want *NodeError", err)
	}
	if nodeErr.Code != nodeapi.ErrorCodeContainerNotFound ||
		nodeErr.StatusCode != http.StatusNotFound {
		t.Errorf("FetchContainer() error = %+v", nodeErr)
	}
}
