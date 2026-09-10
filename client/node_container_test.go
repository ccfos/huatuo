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
	"net/http/httptest"
	"strings"
	"testing"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
)

const nodeTestContainerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestFetchNodeContainer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/containers/"+nodeTestContainerID {
			t.Errorf("request path = %q", request.URL.Path)
		}
		if authorization := request.Header.Get("Authorization"); authorization != "" {
			t.Errorf("Authorization header = %q, want empty", authorization)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(
			w,
			`{"data":{"id":%q,"cgroup_css":{"cpu":"0xffff888012345678"}}}`,
			nodeTestContainerID,
		)
	}))
	defer server.Close()

	metadata, err := FetchNodeContainer(
		t.Context(),
		strings.TrimPrefix(server.URL, "http://"),
		nodeTestContainerID,
	)
	if err != nil {
		t.Fatalf("FetchNodeContainer() error = %v", err)
	}
	if metadata.ID != nodeTestContainerID {
		t.Errorf("FetchNodeContainer() ID = %q, want %q", metadata.ID, nodeTestContainerID)
	}
	if got := metadata.CgroupCSS["cpu"]; got != "0xffff888012345678" {
		t.Errorf("FetchNodeContainer() CPU CSS = %q", got)
	}
}

func TestFetchNodeContainerReturnsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(
			w,
			`{"error":{"code":"container_not_found","message":"container was not found"}}`,
		)
	}))
	defer server.Close()

	_, err := FetchNodeContainer(
		t.Context(),
		strings.TrimPrefix(server.URL, "http://"),
		nodeTestContainerID,
	)
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("FetchNodeContainer() error = %v, want *NodeError", err)
	}
	if nodeErr.Code != nodeapi.ErrorCodeContainerNotFound ||
		nodeErr.StatusCode != http.StatusNotFound {
		t.Errorf("FetchNodeContainer() error = %+v", nodeErr)
	}
}
