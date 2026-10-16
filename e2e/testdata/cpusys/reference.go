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
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/prometheus/procfs"
)

type referenceSample struct {
	At     time.Time `json:"at"`
	System float64   `json:"system"`
	Delta  float64   `json:"delta"`
}

func systemPercent(previous, current *procfs.CPUStat) (float64, error) {
	total := func(cpu *procfs.CPUStat) float64 {
		return cpu.User + cpu.Nice + cpu.System + cpu.Idle + cpu.Iowait + cpu.IRQ + cpu.SoftIRQ + cpu.Steal
	}
	elapsed, system := total(current)-total(previous), current.System-previous.System
	if elapsed <= 0 || system < 0 || system > elapsed {
		return 0, errors.New("host CPU counters did not advance consistently")
	}
	return 100 * system / elapsed, nil
}

func sampleUsage(duration, interval time.Duration, writer io.Writer) error {
	if duration <= 0 || interval <= 0 || duration < interval {
		return errors.New("sample requires positive duration >= interval")
	}
	fs, err := procfs.NewFS("/proc")
	if err != nil {
		return err
	}
	previous, err := fs.Stat()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	started := time.Now()
	var lastPercent float64
	hasPercent := false
	for range ticker.C {
		current, err := fs.Stat()
		if err != nil {
			return err
		}
		percent, err := systemPercent(&previous.CPUTotal, &current.CPUTotal)
		if err != nil {
			return err
		}
		sample := referenceSample{At: time.Now().UTC(), System: percent, Delta: percent - lastPercent}
		if !hasPercent {
			sample.Delta = 0
		}
		if err := json.NewEncoder(writer).Encode(sample); err != nil {
			return err
		}
		previous, lastPercent = current, percent
		hasPercent = true
		if time.Since(started) >= duration {
			return nil
		}
	}
	return nil
}

type thresholds struct {
	Usage       int64   `json:"usage"`
	Delta       int64   `json:"delta"`
	BaselineMax float64 `json:"baseline_max"`
	BusyMin     float64 `json:"busy_min"`
}

func readSamples(path string) ([]referenceSample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var samples []referenceSample
	decoder := json.NewDecoder(file)
	for {
		var sample referenceSample
		if err := decoder.Decode(&sample); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if sample.System < 0 || sample.System > 100 {
			return nil, fmt.Errorf("invalid system percentage %g in %s", sample.System, path)
		}
		samples = append(samples, sample)
	}
	if len(samples) < 2 {
		return nil, fmt.Errorf("need at least two reference samples in %s", path)
	}
	return samples, nil
}

func chooseThresholds(baselinePath, busyPath string) (thresholds, error) {
	baseline, err := readSamples(baselinePath)
	if err != nil {
		return thresholds{}, err
	}
	busy, err := readSamples(busyPath)
	if err != nil {
		return thresholds{}, err
	}
	var result thresholds
	for _, sample := range baseline {
		result.BaselineMax = math.Max(result.BaselineMax, sample.System)
	}
	result.BusyMin = math.Min(busy[len(busy)-2].System, busy[len(busy)-1].System)
	low, high := int64(math.Ceil(result.BaselineMax)), int64(math.Floor(result.BusyMin))
	gap := high - low
	if gap < 3 {
		return thresholds{}, fmt.Errorf("system workload has insufficient headroom: baseline max %.3f%%, busy min %.3f%%; need at least 3 integer percentage points", result.BaselineMax, result.BusyMin)
	}
	// A step may straddle two tracer ticks. Leave room for both the partial
	// first interval and the remaining increase in the following interval.
	result.Usage = low + gap/3
	result.Delta = max(0, gap/3-1)
	return result, nil
}
