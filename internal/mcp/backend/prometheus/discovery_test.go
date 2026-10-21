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
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/constant"
)

func discoveryBounds() (time.Time, time.Time) {
	start := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	return start, start.Add(2 * time.Hour)
}

func TestDiscoverCatalogDoesNotScanRegionSeries(t *testing.T) {
	start, end := discoveryBounds()
	requests := 0
	reader := newTestReader(t, &Config{}, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != constant.PrometheusMetricNamesPath || len(r.URL.Query()[constant.PrometheusMatchParameter]) != 0 || r.URL.Query().Get("start") != start.Format(time.RFC3339Nano) || r.URL.Query().Get("end") != end.Format(time.RFC3339Nano) {
			t.Errorf("candidate catalog scanned series: %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `{"status":"success","data":["up","huatuo_c","huatuo_a","huatuo_b"],"warnings":["catalog partial"]}`)
	})
	result, err := Discover(t.Context(), reader.Request, &DiscoveryOptions{Region: "us01", MetricPrefix: "huatuo_", Maximum: 2, StartTime: start, EndTime: end})
	if err != nil || requests != 1 || !result.Truncated || len(result.Warnings) != 2 || !reflect.DeepEqual(result.Metrics, []MetricInfo{{Region: "us01", Name: "huatuo_a", Availability: constant.MetricAvailabilityUnknown}, {Region: "us01", Name: "huatuo_b", Availability: constant.MetricAvailabilityUnknown}}) {
		t.Fatalf("catalog result=%+v requests=%d error=%v", result, requests, err)
	}
}

func TestDiscoverTargetedMetricHasScopedExistenceLabelsAndMetadata(t *testing.T) {
	start, end := discoveryBounds()
	paths := make([]string, 0, 3)
	reader := newTestReader(t, &Config{}, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case constant.PrometheusSeriesPath:
			if r.URL.Query().Get("match[]") != `{__name__="huatuo_cpu",region="us01"}` || r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("start") != start.Format(time.RFC3339Nano) || r.URL.Query().Get("end") != end.Format(time.RFC3339Nano) {
				t.Errorf("existence check is not scoped: %s", r.URL)
			}
			_, _ = io.WriteString(w, `{"status":"success","data":[{"__name__":"huatuo_cpu","region":"us01","host":"node"}]}`)
		case constant.PrometheusLabelsPath:
			if r.URL.Query().Get("match[]") != `{__name__="huatuo_cpu",region="us01"}` || r.URL.Query().Get("start") != start.Format(time.RFC3339Nano) || r.URL.Query().Get("end") != end.Format(time.RFC3339Nano) {
				t.Errorf("label keys are not scoped: %s", r.URL)
			}
			_, _ = io.WriteString(w, `{"status":"success","data":["region","host","__name__","host"]}`)
		case constant.PrometheusMetadataPath:
			if r.URL.Query().Get("metric") != "huatuo_cpu" || r.URL.Query().Get("limit") != "1" {
				t.Errorf("metadata not selected by exact metric: %s", r.URL)
			}
			_, _ = io.WriteString(w, `{"status":"success","data":{"huatuo_cpu":[{"help":"CPU seconds","type":"counter","unit":"seconds"}]}}`)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL)
		}
	})
	result, err := Discover(t.Context(), reader.Request, &DiscoveryOptions{Region: "us01", MetricPrefix: "huatuo_", Maximum: 2, MetricNames: []string{"huatuo_cpu"}, StartTime: start, EndTime: end})
	if err != nil || !reflect.DeepEqual(paths, []string{constant.PrometheusSeriesPath, constant.PrometheusLabelsPath, constant.PrometheusMetadataPath}) || !reflect.DeepEqual(result.Metrics, []MetricInfo{{Region: "us01", Name: "huatuo_cpu", Availability: constant.MetricAvailabilityPresent, Labels: []string{"host", "region"}, Help: "CPU seconds", Type: "counter", Unit: "seconds", MetadataStatus: constant.MetricMetadataAvailable}}) || len(result.Warnings) != 0 {
		t.Fatalf("targeted metadata result=%+v paths=%v error=%v", result, paths, err)
	}
}

func TestDiscoverTargetedAbsentAndUnknownAreDistinct(t *testing.T) {
	start, end := discoveryBounds()
	var paths []string
	reader := newTestReader(t, &Config{}, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		match := r.URL.Query().Get("match[]")
		switch {
		case strings.Contains(match, "huatuo_absent"):
			_, _ = io.WriteString(w, `{"status":"success","data":[]}`)
		case strings.Contains(match, "huatuo_unknown"):
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected detail request: %s", r.URL)
		}
	})
	result, err := Discover(t.Context(), reader.Request, &DiscoveryOptions{Region: "us01", MetricPrefix: "huatuo_", Maximum: 2, MetricNames: []string{"huatuo_absent", "huatuo_unknown"}, StartTime: start, EndTime: end})
	if err != nil || len(paths) != 2 || len(result.Metrics) != 2 || result.Metrics[0].Availability != constant.MetricAvailabilityAbsent || result.Metrics[1].Availability != constant.MetricAvailabilityUnknown || len(result.Warnings) != 1 {
		t.Fatalf("failed checks confused with absent series: %+v paths=%v error=%v", result, paths, err)
	}
}

