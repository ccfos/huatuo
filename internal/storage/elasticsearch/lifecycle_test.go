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

package elasticsearch

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"huatuo-bamai/internal/storage/driver"
)

type lifecycleTransport func(*http.Request) (*http.Response, error)

func (f lifecycleTransport) Perform(req *http.Request) (*http.Response, error) { return f(req) }

func TestInitConfiguresIndexLifecycle(t *testing.T) {
	type call struct {
		method, path string
		body         map[string]any
	}
	var calls []call
	transport := lifecycleTransport(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if req.Body != nil {
			_ = json.NewDecoder(req.Body).Decode(&body)
		}
		calls = append(calls, call{req.Method, req.URL.Path, body})
		status := http.StatusOK
		if req.Method == http.MethodHead {
			status = http.StatusNotFound
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	backend := &Storage{transport: transport, index: "huatuo_bamai", ilmRetentionDays: 14}

	if err := backend.Init(t.Context(), "documents", []driver.Index{{Field: "region"}}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %d, want 4: %+v", len(calls), calls)
	}
	want := []struct{ method, path string }{
		{http.MethodPut, "/_ilm/policy/huatuo_bamai-policy"},
		{http.MethodPut, "/_index_template/huatuo_bamai-template"},
		{http.MethodHead, "/huatuo_bamai-000001"},
		{http.MethodPut, "/huatuo_bamai-000001"},
	}
	for i := range want {
		if calls[i].method != want[i].method || calls[i].path != want[i].path {
			t.Errorf("call[%d] = %s %s, want %s %s", i, calls[i].method, calls[i].path, want[i].method, want[i].path)
		}
	}
	policyJSON, _ := json.Marshal(calls[0].body)
	if !strings.Contains(string(policyJSON), `"min_age":"14d"`) || !strings.Contains(string(policyJSON), `"max_age":"1d"`) {
		t.Errorf("policy = %s", policyJSON)
	}
	bootstrapJSON, _ := json.Marshal(calls[3].body)
	if !strings.Contains(string(bootstrapJSON), `"is_write_index":true`) {
		t.Errorf("bootstrap = %s", bootstrapJSON)
	}
}

func TestInitSkipsExistingBootstrapIndex(t *testing.T) {
	var calls int
	transport := lifecycleTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	backend := &Storage{transport: transport, index: "huatuo_bamai", ilmRetentionDays: 7}
	if err := backend.Init(t.Context(), "documents", nil); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want policy, template, HEAD", calls)
	}
}

func TestInitReportsLifecycleAPIError(t *testing.T) {
	transport := lifecycleTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"error":"no handler for _ilm"}`))}, nil
	})
	backend := &Storage{transport: transport, index: "huatuo_bamai", ilmRetentionDays: 7}
	err := backend.Init(t.Context(), "documents", nil)
	if err == nil || !strings.Contains(err.Error(), "no handler for _ilm") {
		t.Fatalf("Init() error = %v", err)
	}
}
