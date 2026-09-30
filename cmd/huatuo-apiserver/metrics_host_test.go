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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestApiserverRuntimeMetricsHTTPHost(t *testing.T) {
	d := &Daemon{}
	if _, err := setupMetrics(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	scrape := httptest.NewServer(promhttp.HandlerFor(d.metrics, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	}))
	defer scrape.Close()
	response, err := http.Get(scrape.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("scrape status = %d", response.StatusCode)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"huatuo_apiserver_process_cpu_seconds_total": false,
		"huatuo_apiserver_go_goroutines":             false,
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		for name := range want {
			if strings.HasPrefix(line, name+"{") {
				if !strings.Contains(line, "host="+strconv.Quote(host)) || !strings.Contains(line, `region=""`) {
					t.Errorf("HTTP metric = %q, want host %q and empty region", line, host)
				}
				want[name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("HTTP scrape omitted %s", name)
		}
	}
}