func TestDiscoveryPreservesDecodeCausesAndBackendWarnings(t *testing.T) {
	start, end := discoveryBounds()
	for _, brokenPath := range []string{constant.PrometheusMetricNamesPath, constant.PrometheusSeriesPath, constant.PrometheusLabelsPath, constant.PrometheusMetadataPath} {
		t.Run(brokenPath, func(t *testing.T) {
			options := &DiscoveryOptions{Region: "cn", Maximum: 1, MetricNames: []string{"huatuo_cpu"}, StartTime: start, EndTime: end}
			if brokenPath == constant.PrometheusMetricNamesPath {
				options.MetricNames = nil
			}
			result, err := Discover(t.Context(), func(_ context.Context, _, path string, _ url.Values) ([]byte, error) {
				if path == brokenPath {
					return []byte(`{"status":"success","data":42}`), nil
				}
				switch path {
				case constant.PrometheusSeriesPath:
					return []byte(`{"status":"success","data":[{"__name__":"huatuo_cpu","region":"cn"}],"warnings":["backend series partial"]}`), nil
				case constant.PrometheusLabelsPath:
					return []byte(`{"status":"success","data":["region"],"warnings":["backend labels partial"]}`), nil
				default:
					return []byte(`{"status":"success","data":{},"warnings":["backend metadata partial"]}`), nil
				}
			}, options)
			text := strings.Join(result.Warnings, " ")
			if err != nil {
				text += err.Error()
			}
			if !strings.Contains(text, "cannot unmarshal number") {
				t.Fatalf("decode cause hidden: %+v, %v", result, err)
			}
			if brokenPath == constant.PrometheusLabelsPath || brokenPath == constant.PrometheusMetadataPath {
				if !strings.Contains(text, "backend series partial") {
					t.Fatalf("backend warnings lost: %+v", result)
				}
			}
		})
	}
}

func TestMissingSeriesDataIsUnknownNotAbsent(t *testing.T) {
	start, end := discoveryBounds()
	result, err := Discover(t.Context(), func(context.Context, string, string, url.Values) ([]byte, error) {
		return []byte(`{"status":"success"}`), nil
	}, &DiscoveryOptions{Region: "cn", Maximum: 1, MetricNames: []string{"huatuo_cpu"}, StartTime: start, EndTime: end})
	if err != nil || len(result.Metrics) != 1 || result.Metrics[0].Availability != constant.MetricAvailabilityUnknown || !strings.Contains(strings.Join(result.Warnings, " "), "missing data array") {
		t.Fatalf("malformed success response became absent data: %+v, %v", result, err)
	}
}

func TestDiscoverMetadataAbsentDoesNotInventUnits(t *testing.T) {
	start, end := discoveryBounds()
	for _, metadata := range []string{`{"status":"success","data":{}}`, `{"status":"success","data":{"huatuo_cpu":[{"help":"one","type":"counter"},{"help":"two","type":"gauge"}]}}`} {
		result, err := Discover(t.Context(), func(_ context.Context, _, path string, _ url.Values) ([]byte, error) {
			switch path {
			case constant.PrometheusSeriesPath:
				return []byte(`{"status":"success","data":[{"__name__":"huatuo_cpu","region":"cn"}]}`), nil
			case constant.PrometheusLabelsPath:
				return []byte(`{"status":"success","data":["__name__","region"]}`), nil
			default:
				return []byte(metadata), nil
			}
		}, &DiscoveryOptions{Region: "cn", MetricPrefix: "huatuo_", Maximum: 1, MetricNames: []string{"huatuo_cpu"}, StartTime: start, EndTime: end})
		if err != nil || len(result.Metrics) != 1 || result.Metrics[0].MetadataStatus == constant.MetricMetadataAvailable || result.Metrics[0].Help != "" || result.Metrics[0].Type != "" || result.Metrics[0].Unit != "" {
			t.Fatalf("backend missing/conflicting metadata was invented: %+v error=%v", result, err)
		}
	}
}

func TestDiscoverRejectsInvalidScopeAndMetricSelection(t *testing.T) {
	start, end := discoveryBounds()
	for _, options := range []DiscoveryOptions{
		{Region: "cn", Maximum: 1},
		{Maximum: 1, StartTime: start, EndTime: end},
		{Region: "cn", Maximum: 1, StartTime: end, EndTime: start},
		{Region: "cn", Maximum: 1, MetricPrefix: "huatuo_", MetricNames: []string{"other"}, StartTime: start, EndTime: end},
		{Region: "cn", Maximum: 1, MetricNames: make([]string, constant.MaxMetricNamesPerLookup+1), StartTime: start, EndTime: end},
	} {
		_, err := Discover(t.Context(), func(context.Context, string, string, url.Values) ([]byte, error) {
			t.Fatal("invalid discovery scope reached backend")
			return nil, nil
		}, &options)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid scope accepted: %+v, %v", options, err)
		}
	}
}
