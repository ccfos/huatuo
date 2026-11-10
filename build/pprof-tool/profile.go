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
	"errors"
	"fmt"
	"math"
	"net/url"

	"github.com/google/pprof/profile"
)

type sampleTotal struct {
	Type  string `json:"type"`
	Unit  string `json:"unit"`
	Total int64  `json:"total"`
}

type profileSummary struct {
	Endpoint      string        `json:"endpoint"`
	Mode          string        `json:"mode"`
	DurationNanos int64         `json:"duration_ns,omitempty"`
	Samples       []sampleTotal `json:"samples"`
}

func (r *resourceSummary) addProfile(endpoint *url.URL, value string, p *profile.Profile, totals []sampleTotal) error {
	summary := profileSummary{
		Endpoint:      value,
		Mode:          "snapshot",
		DurationNanos: p.DurationNanos,
		Samples:       totals,
	}
	if endpoint.Query().Has("seconds") {
		summary.Mode = "delta"
	}

	switch {
	case endpoint.Path == "profile":
		summary.Mode = "cpu"
		cpuNS, err := findSampleTotal(totals, "cpu", "nanoseconds")
		if err != nil {
			return err
		}

		if p.DurationNanos <= 0 {
			return errors.New("CPU profile requires positive duration")
		}

		percent := float64(cpuNS) / float64(p.DurationNanos) * 100
		r.CPUPercent = &percent
		if cpuNS == 0 {
			r.Warnings = append(r.Warnings, "CPU profile has no samples; this does not establish zero CPU usage")
		}
	case endpoint.Path == "heap" && summary.Mode == "snapshot":
		value, err := findSampleTotal(totals, "inuse_space", "bytes")
		if err != nil {
			return err
		}

		r.HeapInuseBytes = &value
	case endpoint.Path == "goroutine" && summary.Mode == "snapshot":
		value, err := findSampleTotal(totals, "goroutine", "count")
		if err != nil {
			return err
		}

		r.Goroutines = &value
	}

	r.Profiles = append(r.Profiles, summary)
	return nil
}

func sumSampleValues(p *profile.Profile) ([]sampleTotal, error) {
	totals := make([]sampleTotal, len(p.SampleType))
	if len(totals) == 0 {
		return nil, errors.New("profile has no sample types")
	}

	for i, kind := range p.SampleType {
		if kind.Type == "" || kind.Unit == "" {
			return nil, errors.New("sample type and unit must not be empty")
		}

		for _, previous := range totals[:i] {
			if previous.Type == kind.Type && previous.Unit == kind.Unit {
				return nil, fmt.Errorf("duplicate sample type %s/%s", kind.Type, kind.Unit)
			}
		}

		totals[i] = sampleTotal{
			Type: kind.Type,
			Unit: kind.Unit,
		}
		for _, sample := range p.Sample {
			value, total := sample.Value[i], totals[i].Total
			overflows := value > 0 && total > math.MaxInt64-value
			underflows := value < 0 && total < math.MinInt64-value
			if overflows || underflows {
				return nil, fmt.Errorf("sample sum overflows int64 for %s/%s", kind.Type, kind.Unit)
			}

			totals[i].Total += value
		}
	}

	return totals, nil
}

func findSampleTotal(totals []sampleTotal, name, unit string) (int64, error) {
	for _, sample := range totals {
		if sample.Type == name && sample.Unit == unit {
			if sample.Total < 0 {
				return 0, fmt.Errorf("negative resource sample %s/%s", name, unit)
			}

			return sample.Total, nil
		}
	}

	return 0, fmt.Errorf("missing sample type %s/%s", name, unit)
}
