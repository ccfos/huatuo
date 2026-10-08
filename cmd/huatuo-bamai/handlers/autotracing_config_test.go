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
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
)

func TestAutoTracingConfigHTTPRejectsInvalidCandidate(t *testing.T) {
	tests := []struct{ key, value, message string }{
		{"AutoTracing.CPUIdle.Interval", "0", "cpu idle"},
		{"AutoTracing.CPUSys.Interval", "0", "cpu system"},
		{"AutoTracing.Dload.Interval", "0", "dload"},
		{"AutoTracing.MemoryBurst.Interval", "0", "memory burst"},
		{"AutoTracing.CPUIdle.Filter", `{"Included":[{"Field":"container_hostname","Pattern":"["}]}`, "cpu idle filter"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			path := writeConfig(t, "")
			if err := config.Load(path); err != nil {
				t.Fatal(err)
			}
			original := config.Get()
			originalFile, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			handler := newTestNodeAPIHandler(t, time.Second)
			handler.updateConfig = config.UpdateAndSync
			baseURL := startNodeAPIServer(t, handler)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPut, baseURL+"/v1/config", strings.NewReader(fmt.Sprintf(`{"config":{%q:%s}}`, tt.key, tt.value)))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer node-secret")
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), tt.message) {
				t.Errorf("status=%d body=%s, want 400 naming %s", response.StatusCode, body, tt.message)
			}
			if config.Get() != original {
				t.Error("rejected update published configuration")
			}
			afterFile, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(originalFile, afterFile) {
				t.Error("rejected update changed persisted file")
			}
		})
	}
}
