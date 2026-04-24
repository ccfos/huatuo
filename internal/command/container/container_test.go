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

package container

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGetContainersCompatibility 覆盖容器查询调用方的兼容性，验证了带 container_id 查询参数时请求路径不变、统一响应包装能够正确解码，以及 GetContainerByID 和 GetAllContainers 都能继续工作。
func TestGetContainersCompatibility(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"code":0,"message":"success","data":[{"id":"container-20250226","hostname":"huatuo-dev"},{"id":"container-20250301","hostname":"huatuo-dev"}]}`)
	}))
	defer server.Close()

	serverAddr := strings.TrimPrefix(server.URL, "http://")

	containers, err := getContainers(serverAddr, "container-20250226")
	if err != nil {
		t.Errorf("getContainers() error = %v", err)
	}
	if !strings.Contains(requestedPath, "/containers/json") {
		t.Errorf("requested path = %q, want /containers/json", requestedPath)
	}
	if !strings.Contains(requestedPath, "container_id=container-20250226") {
		t.Errorf("requested path = %q, want container_id query", requestedPath)
	}
	if len(containers) != 2 {
		t.Errorf("len(containers) = %d, want %d", len(containers), 2)
	} else {
		if containers[0].ID != "container-20250226" {
			t.Errorf("first container ID = %q, want %q", containers[0].ID, "container-20250226")
		}
		if containers[0].Hostname != "huatuo-dev" {
			t.Errorf("first container Hostname = %q, want %q", containers[0].Hostname, "huatuo-dev")
		}
	}

	container, err := GetContainerByID(serverAddr, "container-20250226")
	if err != nil {
		t.Errorf("GetContainerByID() error = %v", err)
	} else if container.ID != "container-20250226" {
		t.Errorf("GetContainerByID() ID = %q, want %q", container.ID, "container-20250226")
	}

	allContainers, err := GetAllContainers(serverAddr)
	if err != nil {
		t.Errorf("GetAllContainers() error = %v", err)
	}
	if len(allContainers) != 2 {
		t.Errorf("len(allContainers) = %d, want %d", len(allContainers), 2)
	}
}
