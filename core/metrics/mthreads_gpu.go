// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Mthreads Authors
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
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"huatuo-bamai/core/metrics/mthreads/mtml"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"
)

func init() {
	tracing.RegisterEventTracing("mthreads_gpu", newMthreadsGpuCollector)
}

// scrapeTimeout bounds the wall-clock time of a single Update() call per
// metric group and device. Since MTML is a synchronous, non-cancelable C
// library, ctx.Done() is checked only between devices and before each metric
// group (health / PCIe / MtLink); once exceeded, remaining devices and groups
// are skipped. If a C call hangs, Update() blocks until it returns — canceling
// in-flight calls would require an async MTML API.
const scrapeTimeout = 10 * time.Second

// mthreadsGpuCollector monitors Moore Threads GPUs via the MTML library.
// Static device info is cached and only rebuilt on re-init; dynamic metrics
// are collected on every scrape.
type mthreadsGpuCollector struct {
	cache atomic.Pointer[mthreadsCache]
}

// mthreadsCache holds the device list plus cached static info metrics. It is
// rebuilt when the cache is invalidated (first use, or after a re-init).
type mthreadsCache struct {
	devices    []uint32       // device indices
	staticInfo []*metric.Data // cached info metrics (library/driver/device/spec/bios/musa/memory_info)
}

func newMthreadsGpuCollector() (*tracing.EventTracingAttr, error) {
	if err := mtml.Init(); err != nil {
		log.Warnf("mthreads: GPU collector disabled (mtml init failed: %v)", err)
		return nil, types.ErrNotSupported
	}

	c := &mthreadsGpuCollector{} // cache built lazily on first Update
	return &tracing.EventTracingAttr{
		TracingData: c,
		Flag:        tracing.FlagMetric,
	}, nil
}

// Update is called by the framework on every Prometheus scrape.
func (c *mthreadsGpuCollector) Update() ([]*metric.Data, error) {
	ctx, cancel := context.WithTimeout(context.Background(), scrapeTimeout)
	defer cancel()

	return c.collect(ctx)
}

func (c *mthreadsGpuCollector) collect(ctx context.Context) ([]*metric.Data, error) {
	cfg := configSnapshot()
	cache, err := c.getOrBuildCache(ctx)
	if err != nil {
		return nil, fmt.Errorf("build mthreads cache: %w", err)
	}
	if len(cache.devices) == 0 {
		return nil, fmt.Errorf("no mthreads gpu devices found")
	}

	// Static info comes from cache (no per-scrape MTML calls).
	metrics := append([]*metric.Data{}, cache.staticInfo...)

	var allErrs []error
	for _, gpu := range cache.devices {
		if ctx.Err() != nil {
			log.Debugf("mthreads: scrape timeout exceeded before device %d, stopping collection", gpu)
			break
		}
		m, err := collectMthreadsDeviceMetrics(ctx, cfg, gpu)
		metrics = append(metrics, m...)
		if err != nil {
			allErrs = append(allErrs, err)
		}
	}

	return metrics, errors.Join(allErrs...)
}

// getOrBuildCache returns the current cache, building it if missing. A build
// failure is returned as an error rather than a nil cache
func (c *mthreadsGpuCollector) getOrBuildCache(ctx context.Context) (*mthreadsCache, error) {
	if cache := c.cache.Load(); cache != nil {
		return cache, nil
	}
	return c.buildCache(ctx)
}

// buildCache enumerates devices and collects static info, storing the result
// atomically and returning it.
func (c *mthreadsGpuCollector) buildCache(ctx context.Context) (*mthreadsCache, error) {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("scrape timeout exceeded before building cache: %w", ctx.Err())
	}

	count, err := mtml.CountDevice()
	if err != nil {
		return nil, fmt.Errorf("count mthreads devices: %w", err)
	}

	cache := &mthreadsCache{}
	for i := uint32(0); i < count; i++ {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("scrape timeout exceeded while enumerating devices: %w", ctx.Err())
		}
		cache.devices = append(cache.devices, i)
	}

	// Library/driver version: device-independent, emit once.
	cache.staticInfo = collectMthreadsVersions(ctx)

	// we do NOT store a partial cache.
	for _, gpu := range cache.devices {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("scrape timeout before device %d static info: %w", gpu, ctx.Err())
		}
		m := collectMthreadsDeviceInfo(ctx, gpu)
		if m == nil {
			return nil, fmt.Errorf("gpu %d static info collection failed; cache not stored, will retry on next build", gpu)
		}
		cache.staticInfo = append(cache.staticInfo, m...)
	}

	c.cache.Store(cache)
	return cache, nil
}

