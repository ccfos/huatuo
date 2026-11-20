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
	"os"
	"path/filepath"
	"strings"

	"github.com/ccfos/huatuo/internal/procfs/sysfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/internal/utils/parseutil"
	"github.com/ccfos/huatuo/pkg/metric"
)

type bondingCollector struct{}

func init() { tracing.RegisterEventTracing("bonding", newBonding) }

func newBonding() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{TracingData: &bondingCollector{}, Flag: tracing.FlagMetric}, nil
}

func (*bondingCollector) Update() ([]*metric.Data, error) {
	paths, err := filepath.Glob(sysfs.Path("class/net/*/bonding/slaves"))
	if err != nil {
		return nil, err
	}
	var metrics []*metric.Data
	var collectErr error
	for _, path := range paths {
		data, err := collectBond(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		metrics = append(metrics, data...)
		if err != nil {
			collectErr = errors.Join(collectErr, fmt.Errorf("collect bond %s: %w", path, err))
		}
	}
	if len(metrics) == 0 && collectErr == nil {
		return nil, metric.ErrNoData
	}
	return metrics, collectErr
}

func collectBond(slavesPath string) ([]*metric.Data, error) {
	raw, err := os.ReadFile(slavesPath)
	if err != nil {
		return nil, err
	}
	slaves := strings.Fields(string(raw))
	bondPath := filepath.Dir(filepath.Dir(slavesPath))
	bond := filepath.Base(bondPath)
	bondLabels := map[string]string{"master": bond}
	metrics := make([]*metric.Data, 0, 2+len(slaves)*2)
	metrics = append(metrics, metric.NewGaugeData("slaves", float64(len(slaves)), "Number of interfaces attached to the bond.", bondLabels))
	up := 0
	complete := true
	var collectErr error
	for _, slave := range slaves {
		slavePath := filepath.Join(bondPath, "lower_"+slave, "bonding_slave")
		labels := map[string]string{"master": bond, "slave": slave}
		status, err := os.ReadFile(filepath.Join(slavePath, "mii_status"))
		if err != nil {
			// A vanished slave is not evidence of link failure; withhold the aggregate.
			complete = false
			if !errors.Is(err, os.ErrNotExist) {
				collectErr = errors.Join(collectErr, fmt.Errorf("read %s link status: %w", slave, err))
			}
			continue
		}
		var linkUp float64
		switch strings.TrimSpace(string(status)) {
		case "up":
			linkUp = 1
			up++
		case "down", "going down", "going back":
		default:
			complete = false
			collectErr = errors.Join(collectErr, fmt.Errorf("unknown bond slave %s link status %q", slave, strings.TrimSpace(string(status))))
			continue
		}
		metrics = append(metrics, metric.NewGaugeData("slave_up", linkUp, "Whether the bond reports the slave link as up.", labels))
		failures, err := parseutil.ReadUint(filepath.Join(slavePath, "link_failure_count"))
		if err == nil {
			metrics = append(metrics, metric.NewCounterData("slave_link_failures_total", float64(failures), "Link failures detected by the bonding driver for the slave.", labels))
		} else if !errors.Is(err, os.ErrNotExist) {
			collectErr = errors.Join(collectErr, fmt.Errorf("read %s link failures: %w", slave, err))
		}
	}
	if complete {
		metrics = append(metrics, metric.NewGaugeData("slaves_up", float64(up), "Number of attached interfaces whose bond link status is up.", bondLabels))
	}
	return metrics, collectErr
}
