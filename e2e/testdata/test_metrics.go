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
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

type metricSample struct {
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

type metricFamily struct {
	Type    string         `json:"type"`
	Help    string         `json:"help"`
	Samples []metricSample `json:"samples"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "metrics reader:", err)
		os.Exit(1)
	}
}

func run() error {
	url := flag.String("url", "", "Prometheus metrics URL")
	flag.Parse()
	_, families, err := fetch(&http.Client{Timeout: 5 * time.Second}, *url)
	if err != nil {
		return err
	}
	result := make(map[string]metricFamily, len(families))
	for name, family := range families {
		f := metricFamily{Type: family.GetType().String(), Help: family.GetHelp(), Samples: make([]metricSample, 0, len(family.GetMetric()))}
		for _, metric := range family.GetMetric() {
			if metric.Gauge != nil {
				f.Samples = append(f.Samples, metricSample{Labels: labels(metric), Value: metric.GetGauge().GetValue()})
			} else if metric.Counter != nil {
				f.Samples = append(f.Samples, metricSample{Labels: labels(metric), Value: metric.GetCounter().GetValue()})
			}
		}
		result[name] = f
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
