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

package profiler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchProfilingMetadataContainerEscapesContainerID(t *testing.T) {
	var requestURI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"container-2026","hostname":"huatuo-dev","type":"normal","qos":"besteffort","labels":{"app":"demo"}}]}`)
	}))
	defer server.Close()

	serverAddr := strings.TrimPrefix(server.URL, "http://")

	container, err := fetchProfilingMetadataContainer(serverAddr, "container+2026&debug")
	if err != nil {
		t.Fatalf("fetchProfilingMetadataContainer() error = %v", err)
	}
	if container == nil || container.ID != "container-2026" {
		t.Fatalf("container = %+v, want container-2026", container)
	}
	if !strings.Contains(requestURI, "container_id=container%2B2026%26debug") {
		t.Fatalf("requestURI = %q, want escaped container_id", requestURI)
	}
}
