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

package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/constant"
)

type MetricInfo struct {
	Region         string   `json:"region"`
	Name           string   `json:"name"`
	Availability   string   `json:"availability"`
	Labels         []string `json:"labels,omitempty"`
	Help           string   `json:"help,omitempty"`
	Type           string   `json:"type,omitempty"`
	Unit           string   `json:"unit,omitempty"`
	MetadataStatus string   `json:"metadata_status,omitempty"`
}

type DiscoveryOptions struct {
	Region       string
	MetricPrefix string
	MetricNames  []string
	Maximum      int
	StartTime    time.Time
	EndTime      time.Time
}

type DiscoveryResult struct {
	Metrics   []MetricInfo
	Truncated bool
	Warnings  []string
}

// Discover lists candidate names cheaply or inspects a bounded selection.
// A catalog name is not proof of a series with the requested region label.
func Discover(ctx context.Context, request RequestFunc, options *DiscoveryOptions) (DiscoveryResult, error) {
	if request == nil || options == nil || options.Maximum <= 0 || options.Maximum > constant.MaxMetrics || len(options.MetricNames) > constant.MaxMetricNamesPerLookup {
		return DiscoveryResult{}, fmt.Errorf("%w: discovery limits or request callback are invalid", ErrInvalidRequest)
	}
	if options.Region == "" || options.StartTime.IsZero() || options.EndTime.IsZero() || options.StartTime.After(options.EndTime) {
		return DiscoveryResult{}, fmt.Errorf("%w: discovery requires a region and valid time bounds", ErrInvalidRequest)
	}
	bounds := url.Values{constant.QueryStartParameter: {options.StartTime.UTC().Format(time.RFC3339Nano)}, constant.QueryEndParameter: {options.EndTime.UTC().Format(time.RFC3339Nano)}}
	if len(options.MetricNames) > 0 {
		return inspectMetrics(ctx, request, options, bounds)
	}
	data, err := request(ctx, http.MethodGet, constant.PrometheusMetricNamesPath, bounds)
	if err != nil {
		return DiscoveryResult{}, err
	}
	var catalog struct {
		Status   string   `json:"status"`
		Data     []string `json:"data"`
		Warnings []string `json:"warnings"`
	}
	if err := decodeDiscoveryResponse(data, &catalog, "metric catalog"); err != nil {
		return DiscoveryResult{}, err
	}
	if catalog.Status != constant.APIStatusSuccess {
		return DiscoveryResult{}, fmt.Errorf("%w: metric catalog status=%q", ErrDecodeFailed, catalog.Status)
	}
	if catalog.Data == nil {
		return DiscoveryResult{}, fmt.Errorf("%w: metric catalog is missing data array", ErrDecodeFailed)
	}
	names := catalog.Data[:0]
	for _, name := range catalog.Data {
		if options.MetricPrefix == "" || strings.HasPrefix(name, options.MetricPrefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	result := DiscoveryResult{Metrics: make([]MetricInfo, 0, min(options.Maximum, len(names))), Warnings: catalog.Warnings}
	if len(names) > options.Maximum {
		result.Truncated = true
		result.Warnings = append(result.Warnings, fmt.Sprintf("MCP metric catalog limit reached: returned %d of %d candidate names", options.Maximum, len(names)))
		names = names[:options.Maximum]
	}
	for _, name := range names {
		result.Metrics = append(result.Metrics, MetricInfo{Region: options.Region, Name: name, Availability: constant.MetricAvailabilityUnknown})
	}
	return result, nil
}

func inspectMetrics(ctx context.Context, request RequestFunc, options *DiscoveryOptions, bounds url.Values) (DiscoveryResult, error) {
	result := DiscoveryResult{Metrics: make([]MetricInfo, 0, len(options.MetricNames))}
	for _, name := range options.MetricNames {
		if name == "" || len(name) > constant.MaxMetricNameLength || options.MetricPrefix != "" && !strings.HasPrefix(name, options.MetricPrefix) {
			return DiscoveryResult{}, fmt.Errorf("%w: metric name must be nonempty, bounded and within the configured prefix", ErrInvalidRequest)
		}
	}
	for _, name := range options.MetricNames {
		metric := MetricInfo{Region: options.Region, Name: name, Availability: constant.MetricAvailabilityUnknown}
		matcher := fmt.Sprintf("{%s=%q,%s=%q}", constant.PrometheusMetricNameLabel, name, constant.ArgumentRegion, options.Region)
		seriesValues := url.Values{constant.QueryStartParameter: bounds[constant.QueryStartParameter], constant.QueryEndParameter: bounds[constant.QueryEndParameter], constant.PrometheusMatchParameter: {matcher}, "limit": {"1"}}
		data, err := request(ctx, http.MethodGet, constant.PrometheusSeriesPath, seriesValues)
		if err == nil {
			var series struct {
				Status   string              `json:"status"`
				Data     []map[string]string `json:"data"`
				Warnings []string            `json:"warnings"`
			}
			err = decodeDiscoveryResponse(data, &series, "series existence response")
			result.Warnings = append(result.Warnings, series.Warnings...)
			if err == nil && series.Status != constant.APIStatusSuccess {
				err = fmt.Errorf("%w: series existence response status=%q", ErrDecodeFailed, series.Status)
			} else if err == nil && series.Data == nil {
				err = fmt.Errorf("%w: series existence response is missing data array", ErrDecodeFailed)
			} else if err == nil && len(series.Data) == 0 {
				metric.Availability = constant.MetricAvailabilityAbsent
			} else if err == nil {
				for _, item := range series.Data {
					if item[constant.PrometheusMetricNameLabel] != name || item[constant.ArgumentRegion] != options.Region {
						err = fmt.Errorf("%w: Prometheus returned an unscoped series", ErrDecodeFailed)
						break
					}
				}
				if err == nil {
					metric.Availability = constant.MetricAvailabilityPresent
				}
			}
		}
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("series existence verification failed for metric %q: %s; metric availability is unknown, not absent", name, boundedErrorValue(err.Error(), constant.MaxBackendErrorReasonBytes)))
		} else if metric.Availability == constant.MetricAvailabilityPresent {
			readMetricLabelKeys(ctx, request, &metric, matcher, bounds, &result.Warnings)
			readMetricMetadata(ctx, request, &metric, &result.Warnings)
		}
		result.Metrics = append(result.Metrics, metric)
	}
	return result, nil
}