// collectMthreadsVersions emits the library and driver version info metrics.
func collectMthreadsVersions(ctx context.Context) []*metric.Data {
	var out []*metric.Data

	if ctx.Err() != nil {
		log.Debug("mthreads: scrape timeout exceeded before collecting library version, skipping")
		return nil
	}

	if v, err := mtml.LibraryVersion(); err == nil {
		out = append(out, metric.NewGaugeData("library_info", 1,
			"MTML library info.", map[string]string{"version": v}))
	}
	return out
}

// collectMthreadsDeviceInfo emits the per-device static info metrics (cached).
func collectMthreadsDeviceInfo(ctx context.Context, gpu uint32) []*metric.Data {
	var out []*metric.Data

	if ctx.Err() != nil {
		log.Debugf("mthreads: scrape timeout exceeded before device %d static info, skipping", gpu)
		return nil
	}

	dev, err := mtml.InitDeviceByIndex(gpu)
	if err != nil {
		log.Debugf("mthreads: init device %d for static info failed: %v", gpu, err)
		return nil
	}
	defer dev.Close()

	gpuLabel := strconv.Itoa(int(gpu))

	// device_info: name/brand/serial
	name, _ := dev.Name()
	brandStr := "MTT"
	if b, err := dev.Brand(); err == nil && b != 0 {
		brandStr = strconv.Itoa(int(b))
	}
	serial, _ := dev.SerialNumber()
	out = append(out, metric.NewGaugeData("device_info", 1, "GPU info.", map[string]string{
		"gpu":    gpuLabel,
		"name":   name,
		"brand":  brandStr,
		"serial": serial,
	}))

	// pci_info
	if sbdf, err := dev.PciSbdf(); err == nil {
		out = append(out, metric.NewGaugeData("device_pci_info", 1, "GPU PCI info.", map[string]string{
			"gpu":  gpuLabel,
			"sbdf": sbdf,
		}))
	}

	// device_spec: cores
	if cores, err := dev.GpuCores(); err == nil {
		out = append(out, metric.NewGaugeData("device_spec", 1, "GPU spec.", map[string]string{
			"gpu":   gpuLabel,
			"cores": strconv.Itoa(int(cores)),
		}))
	}

	// bios version
	if bios, err := dev.BiosVersion(); err == nil {
		out = append(out, metric.NewGaugeData("device_bios_version", 1, "GPU BIOS version.", map[string]string{
			"gpu":  gpuLabel,
			"bios": bios,
		}))
	}

	// musa compute capability
	if maj, min, err := dev.MusaComputeCapability(); err == nil {
		out = append(out, metric.NewGaugeData("device_musa_capability", 1, "GPU MUSA compute capability.", map[string]string{
			"gpu":             gpuLabel,
			"musa_capability": fmt.Sprintf("%d.%d", maj, min),
		}))
	}

	// memory info (static: type/vendor/bus_width)
	if mem, err := dev.InitMemory(); err == nil {
		labels := map[string]string{"gpu": gpuLabel}
		if t, err := mem.MemoryType(); err == nil {
			labels["type"] = memoryTypeName(t)
		}
		if v, err := mem.Vendor(); err == nil {
			labels["vendor"] = v
		}
		if w, err := mem.BusWidth(); err == nil {
			labels["bus_width"] = strconv.Itoa(int(w))
		}
		if len(labels) > 1 {
			out = append(out, metric.NewGaugeData("memory_info", 1, "GPU memory info.", labels))
		}
		mem.Close()
	}

	return out
}

