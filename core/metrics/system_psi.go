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

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

// psiResources are the host pressure-stall resources exposed by the kernel
// under /proc/pressure/. cpu only reports "some" (there is no "all tasks
// stalled" state for cpu); memory and io report both "some" and "full".
var psiResources = []string{"cpu", "memory", "io"}

// systemPSICollector exposes host Pressure Stall Information
// (/proc/pressure/{cpu,memory,io}) as metrics: some/full avg10/avg60/avg300
// gauges and some/full total counters, labeled by resource.
//
// This is the host-only MVP for issue #384: cgroup v2 *.pressure collection,
// the configurable pressure state machine, Grafana panels and alerting are
// deferred to follow-ups.
type systemPSICollector struct {
	fs procfs.FS
}

func init() {
	tracing.RegisterEventTracing("system_psi", newSystemPSI)
}

// newSystemPSI returns a host PSI collector, or types.ErrNotSupported when the
// kernel does not expose /proc/pressure (e.g. PSI disabled at boot) so the
// collector is marked inactive without affecting the other collectors.
func newSystemPSI() (*tracing.EventTracingAttr, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	// Probe one resource: if /proc/pressure is absent the kernel lacks PSI
	// support and this collector should stay inactive.
	if _, err := fs.PSIStatsForResource(psiResources[0]); err != nil {
		return nil, errors.Join(types.ErrNotSupported, err)
	}

	return &tracing.EventTracingAttr{
		TracingData: &systemPSICollector{fs: fs},
		Flag:        tracing.FlagMetric,
	}, nil
}

func (c *systemPSICollector) Update() ([]*metric.Data, error) {
	data := make([]*metric.Data, 0, len(psiResources)*8)

	for _, resource := range psiResources {
		stats, err := c.fs.PSIStatsForResource(resource)
		if err != nil {
			// Degrade per resource: a transient read failure or a missing
			// file must not fail the whole scrape.
			log.Debugf("system_psi: read %s: %v", resource, err)
			continue
		}

		label := map[string]string{"resource": resource}
		for _, s := range psiSamples(resource, stats) {
			if s.counter {
				data = append(data, metric.NewCounterData(s.name, s.value, s.help, label))
			} else {
				data = append(data, metric.NewGaugeData(s.name, s.value, s.help, label))
			}
		}
	}

	return data, nil
}

// psiSample is a single metric derived from a PSIStats line, before it is
// wrapped into a metric.Data. It exists so the mapping is unit-testable
// without going through the metric package's private fields.
type psiSample struct {
	name    string
	value   float64
	help    string
	counter bool
}

// psiSamples maps a parsed PSIStats for one resource into the metric samples
// to emit. "some" is always present; "full" is only present for memory and io
// (nil for cpu). Each line yields three avg gauges and one total counter.
func psiSamples(resource string, stats procfs.PSIStats) []psiSample {
	var samples []psiSample

	if stats.Some != nil {
		samples = append(samples,
			psiSample{"psi_some_avg10", stats.Some.Avg10, "PSI some avg10 stall fraction (percent) for " + resource, false},
			psiSample{"psi_some_avg60", stats.Some.Avg60, "PSI some avg60 stall fraction (percent) for " + resource, false},
			psiSample{"psi_some_avg300", stats.Some.Avg300, "PSI some avg300 stall fraction (percent) for " + resource, false},
			psiSample{"psi_some_total", float64(stats.Some.Total), "PSI some total stall time (microseconds) for " + resource, true},
		)
	}

	if stats.Full != nil {
		samples = append(samples,
			psiSample{"psi_full_avg10", stats.Full.Avg10, "PSI full avg10 stall fraction (percent) for " + resource, false},
			psiSample{"psi_full_avg60", stats.Full.Avg60, "PSI full avg60 stall fraction (percent) for " + resource, false},
			psiSample{"psi_full_avg300", stats.Full.Avg300, "PSI full avg300 stall fraction (percent) for " + resource, false},
			psiSample{"psi_full_total", float64(stats.Full.Total), "PSI full total stall time (microseconds) for " + resource, true},
		)
	}

	return samples
}
