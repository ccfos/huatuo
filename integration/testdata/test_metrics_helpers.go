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
	"fmt"
	"io"
	"math"
	"net/http"
	"os"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

func fetch(client *http.Client, url, output string) (map[string]*dto.MetricFamily, error) {
	var data []byte
	status := 0
	response, requestErr := client.Get(url)
	if requestErr == nil {
		defer response.Body.Close()
		status = response.StatusCode
		data, requestErr = io.ReadAll(response.Body)
	}

	// Keep server diagnostics even when status checks or decoding fail.
	if output != "" {
		if err := os.WriteFile(output, data, 0o600); err != nil {
			return nil, err
		}

		transportError := ""
		if requestErr != nil {
			transportError = requestErr.Error()
		}

		diagnostic := fmt.Sprintf("http_status=%d\ntransport_error=%s\n", status, transportError)
		if err := os.WriteFile(output+".http", []byte(diagnostic), 0o600); err != nil {
			return nil, err
		}
	}

	var families map[string]*dto.MetricFamily
	var err error
	switch {
	case requestErr != nil:
		err = fmt.Errorf("fetch %s: %w", url, requestErr)
	case status != http.StatusOK:
		err = fmt.Errorf("%s: HTTP %d", url, status)
	default:
		var parser expfmt.TextParser
		families, err = parser.TextToMetricFamilies(bytes.NewReader(data))
		if err != nil {
			err = fmt.Errorf("parse metrics from %s: %w", url, err)
		}
	}
	if err != nil && output == "" {
		fmt.Fprintf(os.Stderr, "metrics response: HTTP %d\n%s\n", status, data)
	}

	return families, err
}

func labels(metric *dto.Metric) map[string]string {
	result := make(map[string]string, len(metric.GetLabel()))
	for _, pair := range metric.GetLabel() {
		result[pair.GetName()] = pair.GetValue()
	}
	return result
}

func matchesContainer(values map[string]string, pod, container, namespace string) bool {
	return values["container_host"] == pod && values["container_name"] == container &&
		values["container_hostnamespace"] == namespace
}

type metricSample struct {
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

type metricFamily struct {
	Type    string         `json:"type"`
	Help    string         `json:"help"`
	Samples []metricSample `json:"samples"`
}

func metricJSON(families map[string]*dto.MetricFamily) map[string]metricFamily {
	result := make(map[string]metricFamily, len(families))
	for name, family := range families {
		f := metricFamily{
			Type:    family.GetType().String(),
			Help:    family.GetHelp(),
			Samples: make([]metricSample, 0, len(family.GetMetric())),
		}
		for _, metric := range family.GetMetric() {
			if metric.Gauge != nil {
				f.Samples = append(f.Samples, metricSample{
					Labels: labels(metric),
					Value:  metric.GetGauge().GetValue(),
				})
			} else if metric.Counter != nil {
				f.Samples = append(f.Samples, metricSample{
					Labels: labels(metric),
					Value:  metric.GetCounter().GetValue(),
				})
			}
		}
		result[name] = f
	}
	return result
}

func validateLabels(actual, expected map[string]string) error {
	if len(actual) != len(expected) {
		return fmt.Errorf("labels=%v, want %v", actual, expected)
	}
	for name, want := range expected {
		if value, ok := actual[name]; !ok || value != want {
			return fmt.Errorf("label %s=%q, want %q", name, value, want)
		}
	}
	return nil
}

func gaugeValue(family *dto.MetricFamily, expected map[string]string) (float64, error) {
	if family == nil || family.GetType() != dto.MetricType_GAUGE || family.GetHelp() == "" {
		return 0, fmt.Errorf("%s: missing Gauge/HELP", family.GetName())
	}
	count, value := 0, 0.0
	for _, metric := range family.GetMetric() {
		values := labels(metric)
		if _, container := expected["container_host"]; container &&
			!matchesContainer(values, expected["container_host"], expected["container_name"], expected["container_hostnamespace"]) {
			continue
		}
		if err := validateLabels(values, expected); err != nil {
			return 0, fmt.Errorf("%s: %w", family.GetName(), err)
		}
		count++
		value = metric.GetGauge().GetValue()
	}
	if count != 1 || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, fmt.Errorf("%s: expected one finite Gauge sample, got %d, value=%g", family.GetName(), count, value)
	}
	return value, nil
}

func validateCollector(families map[string]*dto.MetricFamily, collector string) error {
	count := 0
	for _, metric := range families["huatuo_bamai_scrape_collector_success"].GetMetric() {
		if labels(metric)["collector"] != collector {
			continue
		}
		count++
		if metric.GetGauge().GetValue() != 1 {
			return fmt.Errorf("%s scrape_collector_success is not 1", collector)
		}
	}
	if count != 1 {
		return fmt.Errorf("expected one %s scrape success, got %d", collector, count)
	}
	return nil
}
