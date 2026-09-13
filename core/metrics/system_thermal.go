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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

type thermalCollector struct{}

func init() {
	tracing.RegisterEventTracing("thermal", newThermalCollector)
}

func thermalZonePaths() ([]string, error) {
	return filepath.Glob(filepath.Join(
		procfs.DefaultPathByType("sys"), "class/thermal/thermal_zone*"))
}

func newThermalCollector() (*tracing.EventTracingAttr, error) {
	paths, err := thermalZonePaths()
	if err != nil {
		return nil, fmt.Errorf("find thermal zones: %w", err)
	}
	if len(paths) == 0 {
		return nil, types.ErrNotSupported
	}
	return &tracing.EventTracingAttr{
		TracingData: &thermalCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

func parseThermalTemperature(raw []byte) (float64, error) {
	milliCelsius, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse thermal temperature: %w", err)
	}
	return float64(milliCelsius) / 1000, nil
}

func readThermalZone(path string) (*metric.Data, error) {
	typeRaw, err := os.ReadFile(filepath.Join(path, "type"))
	if err != nil {
		return nil, err
	}
	tempRaw, err := os.ReadFile(filepath.Join(path, "temp"))
	if err != nil {
		return nil, err
	}
	temperature, err := parseThermalTemperature(tempRaw)
	if err != nil {
		return nil, err
	}
	return metric.NewGaugeData("temperature_celsius", temperature,
		"Thermal zone temperature in degrees Celsius.", map[string]string{
			"zone": filepath.Base(path),
			"type": strings.TrimSpace(string(typeRaw)),
		}), nil
}

func (c *thermalCollector) Update() ([]*metric.Data, error) {
	paths, err := thermalZonePaths()
	if err != nil {
		return nil, err
	}
	metrics := make([]*metric.Data, 0, len(paths))
	for _, path := range paths {
		data, err := readThermalZone(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		metrics = append(metrics, data)
	}
	return metrics, nil
}