// collectMthreadsDeviceMetrics collects dynamic metrics for a single device.
// It opens the device once and dispatches to per-category collectors (health /
// PCIe / MtLink), each of which manages its own sub-handles.
func collectMthreadsDeviceMetrics(ctx context.Context, cfg *Config, gpu uint32) ([]*metric.Data, error) {
	dev, err := mtml.InitDeviceByIndex(gpu)
	if err != nil {
		if mtml.IsNotSupported(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("gpu %d init device: %w", gpu, err)
	}
	defer dev.Close()

	var out []*metric.Data
	var errs []error

	record := func(name string, err error) {
		if err == nil || mtml.IsNotSupported(err) {
			return
		}
		errs = append(errs, fmt.Errorf("gpu %d %s: %w", gpu, name, err))
	}

	if cfg.Mthreads.EnableHealth {
		if ctx.Err() != nil {
			record("health ctx", ctx.Err())
		} else {
			m, err := collectMthreadsHealth(ctx, dev, gpu)
			out = append(out, m...)
			if err != nil {
				errs = append(errs, fmt.Errorf("health: %w", err))
			}
		}
	}

	if cfg.Mthreads.EnablePCIe {
		if ctx.Err() != nil {
			record("pcie ctx", ctx.Err())
		} else {
			m, err := collectMthreadsPCIe(ctx, dev, gpu)
			out = append(out, m...)
			if err != nil {
				errs = append(errs, fmt.Errorf("pcie: %w", err))
			}
		}
	}

	if cfg.Mthreads.EnableMTLink {
		if ctx.Err() != nil {
			record("mtlink ctx", ctx.Err())
		} else {
			m, err := collectMthreadsMtLink(ctx, dev, gpu)
			out = append(out, m...)
			if err != nil {
				errs = append(errs, fmt.Errorf("mtlink: %w", err))
			}
		}
	}

	return out, errors.Join(errs...)
}

// collectMthreadsHealth collects health metrics (temperature/power/utilization/
// clocks/fans/pstate/vpu) for one device. It inits the gpu/memory/vpu handles
// once and reuses them across all reads.
func collectMthreadsHealth(ctx context.Context, dev *mtml.Device, gpu uint32) ([]*metric.Data, error) {
	gpuLabel := strconv.Itoa(int(gpu))
	var out []*metric.Data
	var errs []error

	record := func(name string, err error) {
		if err == nil || mtml.IsNotSupported(err) {
			return
		}
		errs = append(errs, fmt.Errorf("gpu %d %s: %w", gpu, name, err))
	}

	if ctx.Err() != nil {
		log.Debugf("mthreads: scrape timeout exceeded before health group on device %d, skipping", gpu)
		return nil, ctx.Err()
	}

	g, gErr := dev.InitGpu()
	mem, memErr := dev.InitMemory()
	vpu, vpuErr := dev.InitVpu()
	record("init gpu", gErr)
	record("init memory", memErr)
	record("init vpu", vpuErr)
	defer func() {
		if gErr == nil {
			g.Close()
		}
		if memErr == nil {
			mem.Close()
		}
		if vpuErr == nil {
			vpu.Close()
		}
	}()

	// Temperature
	if gErr == nil {
		t, err := g.Temperature()
		if err == nil {
			out = append(out, metric.NewGaugeData("gpu_temperature_celsius", float64(t),
				"GPU temperature in degrees Celsius.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("gpu temperature", err)
		}
	}
	if memErr == nil {
		t, err := mem.Temperature()
		if err == nil {
			out = append(out, metric.NewGaugeData("memory_temperature_celsius", float64(t),
				"GPU memory temperature in degrees Celsius.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("memory temperature", err)
		}
	}

	// Power & voltage
	{
		pw, err := dev.PowerUsage()
		if err == nil {
			out = append(out, metric.NewGaugeData("device_power_watts", float64(pw)/1000,
				"GPU device power in watts.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("power usage", err)
		}
	}
	if gErr == nil {
		if v, err := g.Voltage(); err == nil {
			out = append(out, metric.NewGaugeData("gpu_voltage_volts", float64(v)/1000,
				"GPU voltage in volts.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("gpu voltage", err)
		}
		if l, err := g.EnforcedPowerLimit(); err == nil {
			out = append(out, metric.NewGaugeData("gpu_power_limit_watts", float64(l)/1000,
				"GPU enforced power limit in watts.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("gpu power limit", err)
		}
		if l, err := g.PowerManagementDefaultLimit(); err == nil {
			out = append(out, metric.NewGaugeData("gpu_power_default_limit_watts", float64(l)/1000,
				"GPU default power management limit in watts.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("gpu default power limit", err)
		}
	}

	// Utilization
	if gErr == nil {
		if u, err := g.Utilization(); err == nil {
			out = append(out, metric.NewGaugeData("gpu_utilization_percent", float64(u),
				"GPU utilization (0-100).", map[string]string{"gpu": gpuLabel}))
		} else {
			record("gpu utilization", err)
		}
	}
	if memErr == nil {
		if u, err := mem.Utilization(); err == nil {
			out = append(out, metric.NewGaugeData("memory_utilization_percent", float64(u),
				"GPU memory utilization (0-100).", map[string]string{"gpu": gpuLabel}))
		} else {
			record("memory utilization", err)
		}
		if total, err := mem.Total(); err == nil {
			out = append(out, metric.NewGaugeData("memory_total_bytes", float64(total),
				"Total GPU memory in bytes.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("memory total", err)
		}
		if used, err := mem.Used(); err == nil {
			out = append(out, metric.NewGaugeData("memory_used_bytes", float64(used),
				"Used GPU memory in bytes.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("memory used", err)
		}
	}

	// Clocks
	if gErr == nil {
		if c, err := g.Clock(); err == nil {
			out = append(out, metric.NewGaugeData("gpu_clock_mhz", float64(c),
				"GPU clock in MHz.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("gpu clock", err)
		}
		if c, err := g.MaxClock(); err == nil {
			out = append(out, metric.NewGaugeData("gpu_max_clock_mhz", float64(c),
				"GPU max clock in MHz.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("gpu max clock", err)
		}
	}
	if memErr == nil {
		if c, err := mem.Clock(); err == nil {
			out = append(out, metric.NewGaugeData("memory_clock_mhz", float64(c),
				"GPU memory clock in MHz.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("memory clock", err)
		}
		if c, err := mem.MaxClock(); err == nil {
			out = append(out, metric.NewGaugeData("memory_max_clock_mhz", float64(c),
				"GPU memory max clock in MHz.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("memory max clock", err)
		}
	}

	// Fans
	if fanCount, err := dev.FanCount(); err == nil && fanCount > 0 {
		for i := uint32(0); i < fanCount; i++ {
			if rpm, err := dev.FanRpm(i); err == nil {
				out = append(out, metric.NewGaugeData("fan_rpm", float64(rpm),
					"GPU fan speed in RPM.", map[string]string{"gpu": gpuLabel, "fan": strconv.Itoa(int(i))}))
			} else {
				record(fmt.Sprintf("fan %d rpm", i), err)
			}
			if sp, err := dev.FanSpeed(i); err == nil {
				out = append(out, metric.NewGaugeData("fan_speed_percent", float64(sp),
					"GPU fan speed percent.", map[string]string{"gpu": gpuLabel, "fan": strconv.Itoa(int(i))}))
			} else {
				record(fmt.Sprintf("fan %d speed", i), err)
			}
		}
	} else if err != nil {
		record("fan count", err)
	}

	// pstate
	if ps, err := dev.PerformanceState(); err == nil {
		out = append(out, metric.NewGaugeData("gpu_pstate", float64(ps),
			"GPU performance state (0=P0).", map[string]string{"gpu": gpuLabel}))
	} else {
		record("gpu pstate", err)
	}

	// VPU
	if vpuErr == nil {
		if util, err := vpu.Utilization(); err == nil {
			out = append(out, metric.NewGaugeData("vpu_utilization_percent", float64(util.Util),
				"GPU VPU utilization (0-100).", map[string]string{"gpu": gpuLabel}))
			out = append(out, metric.NewGaugeData("vpu_encoder_utilization_percent", float64(util.EncUtil),
				"GPU VPU encoder utilization (0-100).", map[string]string{"gpu": gpuLabel}))
			out = append(out, metric.NewGaugeData("vpu_decoder_utilization_percent", float64(util.DecUtil),
				"GPU VPU decoder utilization (0-100).", map[string]string{"gpu": gpuLabel}))
		} else {
			record("vpu utilization", err)
		}
		if c, err := vpu.Clock(); err == nil {
			out = append(out, metric.NewGaugeData("vpu_clock_mhz", float64(c),
				"GPU VPU clock in MHz.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("vpu clock", err)
		}
	}

	return out, errors.Join(errs...)
}

// collectMthreadsPCIe collects dynamic PCIe link metrics (cur / max
// speed/width + replay counter) for one device.
func collectMthreadsPCIe(ctx context.Context, dev *mtml.Device, gpu uint32) ([]*metric.Data, error) {
	gpuLabel := strconv.Itoa(int(gpu))
	var out []*metric.Data
	var errs []error

	record := func(name string, err error) {
		if err == nil || mtml.IsNotSupported(err) {
			return
		}
		errs = append(errs, fmt.Errorf("gpu %d %s: %w", gpu, name, err))
	}

	if ctx.Err() != nil {
		log.Debugf("mthreads: scrape timeout exceeded before PCIe group on device %d, skipping", gpu)
		return nil, ctx.Err()
	}

	if info, err := dev.PciInfo(); err == nil {
		out = append(out,
			metric.NewGaugeData("pcie_link_speed_gt_per_sec", info.CurSpeed,
				"GPU PCIe current link speed in GT/s.", map[string]string{"gpu": gpuLabel}),
			metric.NewGaugeData("pcie_link_width_lanes", float64(info.CurWidth),
				"GPU PCIe current link width in lanes.", map[string]string{"gpu": gpuLabel}),
			metric.NewGaugeData("pcie_link_max_speed_gt_per_sec", info.MaxSpeed,
				"GPU PCIe max link speed in GT/s (hardware capability).", map[string]string{"gpu": gpuLabel}),
			metric.NewGaugeData("pcie_link_max_width_lanes", float64(info.MaxWidth),
				"GPU PCIe max link width in lanes (hardware capability).", map[string]string{"gpu": gpuLabel}),
		)
	} else {
		record("pci info", err)
	}
	if rc, err := dev.PcieReplayCounter(); err == nil {
		out = append(out, metric.NewCounterData("pcie_replay_total", float64(rc),
			"GPU PCIe replay counter.", map[string]string{"gpu": gpuLabel}))
	} else {
		record("pcie replay counter", err)
	}

	return out, errors.Join(errs...)
}

// collectMthreadsMtLink collects MtLink interconnect metrics (per-link state
// and bandwidth) for one device.
func collectMthreadsMtLink(ctx context.Context, dev *mtml.Device, gpu uint32) ([]*metric.Data, error) {
	gpuLabel := strconv.Itoa(int(gpu))
	var out []*metric.Data
	var errs []error

	record := func(name string, err error) {
		if err == nil || mtml.IsNotSupported(err) {
			return
		}
		errs = append(errs, fmt.Errorf("gpu %d %s: %w", gpu, name, err))
	}

	if ctx.Err() != nil {
		log.Debugf("mthreads: scrape timeout exceeded before MtLink group on device %d, skipping", gpu)
		return nil, ctx.Err()
	}

	spec, err := dev.MtLinkSpec()
	if err != nil {
		record("mtlink spec", err)
		return out, errors.Join(errs...)
	}
	for link := uint32(0); link < spec.LinkNum; link++ {
		linkLabel := strconv.Itoa(int(link))
		if st, err := dev.MtLinkState(link); err == nil {
			out = append(out, metric.NewGaugeData("mtlink_state", float64(st),
				"GPU MtLink state (0=DOWN,1=UP,2=DOWNGRADE).",
				map[string]string{"gpu": gpuLabel, "link": linkLabel}))
		} else {
			record(fmt.Sprintf("mtlink %d state", link), err)
		}
		out = append(out, metric.NewGaugeData("mtlink_bandwidth_gbps", float64(spec.BandWidth),
			"GPU MtLink bandwidth in GB/s.",
			map[string]string{"gpu": gpuLabel, "link": linkLabel}))
	}

	return out, errors.Join(errs...)
}

func memoryTypeName(t uint32) string {
	switch t {
	case mtml.MemoryTypeLPDDR4:
		return "LPDDR4"
	case mtml.MemoryTypeGDDR6:
		return "GDDR6"
	}
	return "unknown"
}
