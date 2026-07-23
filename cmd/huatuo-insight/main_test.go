// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunReadsMetricsFromHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			t.Fatalf("path = %q, want /metrics", r.URL.Path)
		}
		_, _ = w.Write([]byte("huatuo_cpu_usage 10\nnode_other 99\n"))
	}))
	defer server.Close()

	var out bytes.Buffer
	err := run([]string{"--metrics-url", server.URL + "/metrics", "--prefix", "huatuo_", "--top", "1"}, &out)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}

	var decoded struct {
		MatchedSamples int `json:"matched_samples"`
		Top            []struct {
			Name string `json:"name"`
		} `json:"top"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON output %q: %v", out.String(), err)
	}
	if decoded.MatchedSamples != 1 || len(decoded.Top) != 1 || decoded.Top[0].Name != "huatuo_cpu_usage" {
		t.Fatalf("unexpected output: %s", out.String())
	}
}
