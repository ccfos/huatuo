// Copyright 2025, 2026 The HuaTuo Authors
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
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/google/cadvisor/utils/cpuload/netlink"
)

type loadavgCollector struct{}

func init() {
	tracing.RegisterEventTracing("loadavg", newLoadavg)
}

// newLoadavg returns a new Collector exposing load average stats.
func newLoadavg() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &loadavgCollector{},
		Flag:        tracing.FlagMetric,
	}, nil
}

// Load average of last 1, 5, 15 minutes.
// See linux kernel Documentation/filesystems/proc.rst
func nodeLoadAvg() ([]*metric.Data, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, err
	}

	load, err := fs.LoadAvg()
	if err != nil {
		return nil, err
	}

	return []*metric.Data{
		metric.NewGaugeData("load1", load.Load1, "system load average, 1 minute", nil),
		metric.NewGaugeData("load5", load.Load5, "system load average, 5 minutes", nil),
		metric.NewGaugeData("load15", load.Load15, "system load average, 15 minutes", nil),
	}, nil
}

func containerLoadavg() ([]*metric.Data, error) {
	n, err := netlink.New()
	if err != nil {
		return nil, err
	}
	defer n.Stop()

	containers, err := pod.ContainersByType(pod.ContainerTypeNormal | pod.ContainerTypeSidecar)
	if err != nil {
		return nil, err
	}

	loadavgs := []*metric.Data{}
	for _, container := range containers {
		stats, err := n.GetCpuLoad(container.Hostname, paths.Path(subsystem.SubsystemCPU, container.CgroupPath))
		if err != nil {
			continue
		}

		loadavgs = append(loadavgs,
			metric.NewContainerGaugeData(container,
				"nr_running", float64(stats.NrRunning), "nr_running of container", nil),
			metric.NewContainerGaugeData(container,
				"nr_uninterruptible", float64(stats.NrUninterruptible), "nr_uninterruptible of container", nil))
	}

	return loadavgs, nil
}

func (c *loadavgCollector) Update() ([]*metric.Data, error) {
	return c.update(cgroups.CgroupMode(), containerLoadavg)
}

func (c *loadavgCollector) update(mode cgroups.Mode, readContainers func() ([]*metric.Data, error)) ([]*metric.Data, error) {
	var loadavgs []*metric.Data
	var errs []error

	if mode == cgroups.Legacy {
		containersLoads, err := readContainers()
		loadavgs = append(loadavgs, containersLoads...)
		if err != nil {
			errs = append(errs, fmt.Errorf("read container load: %w", err))
		}
	}

	data, err := nodeLoadAvg()
	loadavgs = append(loadavgs, data...)
	if err != nil {
		errs = append(errs, fmt.Errorf("read host load average: %w", err))
	}

	raw, err := os.ReadFile(procfs.Path("stat"))
	if err == nil {
		var running uint64
		running, err = parseHostRunnable(raw)
		if err == nil {
			loadavgs = append(loadavgs, metric.NewGaugeData("nr_running", float64(running),
				"number of running or runnable host tasks", nil))
		}
	}
	if err != nil {
		errs = append(errs, fmt.Errorf("read host runnable tasks: %w", err))
	}
	return loadavgs, errors.Join(errs...)
}

func parseHostRunnable(raw []byte) (uint64, error) {
	// Parse only this field: unrelated CPU/IRQ columns can be very large.
	// A missing field must not become a synthetic zero.
	for len(raw) > 0 {
		var line []byte
		line, raw, _ = bytes.Cut(raw, []byte{'\n'})
		if !bytes.HasPrefix(line, []byte("procs_running")) {
			continue
		}
		fields := bytes.Fields(line)
		if len(fields) == 0 || !bytes.Equal(fields[0], []byte("procs_running")) {
			continue
		}
		if len(fields) != 2 {
			return 0, fmt.Errorf("invalid procs_running field: %q", line)
		}
		value, err := strconv.ParseUint(string(fields[1]), 10, 64)
		if err != nil {
			return 0, err
		}
		return value, nil
	}
	return 0, errors.New("procs_running missing from proc stat")
}
