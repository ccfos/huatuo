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

type cpuFrequencyCollector struct{}

var cpuFrequencyFields = [...]struct{ file, name, help string }{
	{"scaling_cur_freq", "scaling_current_hertz", "Current CPU frequency reported by the scaling driver in hertz."},
	{"scaling_min_freq", "scaling_minimum_hertz", "Minimum CPU frequency allowed by the policy in hertz."},
	{"scaling_max_freq", "scaling_maximum_hertz", "Maximum CPU frequency allowed by the policy in hertz."},
	{"cpuinfo_min_freq", "hardware_minimum_hertz", "Minimum CPU frequency supported by the hardware in hertz."},
	{"cpuinfo_max_freq", "hardware_maximum_hertz", "Maximum CPU frequency supported by the hardware in hertz."},
	{"bios_limit", "bios_limit_hertz", "CPU frequency limit imposed by firmware in hertz."},
}

func init() { tracing.RegisterEventTracing("cpufreq", newCPUFrequency) }

func newCPUFrequency() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{TracingData: &cpuFrequencyCollector{}, Flag: tracing.FlagMetric}, nil
}

func (*cpuFrequencyCollector) Update() ([]*metric.Data, error) {
	// A policy can be shared by several CPUs. Read it once instead of following
	// every per-CPU symlink, and rediscover policies after CPU hotplug.
	policies, err := filepath.Glob(sysfs.Path("devices/system/cpu/cpufreq/policy[0-9]*"))
	if err != nil {
		return nil, err
	}
	metrics := make([]*metric.Data, 0, len(policies)*(len(cpuFrequencyFields)+1))
	var collectErr error
	for _, path := range policies {
		policy := strings.TrimPrefix(filepath.Base(path), "policy")
		labels := map[string]string{"policy": policy}
		for _, field := range cpuFrequencyFields {
			value, err := parseutil.ReadUint(filepath.Join(path, field.file))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				collectErr = errors.Join(collectErr, fmt.Errorf("read CPU policy %s %s: %w", policy, field.file, err))
				continue
			}
			metrics = append(metrics, metric.NewGaugeData(field.name, float64(value)*1000, field.help, labels))
		}
		governor, governorErr := os.ReadFile(filepath.Join(path, "scaling_governor"))
		driver, driverErr := os.ReadFile(filepath.Join(path, "scaling_driver"))
		for _, err := range []error{governorErr, driverErr} {
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				collectErr = errors.Join(collectErr, fmt.Errorf("read CPU policy %s metadata: %w", policy, err))
			}
		}
		if governorErr == nil && driverErr == nil {
			metrics = append(metrics, metric.NewGaugeData("info", 1, "CPU frequency policy governor and scaling driver.", map[string]string{
				"policy": policy, "governor": strings.TrimSpace(string(governor)), "driver": strings.TrimSpace(string(driver)),
			}))
		}
	}
	if len(metrics) == 0 && collectErr == nil {
		return nil, metric.ErrNoData
	}
	return metrics, collectErr
}
