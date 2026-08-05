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
	"sync"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	cgroupV2 "github.com/ccfos/huatuo/internal/cgroups/v2"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"

	cadvisorV1 "github.com/google/cadvisor/info/v1"
	"github.com/google/cadvisor/utils/cpuload/netlink"
)

type loadavgCollector struct {
	unsupportedV2 sync.Once
}

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

func (c *loadavgCollector) collectContainerV2(
	collect func() ([]*metric.Data, error),
) ([]*metric.Data, error) {
	loadavgs, err := collect()
	if !errors.Is(err, cgroupV2.ErrTaskIteratorNotSupported) {
		return loadavgs, err
	}

	c.unsupportedV2.Do(func() {
		log.WithError(err).Warn(
			"cgroup v2 container load metrics are unavailable; host load metrics remain enabled")
	})
	return nil, nil
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

	return collectContainerLoadavgV1(
		containers,
		n.GetCpuLoad,
	)
}

func collectContainerLoadavgV1(
	containers map[string]*pod.Container,
	getCpuLoad func(string, string) (cadvisorV1.LoadStats, error),
) ([]*metric.Data, error) {
	loadavgs := []*metric.Data{}
	for _, container := range containers {
		cgroupPath := paths.Path(subsystem.SubsystemCPU, container.CgroupPath)
		stats, err := getCpuLoad(container.Hostname, cgroupPath)
		if err != nil {
			continue
		}

		loadavgs = append(loadavgs, containerLoadMetrics(
			container, stats.NrRunning, stats.NrUninterruptible)...)
	}

	return loadavgs, nil
}

func containerLoadavgV2() ([]*metric.Data, error) {
	containers, err := pod.ContainersByType(pod.ContainerTypeNormal | pod.ContainerTypeSidecar)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(containers))
	for _, container := range containers {
		paths = append(paths, container.CgroupPath)
	}
	statsByPath, err := cgroupV2.SharedLoadStats(
		cgroupV2.LoadStatsConsumerLoadavg, paths)

	loadavgs := []*metric.Data{}
	for _, container := range containers {
		stats, ok := statsByPath[container.CgroupPath]
		if !ok {
			continue
		}

		loadavgs = append(loadavgs, containerLoadMetrics(
			container, stats.NrRunning, stats.NrUninterruptible)...)
	}

	return loadavgs, err
}

func containerLoadMetrics(
	container *pod.Container,
	nrRunning uint64,
	nrUninterruptible uint64,
) []*metric.Data {
	return []*metric.Data{
		metric.NewContainerGaugeData(container,
			"nr_running", float64(nrRunning), "nr_running of container", nil),
		metric.NewContainerGaugeData(container,
			"nr_uninterruptible", float64(nrUninterruptible),
			"nr_uninterruptible of container", nil),
	}
}

func (c *loadavgCollector) Update() ([]*metric.Data, error) {
	return c.update(cgroups.CgroupMode(), containerLoadavg, containerLoadavgV2)
}

func (c *loadavgCollector) update(
	mode cgroups.Mode,
	readV1 func() ([]*metric.Data, error),
	readV2 func() ([]*metric.Data, error),
) ([]*metric.Data, error) {
	var containerLoadavgFn func() ([]*metric.Data, error)
	switch mode {
	case cgroups.Legacy, cgroups.Hybrid:
		containerLoadavgFn = readV1
	case cgroups.Unified:
		containerLoadavgFn = func() ([]*metric.Data, error) {
			return c.collectContainerV2(readV2)
		}
	}
	return collectLoadavg(containerLoadavgFn, nodeLoadMetrics)
}

func nodeLoadMetrics() ([]*metric.Data, error) {
	var loadavgs []*metric.Data
	var errs []error
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

func collectLoadavg(
	containerLoadavgFn func() ([]*metric.Data, error),
	nodeLoadavgFn func() ([]*metric.Data, error),
) ([]*metric.Data, error) {
	var loadavgs []*metric.Data
	var containerErr error
	if containerLoadavgFn != nil {
		containersLoads, err := containerLoadavgFn()
		loadavgs = append(loadavgs, containersLoads...)
		if err != nil {
			containerErr = fmt.Errorf("read container load: %w", err)
		}
	}

	data, nodeErr := nodeLoadavgFn()
	loadavgs = append(loadavgs, data...)

	return loadavgs, errors.Join(containerErr, nodeErr)
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
