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

//go:build integration

package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	bpfmock "github.com/ccfos/huatuo/integration/bpf_mock/common"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func metricValue(text, name string) (float64, error) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.SplitN(fields[0], "{", 2)[0] != "huatuo_bamai_memory_free_"+name {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, fmt.Errorf("invalid metric %s", name)
		}
		return value, nil
	}
	return 0, fmt.Errorf("missing metric %s", name)
}

func verifyLatencyMetrics(before, after string, expectedRawReclaimNS, expectedRawCompactionNS uint64) error {
	for _, item := range []struct {
		name      string
		elapsedNS uint64
	}{
		{"allocpages_stall", expectedRawReclaimNS}, {"compaction_stall", expectedRawCompactionNS},
	} {
		baselineMilliseconds, err := metricValue(before, item.name)
		if err != nil {
			return err
		}
		actualMilliseconds, err := metricValue(after, item.name)
		if err != nil {
			return err
		}
		if baselineMilliseconds != 0 || item.elapsedNS < 40000000 || actualMilliseconds < 40 || actualMilliseconds > float64(item.elapsedNS)/1e6 {
			return fmt.Errorf("%s: baseline %g, actual %g ms outside [40, %g]", item.name, baselineMilliseconds, actualMilliseconds, float64(item.elapsedNS)/1e6)
		}
	}
	return nil
}

func verifyUnmatchedReclaimEnd(before, after string) error {
	for _, name := range []string{"allocpages_stall", "compaction_stall"} {
		baselineMilliseconds, err := metricValue(before, name)
		if err != nil {
			return err
		}
		actualMilliseconds, err := metricValue(after, name)
		if err != nil {
			return err
		}
		if actualMilliseconds != baselineMilliseconds {
			return fmt.Errorf("unmatched reclaim end changed %s: %g -> %g", name, baselineMilliseconds, actualMilliseconds)
		}
	}
	return nil
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: %s SCENARIO_DIR", os.Args[0])
	}
	dir := os.Args[1]
	before, err := os.ReadFile(filepath.Join(dir, "before.metrics"))
	if err != nil {
		return err
	}
	after, err := os.ReadFile(filepath.Join(dir, "after.metrics"))
	if err != nil {
		return err
	}
	unmatchedBefore, err := os.ReadFile(filepath.Join(dir, "unmatched-before.metrics"))
	if err != nil {
		return err
	}
	paired, err := os.ReadFile(filepath.Join(dir, "paired.metrics"))
	if err != nil {
		return err
	}
	if err := verifyUnmatchedReclaimEnd(string(before), string(unmatchedBefore)); err != nil {
		return err
	}
	if err := verifyUnmatchedReclaimEnd(string(paired), string(after)); err != nil {
		return err
	}
	expectedRawElapsedNS := map[string]uint64{}
	for _, name := range []string{"reclaim", "compaction"} {
		for i := 1; i <= 2; i++ {
			expectedRaw, err := bpfmock.ReadJSON[struct {
				ElapsedNS uint64 `json:"elapsed_ns"`
			}](filepath.Join(dir, fmt.Sprintf("%s-%d.json", name, i)))
			if err != nil {
				return err
			}
			expectedRawElapsedNS[name] += expectedRaw.ElapsedNS
		}
	}
	if err := verifyLatencyMetrics(string(before), string(after), expectedRawElapsedNS["reclaim"], expectedRawElapsedNS["compaction"]); err != nil {
		return err
	}
	fmt.Println("PASS memory_free_compact: published reclaim and compaction latency bounds")
	return nil
}