func readMetricLabelKeys(ctx context.Context, request RequestFunc, metric *MetricInfo, matcher string, bounds url.Values, warnings *[]string) {
	values := url.Values{constant.QueryStartParameter: bounds[constant.QueryStartParameter], constant.QueryEndParameter: bounds[constant.QueryEndParameter], constant.PrometheusMatchParameter: {matcher}}
	data, err := request(ctx, http.MethodGet, constant.PrometheusLabelsPath, values)
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("label-key discovery failed for metric %q: %s", metric.Name, boundedErrorValue(err.Error(), constant.MaxBackendErrorReasonBytes)))
		return
	}
	var labels struct {
		Status   string   `json:"status"`
		Data     []string `json:"data"`
		Warnings []string `json:"warnings"`
	}
	if err := decodeDiscoveryResponse(data, &labels, "label-key response"); err != nil {
		*warnings = append(*warnings, fmt.Sprintf("metric %q: %v", metric.Name, err))
		return
	}
	*warnings = append(*warnings, labels.Warnings...)
	if labels.Status != constant.APIStatusSuccess {
		*warnings = append(*warnings, fmt.Sprintf("metric %q: invalid label-key response status=%q", metric.Name, labels.Status))
		return
	}
	if labels.Data == nil {
		*warnings = append(*warnings, fmt.Sprintf("metric %q: label-key response is missing data array", metric.Name))
		return
	}
	seen := make(map[string]struct{}, len(labels.Data))
	for _, label := range labels.Data {
		if label != constant.PrometheusMetricNameLabel {
			seen[label] = struct{}{}
		}
	}
	metric.Labels = make([]string, 0, len(seen))
	for label := range seen {
		metric.Labels = append(metric.Labels, label)
	}
	sort.Strings(metric.Labels)
}

func readMetricMetadata(ctx context.Context, request RequestFunc, metric *MetricInfo, warnings *[]string) {
	metric.MetadataStatus = constant.MetricMetadataUnavailable
	data, err := request(ctx, http.MethodGet, constant.PrometheusMetadataPath, url.Values{"metric": {metric.Name}, "limit": {"1"}})
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("metric HELP/TYPE/UNIT unavailable for metric %q: %s", metric.Name, boundedErrorValue(err.Error(), constant.MaxBackendErrorReasonBytes)))
		return
	}
	var response struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
		Data     map[string][]struct {
			Help string `json:"help"`
			Type string `json:"type"`
			Unit string `json:"unit"`
		} `json:"data"`
	}
	if err := decodeDiscoveryResponse(data, &response, "metric metadata response"); err != nil {
		*warnings = append(*warnings, fmt.Sprintf("metric %q: %v", metric.Name, err))
		return
	}
	*warnings = append(*warnings, response.Warnings...)
	if response.Status != constant.APIStatusSuccess {
		*warnings = append(*warnings, fmt.Sprintf("metric %q: invalid backend metric metadata response status=%q", metric.Name, response.Status))
		return
	}
	if response.Data == nil {
		*warnings = append(*warnings, fmt.Sprintf("metric %q: metadata response is missing data object", metric.Name))
		return
	}
	entries := response.Data[metric.Name]
	if len(entries) == 0 {
		return
	}
	first := entries[0]
	if first.Help == "" || first.Type == "" {
		metric.MetadataStatus = constant.MetricMetadataAmbiguous
		return
	}
	for _, entry := range entries[1:] {
		if entry != first {
			metric.MetadataStatus = constant.MetricMetadataAmbiguous
			return
		}
	}
	metric.Help, metric.Type, metric.Unit = first.Help, first.Type, first.Unit
	metric.MetadataStatus = constant.MetricMetadataAvailable
}

func decodeDiscoveryResponse(data []byte, target any, description string) error {
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%w: decode %s: %w; response=%q", ErrDecodeFailed, description, err, boundedErrorValue(string(data), constant.MaxBackendErrorReasonBytes))
	}
	return nil
}
