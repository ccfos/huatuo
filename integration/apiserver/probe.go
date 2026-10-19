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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"time"

	querier "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	profiling "github.com/ccfos/huatuo/pkg/profiling"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fetch(method, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("API_TOKEN"))
	if body != nil {
		req.Header.Set("Content-Type", "application/proto")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, data)
	}
	return data, nil
}

func run() error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: probe capabilities URL | flamegraph URL JOB RAW_JSON")
	}
	base := os.Args[2]
	switch os.Args[1] {
	case "capabilities":
		data, err := fetch("GET", base+"/v1/profiling/capabilities", nil)
		if err != nil {
			return err
		}
		type capability struct {
			Type     profiling.Type     `json:"type"`
			Language profiling.Language `json:"language"`
			Modes    []profiling.Mode   `json:"modes"`
			Scopes   []string           `json:"supported_scopes"`
			Binary   bool               `json:"supports_binary_match"`
		}
		var response struct {
			Data struct {
				Items []capability `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return err
		}
		want := make([]capability, 0)
		for _, c := range profiling.Capabilities() {
			scopes := make([]string, len(c.SupportedScopes))
			for i, scope := range c.SupportedScopes {
				scopes[i] = string(scope)
			}
			want = append(want, capability{c.Type, c.Language, c.Modes, scopes, c.SupportsBinaryMatch})
		}
		if !reflect.DeepEqual(response.Data.Items, want) {
			return fmt.Errorf("capability API differs from pkg/profiling: got %+v, want %+v", response.Data.Items, want)
		}
		fmt.Println(string(data))
	case "flamegraph":
		if len(os.Args) != 5 {
			return fmt.Errorf("flamegraph requires URL JOB RAW_JSON")
		}
		raw, err := os.ReadFile(os.Args[4])
		if err != nil {
			return err
		}
		var page struct {
			Data struct {
				Items []struct {
					Type     string    `json:"profile_type"`
					Started  time.Time `json:"started_timestamp"`
					Uploaded time.Time `json:"uploaded_timestamp"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		if len(page.Data.Items) == 0 {
			return fmt.Errorf("raw result has no samples")
		}
		start, end := page.Data.Items[0].Started, page.Data.Items[0].Uploaded
		for _, item := range page.Data.Items {
			if item.Started.Before(start) {
				start = item.Started
			}
			if item.Uploaded.After(end) {
				end = item.Uploaded
			}
		}
		req := &querier.SelectMergeStacktracesRequest{
			ProfileTypeID: page.Data.Items[0].Type,
			LabelSelector: fmt.Sprintf("{id=%q}", os.Args[3]), Start: start.Add(-time.Second).UnixMilli(), End: end.Add(time.Second).UnixMilli(),
		}
		body, err := proto.Marshal(req)
		if err != nil {
			return err
		}
		data, err := fetch("POST", base+"/v1/profiling/flamegraph/querier.v1.QuerierService/SelectMergeStacktraces", body)
		if err != nil {
			return err
		}
		var result querier.SelectMergeStacktracesResponse
		if err := proto.Unmarshal(data, &result); err != nil {
			return err
		}
		graph := result.GetFlamegraph()
		if graph == nil || graph.Total <= 0 || len(graph.Names) == 0 || len(graph.Levels) == 0 {
			return fmt.Errorf("empty flamegraph for job %s", os.Args[3])
		}
		data, err = protojson.Marshal(&result)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	default:
		return fmt.Errorf("unknown probe: %s", os.Args[1])
	}
	return nil
}
