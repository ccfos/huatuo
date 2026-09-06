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

package collector

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

type entropyCollector struct{}

func init() {
	tracing.RegisterEventTracing("entropy", newEntropyCollector)
}

func newEntropyCollector() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &entropyCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func parseEntropyValue(name string, raw []byte) (uint64, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return value, nil
}

func readEntropyValue(name string) (uint64, error) {
	raw, err := os.ReadFile(procfs.Path("sys/kernel/random", name))
	if err != nil {
		return 0, err
	}
	return parseEntropyValue(name, raw)
}

func (c *entropyCollector) Update() ([]*metric.Data, error) {
	available, err := readEntropyValue("entropy_avail")
	if err != nil {
		return nil, err
	}
	poolSize, err := readEntropyValue("poolsize")
	if err != nil {
		return nil, err
	}
	availablePercent := float64(0)
	if poolSize > 0 {
		availablePercent = float64(available) / float64(poolSize) * 100
	}
	return []*metric.Data{
		metric.NewGaugeData("available_bits", float64(available),
			"Bits of entropy available to the kernel random-number generator.", nil),
		metric.NewGaugeData("pool_size_bits", float64(poolSize),
			"Kernel random-number generator entropy pool size in bits.", nil),
		metric.NewGaugeData("available_percent", availablePercent,
			"Available entropy as a percentage of the pool size.", nil),
	}, nil
}
