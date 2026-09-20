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
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"sync"
	"time"

	cadvisorV1 "github.com/google/cadvisor/info/v1"
	"github.com/google/cadvisor/utils/cpuload/netlink"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	cgroupV2 "github.com/ccfos/huatuo/internal/cgroups/v2"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/ccfos/huatuo/internal/cgroups/stats"
)

type loadavgCollector struct {
	sampleInterval  time.Duration
	unsupportedHost sync.Once
	unsupportedV2   sync.Once
	mu              sync.Mutex
	sampling        bool
	sampledAt       time.Time
	sampledData     []*metric.Data
	sampledErr      error
	averages        map[containerLoadKey]containerLoadAverage
}

func init() {
	tracing.RegisterEventTracing("loadavg", newLoadavg)
}

// newLoadavg returns a new Collector exposing load average stats.
func newLoadavg() (*tracing.EventTracingAttr, error) {
	cfg := configSnapshot()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &tracing.EventTracingAttr{
		TracingData: &loadavgCollector{
			sampleInterval: time.Duration(cfg.Loadavg.Interval) * time.Second,
		},
		Interval: 5,
		Flag:     tracing.FlagMetric | tracing.FlagTracing,
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

func readContainerLoadV1() ([]containerLoadSample, error) {
	return readContainerLoadV1WithDiscovery(pod.NormalSidecarContainers)
}

func readContainerLoadV1WithDiscovery(discover func() (map[string]*pod.Container, error)) ([]containerLoadSample, error) {
	containers, err := discover()
	if err != nil || len(containers) == 0 {
		return nil, err
	}

	n, err := netlink.New()
	if err != nil {
		return nil, err
	}
	defer n.Stop()

	return readContainerLoadSamplesV1(
		containers,
		n.GetCpuLoad,
	)
}

func readContainerLoadSamplesV1(
	containers map[string]*pod.Container,
	getCpuLoad func(string, string) (cadvisorV1.LoadStats, error),
) ([]containerLoadSample, error) {
	samples := make([]containerLoadSample, 0, len(containers))
	for _, container := range containers {
		cgroupPath := paths.Path(subsystem.SubsystemCPU, container.CgroupPath)
		stats, err := getCpuLoad(container.Hostname, cgroupPath)
		if err != nil {
			continue
		}

		samples = append(samples, containerLoadSample{container, stats.NrRunning, stats.NrUninterruptible})
	}

	return samples, nil
}

func readContainerLoadV2() ([]containerLoadSample, error) {
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

	samples := make([]containerLoadSample, 0, len(containers))
	for _, container := range containers {
		stats, ok := statsByPath[container.CgroupPath]
		if !ok {
			continue
		}

		samples = append(samples, containerLoadSample{container, stats.NrRunning, stats.NrUninterruptible})
	}

	return samples, err
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
	data, err, sampling := c.cachedContainerLoad(time.Now())
	if sampling {
		return collectLoadavg(func() ([]*metric.Data, error) { return data, err }, nodeLoadMetrics)
	}
	return c.update(cgroups.CgroupMode(), readContainerLoadV1, readContainerLoadV2)
}

func (c *loadavgCollector) update(
	mode cgroups.Mode,
	readV1, readV2 func() ([]containerLoadSample, error),
) ([]*metric.Data, error) {
	return collectLoadavg(func() ([]*metric.Data, error) {
		samples, err := c.readContainerLoad(mode, readV1, readV2)
		return instantaneousContainerLoad(samples), err
	}, nodeLoadMetrics)
}

// Scrape fallback and background sampling share the same readers and policy.
func (c *loadavgCollector) readContainerLoad(
	mode cgroups.Mode,
	readV1, readV2 func() ([]containerLoadSample, error),
) ([]containerLoadSample, error) {
	switch mode {
	case cgroups.Legacy, cgroups.Hybrid:
		return readV1()
	case cgroups.Unified:
		samples, err := readV2()
		if errors.Is(err, cgroupV2.ErrTaskIteratorNotSupported) {
			c.unsupportedV2.Do(func() {
				log.WithError(err).Warn(
					"cgroup v2 container load metrics are unavailable; host load metrics remain enabled")
			})
			return nil, nil
		}
		return samples, err
	}
	return nil, nil
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

const defaultLoadSampleInterval = 15 * time.Second

func (c *loadavgCollector) samplingInterval() time.Duration {
	if c.sampleInterval == 0 {
		return defaultLoadSampleInterval
	}
	return c.sampleInterval
}

type containerLoadSample struct {
	container                *pod.Container
	running, uninterruptible uint64
}

// A new container or a reused cgroup must not inherit another task set's EMA.
type containerLoadKey struct {
	ID, Path  string
	StartedAt time.Time
}

type containerLoadAverage struct {
	last   time.Time
	values [3]float64
}

func instantaneousContainerLoad(samples []containerLoadSample) []*metric.Data {
	data := make([]*metric.Data, 0, len(samples)*2)
	for _, sample := range samples {
		data = append(data, containerLoadMetrics(sample.container, sample.running, sample.uninterruptible)...)
	}
	return data
}

// Start samples independently of Prometheus scrapes and dload profiling.
// The tracing manager owns cancellation and restart; no detached goroutine lives
// beyond the collector lifecycle.
func (c *loadavgCollector) Start(ctx context.Context) error {
	return c.sampleLoad(ctx, func() ([]containerLoadSample, *stats.LoadStats, error) {
		return c.readLoadSample(cgroups.CgroupMode(), readContainerLoadV1, readTaskLoadWithHost)
	})
}

func (c *loadavgCollector) sampleLoad(ctx context.Context, read func() ([]containerLoadSample, *stats.LoadStats, error)) error {
	c.mu.Lock()
	c.sampling = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.sampling = false
		c.sampledData, c.sampledErr, c.averages = nil, nil, nil
		c.sampledAt = time.Time{}
		c.mu.Unlock()
		cgroupV2.ForgetSharedLoadStatsConsumer(cgroupV2.LoadStatsConsumerLoadavg)
	}()
	ticker := time.NewTicker(c.samplingInterval())
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		at := time.Now()
		samples, host, err := read()
		c.publishContainerLoad(at, samples, err, host)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (c *loadavgCollector) publishContainerLoad(at time.Time, samples []containerLoadSample, err error, host *stats.LoadStats) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data := instantaneousContainerLoad(samples)
	if host != nil {
		data = append(data, metric.NewGaugeData("nr_uninterruptible", float64(host.NrUninterruptible),
			"number of host uninterruptible tasks contributing to load", nil))
	}
	var next map[containerLoadKey]containerLoadAverage
	if len(samples) > 0 {
		next = make(map[containerLoadKey]containerLoadAverage, len(samples))
	}
	for _, sample := range samples {
		container := sample.container
		key := containerLoadKey{ID: container.ID, Path: container.CgroupPath, StartedAt: container.StartedAt}
		average := c.averages[key]
		dt := at.Sub(average.last)
		if average.last.IsZero() || dt <= 0 || dt > 3*c.samplingInterval() {
			// Establish a baseline, then warm up from zero over observed time.
			// Never extrapolate across a missing sample or a long suspension.
			average = containerLoadAverage{last: at}
		} else {
			active := float64(sample.running) + float64(sample.uninterruptible)
			for i, window := range [...]float64{60, 300, 900} {
				weight := -math.Expm1(-dt.Seconds() / window)
				average.values[i] += (active - average.values[i]) * weight
				name := [...]string{"load1", "load5", "load15"}[i]
				data = append(data, metric.NewContainerGaugeData(container, name, average.values[i],
					"estimated container R+D load average, "+name, nil))
			}
			average.last = at
		}
		next[key] = average
	}
	// Missing/failed containers are omitted and pruned, never sampled as zero.
	c.averages, c.sampledData, c.sampledErr, c.sampledAt = next, data, err, at
}

func (c *loadavgCollector) cachedContainerLoad(now time.Time) ([]*metric.Data, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.sampling {
		return nil, nil, false
	}
	if c.sampledAt.IsZero() {
		return nil, nil, true
	}
	if now.Sub(c.sampledAt) > 3*c.samplingInterval() {
		return nil, errors.New("container load sample expired; check loadavg sampler"), true
	}
	return c.sampledData, c.sampledErr, true
}

func (c *loadavgCollector) readLoadSample(
	mode cgroups.Mode,
	readV1 func() ([]containerLoadSample, error),
	readWithHost func(bool) ([]containerLoadSample, *stats.LoadStats, error),
) ([]containerLoadSample, *stats.LoadStats, error) {
	includeContainers := mode == cgroups.Unified
	samples, host, err := readWithHost(includeContainers)
	if errors.Is(err, cgroupV2.ErrTaskIteratorNotSupported) {
		c.unsupportedHost.Do(func() {
			log.WithError(err).Warn("BPF task load metrics unavailable; procfs host load and v1 container load remain enabled")
		})
		samples, host, err = nil, nil, nil
	}
	if mode == cgroups.Legacy || mode == cgroups.Hybrid {
		var containerErr error
		samples, containerErr = readV1()
		err = errors.Join(err, containerErr)
	}
	return samples, host, err
}

func readTaskLoadWithHost(includeContainers bool) ([]containerLoadSample, *stats.LoadStats, error) {
	var containers map[string]*pod.Container
	var containerErr error
	if includeContainers {
		containers, containerErr = pod.ContainersByType(pod.ContainerTypeNormal | pod.ContainerTypeSidecar)
	}
	paths := make([]string, 0, len(containers))
	for _, container := range containers {
		paths = append(paths, container.CgroupPath)
	}
	byPath, host, err := cgroupV2.SharedLoadStatsWithHost(cgroupV2.LoadStatsConsumerLoadavg, paths)
	samples := make([]containerLoadSample, 0, len(containers))
	for _, container := range containers {
		if load, ok := byPath[container.CgroupPath]; ok {
			samples = append(samples, containerLoadSample{container, load.NrRunning, load.NrUninterruptible})
		}
	}
	return samples, host, errors.Join(containerErr, err)
}
