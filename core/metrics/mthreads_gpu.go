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
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ccfos/huatuo/core/metrics/mthreads/mtml"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"
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
//
// Concurrency model:
//   - The framework serializes Update() calls via CollectorWrapper.mu
//     (pkg/metric/collector.go), so there is at most one in-flight
//     collect() per collector. The lock-free cache fast path below is
//     safe under that contract: invalidateCache is only called from
//     inside Update() (the recoverable-error re-init path), so a stale
//     "built=true" read cannot outlive a concurrent invalidator — both
//     run on the same goroutine.
//   - If a future caller invokes collect() or invalidateCache() outside
//     of Update(), the fast path must be revisited (see H-07).
type mthreadsGpuCollector struct {
	cache     atomic.Pointer[mthreadsCache] // immutable once stored; readers must not mutate
	build     sync.Mutex                    // serializes concurrent buildCache callers
	poisoned  atomic.Bool                   // once true, Update() short-circuits and never touches collect/Shutdown/Init again
	poisonErr atomic.Pointer[error]         // root cause; errors.Is chain is preserved
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
	// Poison short-circuit: once Shutdown has failed, this collector is
	// permanently abandoned. The contaminated path is physically unreachable
	// — no collect, Shutdown, or Init is ever attempted again.
	if c.poisoned.Load() {
		return nil, c.poisonedErr()
	}

	ctx, cancel := context.WithTimeout(context.Background(), scrapeTimeout)
	defer cancel()

	metrics, err := c.collect(ctx)
	if err == nil || !mtml.IsRecoverable(err) {
		return metrics, err
	}

	// Recoverable MTML error: try exactly one Shutdown + Init + cache
	// invalidate + retry cycle.
	log.Warnf("mthreads: recoverable error, re-initing MTML: %v", err)
	if serr := mtml.Shutdown(); serr != nil {
		// Shutdown failed -> handle is frozen, permanently poisoned.
		c.setPoisoned(serr)
		return nil, fmt.Errorf("recoverable error %w; shutdown failed: %w", err, serr)
	}
	if ierr := mtml.Init(); ierr != nil {
		return nil, fmt.Errorf("recoverable error %w; re-init failed: %w", err, ierr)
	}
	c.invalidateCache()
	return c.collect(ctx)
}

// poisonedErr builds the error returned on short-circuit. The poison root
// cause is chained via errors.Join so the errors.Is chain is preserved
// (upstream alerts can still errors.Is(err, &mtml.Error{}) to recover
// the original code).
func (c *mthreadsGpuCollector) poisonedErr() error {
	if ep := c.poisonErr.Load(); ep != nil {
		return errors.Join(
			errors.New("mthreads: GPU metrics disabled, library poisoned by prior failure"),
			*ep,
		)
	}
	return errors.New("mthreads: GPU metrics disabled, library poisoned")
}

func (c *mthreadsGpuCollector) setPoisoned(root error) {
	if c.poisoned.Load() {
		log.Warnf("mthreads: setPoisoned called on already-poisoned collector, ignoring: %v", root)
		return
	}
	c.poisonErr.Store(&root)
	c.poisoned.Store(true)
	log.Warnf("mthreads: library poisoned by failed Shutdown, GPU metrics disabled: %v", root)
}

func (c *mthreadsGpuCollector) collect(ctx context.Context) ([]*metric.Data, error) {
	cfg := configSnapshot()

	// Library readiness gate.
	if !mtml.IsReady() {
		log.Warnf("mthreads: mtml not ready on scrape, attempting re-init")
		if err := mtml.Init(); err != nil {
			return nil, fmt.Errorf("mtml not ready and re-init failed: %w", err)
		}
		c.invalidateCache()
	}

	cache, err := c.getOrBuildCache(ctx)
	if err != nil {
		return nil, fmt.Errorf("build mthreads cache: %w", err)
	}
	if len(cache.devices) == 0 {
		return nil, fmt.Errorf("no mthreads gpu devices found")
	}

	// Static info comes from cache (no per-scrape MTML calls).
	metrics := append([]*metric.Data{}, cache.staticInfo...)

	// Collect each device concurrently.
	var (
		mu      sync.Mutex
		allErrs []error
	)
	eg, subCtx := errgroup.WithContext(ctx)
	// Go 1.22+ scopes the loop variable per-iteration, so the closure
	// below captures the correct `gpu` without the `gpu := gpu` rebind
	// that was required pre-1.22. (go.mod: `go 1.24.0`.)
	for _, gpu := range cache.devices {
		eg.Go(func() error {
			if subCtx.Err() != nil {
				return fmt.Errorf("gpu %d: scrape deadline exceeded before collection: %w", gpu, subCtx.Err())
			}
			m, err := collectMthreadsDeviceMetrics(subCtx, cfg, gpu)
			mu.Lock()
			metrics = append(metrics, m...)
			if err != nil {
				allErrs = append(allErrs, err)
			}
			mu.Unlock()
			return nil
		})
	}
	// eg.Wait returns the first non-nil error from any worker (or nil
	// if all succeeded).
	if werr := eg.Wait(); werr != nil {
		allErrs = append(allErrs, werr)
	}

	return metrics, errors.Join(allErrs...)
}

// getOrBuildCache returns the current cache, building it if missing.
func (c *mthreadsGpuCollector) getOrBuildCache(ctx context.Context) (*mthreadsCache, error) {
	if cache := c.cache.Load(); cache != nil {
		return cache, nil
	}
	c.build.Lock()
	defer c.build.Unlock()
	// Double-check: another goroutine may have completed the build while we
	// were waiting for the lock.
	if cache := c.cache.Load(); cache != nil {
		return cache, nil
	}
	cache, err := c.buildCache(ctx)
	if err != nil {
		return nil, err
	}
	c.cache.Store(cache)
	return cache, nil
}

// invalidateCache drops the cached static info so the next Update rebuilds.
// Called by the recoverable-error re-init path in Update() after mtml.Shutdown
// + mtml.Init succeeds; see H-06.
func (c *mthreadsGpuCollector) invalidateCache() {
	c.build.Lock()
	defer c.build.Unlock()
	c.cache.Store(nil)
}

// buildCache enumerates devices and collects static info.
func (c *mthreadsGpuCollector) buildCache(ctx context.Context) (*mthreadsCache, error) {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("scrape timeout exceeded before building cache: %w", ctx.Err())
	}

	count, err := mtml.CountDevice()
	if err != nil {
		return nil, fmt.Errorf("count mthreads devices: %w", err)
	}
	// Reject an empty enumeration: devices=[]
	if count == 0 {
		return nil, fmt.Errorf("no mthreads gpu devices found during cache build")
	}

	cache := &mthreadsCache{devices: make([]uint32, 0, count)}
	for i := uint32(0); i < count; i++ {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("scrape timeout exceeded while enumerating devices: %w", ctx.Err())
		}
		cache.devices = append(cache.devices, i)
	}

	// Library/driver version: device-independent, emit once.
	cache.staticInfo = collectMthreadsVersions(ctx)

	for _, gpu := range cache.devices {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("scrape timeout before device %d static info: %w", gpu, ctx.Err())
		}
		m, derr := collectMthreadsDeviceInfo(ctx, gpu)
		if derr != nil {
			return nil, fmt.Errorf("gpu %d static info collection failed; cache not stored, will retry on next build: %w", gpu, derr)
		}
		cache.staticInfo = append(cache.staticInfo, m...)
	}

	return cache, nil
}

// collectMthreadsVersions emits the library version info metric.
func collectMthreadsVersions(ctx context.Context) []*metric.Data {
	var out []*metric.Data

	if ctx.Err() != nil {
		log.Debug("mthreads: scrape timeout exceeded before collecting library version, skipping")
		return nil
	}

	if v, err := mtml.LibraryVersion(); err == nil {
		out = append(out, metric.NewGaugeData("library_info", 1,
			"MTML library info.", map[string]string{"version": v}))
	} else {
		log.Warnf("mthreads: mtml get library version failed: %v", err)
		return nil
	}

	return out
}

// collectMthreadsDeviceInfo emits the per-device static info metrics (cached).
func collectMthreadsDeviceInfo(ctx context.Context, gpu uint32) ([]*metric.Data, error) {
	var (
		out  []*metric.Data
		errs []error
	)
	record := func(name string, err error) {
		recordStaticRead(gpu, name, err, &errs)
	}

	if ctx.Err() != nil {
		log.Debugf("mthreads: scrape timeout exceeded before device %d static info, skipping", gpu)
		return nil, ctx.Err()
	}

	dev, err := mtml.InitDeviceByIndex(gpu)
	if err != nil {
		record("init device", err)
		return nil, errors.Join(errs...)
	}
	defer dev.Close()

	gpuLabel := strconv.Itoa(int(gpu))

	// device_info: name/brand/serial.
	var (
		name      string
		brandStr  string
		serial    string
		gotName   bool
		gotBrand  bool
		gotSerial bool
	)
	if n, err := dev.Name(); err == nil {
		name = n
		gotName = true
	} else {
		record("name", err)
	}
	if b, err := dev.Brand(); err == nil {
		brandStr = b.String()
		gotBrand = true
	} else {
		record("brand", err)
	}
	if s, err := dev.SerialNumber(); err == nil {
		serial = s
		gotSerial = true
	} else {
		record("serial", err)
	}
	// Emit device_info only when all three identity fields are present.
	if gotName && gotBrand && gotSerial {
		out = append(out, metric.NewGaugeData("device_info", 1, "GPU info.", map[string]string{
			"gpu":    gpuLabel,
			"name":   name,
			"brand":  brandStr,
			"serial": serial,
		}))
	}

	// pci_info
	if sbdf, err := dev.PciSbdf(); err == nil {
		out = append(out, metric.NewGaugeData("device_pci_info", 1, "GPU PCI info.", map[string]string{
			"gpu":  gpuLabel,
			"sbdf": sbdf,
		}))
	} else {
		record("pci sbdf", err)
	}

	// device_spec: cores
	if cores, err := dev.GpuCores(); err == nil {
		out = append(out, metric.NewGaugeData("device_spec", 1, "GPU spec.", map[string]string{
			"gpu":   gpuLabel,
			"cores": strconv.Itoa(int(cores)),
		}))
	} else {
		record("gpu cores", err)
	}

	// bios version
	if bios, err := dev.BiosVersion(); err == nil {
		out = append(out, metric.NewGaugeData("device_bios_version", 1, "GPU BIOS version.", map[string]string{
			"gpu":  gpuLabel,
			"bios": bios,
		}))
	} else {
		record("bios version", err)
	}

	// musa compute capability
	if major, minor, err := dev.MusaComputeCapability(); err == nil {
		out = append(out, metric.NewGaugeData("device_musa_capability", 1, "GPU MUSA compute capability.", map[string]string{
			"gpu":             gpuLabel,
			"musa_capability": fmt.Sprintf("%d.%d", major, minor),
		}))
	} else {
		record("musa compute capability", err)
	}

	// memory info (static: type/vendor/bus_width)
	if mem, err := dev.InitMemory(); err == nil {
		defer mem.Close()
		labels := map[string]string{
			"gpu":       gpuLabel,
			"type":      "Unknown",
			"vendor":    "Unknown",
			"bus_width": "Unknown",
		}
		if t, err := mem.MemoryType(); err == nil {
			labels["type"] = memoryTypeName(t)
		} else {
			record("memory type", err)
		}
		if v, err := mem.Vendor(); err == nil {
			labels["vendor"] = v
		} else {
			record("memory vendor", err)
		}
		if w, err := mem.BusWidth(); err == nil {
			labels["bus_width"] = strconv.Itoa(int(w))
		} else {
			record("memory bus width", err)
		}
		out = append(out, metric.NewGaugeData("memory_info", 1, "GPU memory info.", labels))
	} else {
		record("init memory", err)
	}

	return out, errors.Join(errs...)
}

// collectMthreadsDeviceMetrics collects dynamic metrics for a single device.
// It opens the device once and dispatches to per-category collectors (health /
// PCIe / MtLink). The Health collector opens the gpu/memory/vpu sub-handles
// and returns them; this function owns the close order so the lifecycle does
// not depend on defer LIFO. Sub-handles are closed before the device, in the
// order vpu → mem → gpu, which is the order MTML expects.
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
			m, g, mem, vpu, err := collectMthreadsHealth(ctx, dev, gpu)
			// Sub-handles must be closed before the device. Register in
			// reverse so LIFO fires them in vpu → mem → gpu order.
			defer func() {
				if vpu != nil {
					_ = vpu.Close()
				}
				if mem != nil {
					_ = mem.Close()
				}
				if g != nil {
					_ = g.Close()
				}
			}()
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
func collectMthreadsHealth(ctx context.Context, dev *mtml.Device, gpu uint32) ([]*metric.Data, *mtml.Gpu, *mtml.Memory, *mtml.Vpu, error) {
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
		return nil, nil, nil, nil, ctx.Err()
	}

	var g *mtml.Gpu
	var mem *mtml.Memory
	var vpu *mtml.Vpu
	if h, err := dev.InitGpu(); err != nil {
		record("init gpu", err)
	} else {
		g = h
	}
	if h, err := dev.InitMemory(); err != nil {
		record("init memory", err)
	} else {
		mem = h
	}
	if h, err := dev.InitVpu(); err != nil {
		record("init vpu", err)
	} else {
		vpu = h
	}

	// Temperature
	if g != nil {
		t, err := g.Temperature()
		if err == nil {
			out = append(out, metric.NewGaugeData("gpu_temperature_celsius", float64(t),
				"GPU temperature in degrees Celsius.", map[string]string{"gpu": gpuLabel}))
		} else {
			record("gpu temperature", err)
		}
	}
	if mem != nil {
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
	if g != nil {
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
	if g != nil {
		if u, err := g.Utilization(); err == nil {
			out = append(out, metric.NewGaugeData("gpu_utilization_percent", float64(u),
				"GPU utilization (0-100).", map[string]string{"gpu": gpuLabel}))
		} else {
			record("gpu utilization", err)
		}
	}
	if mem != nil {
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
	if g != nil {
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
	if mem != nil {
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
	if vpu != nil {
		if util, err := vpu.Utilization(); err == nil {
			out = append(
				out,
				metric.NewGaugeData("vpu_utilization_percent", float64(util.Util),
					"GPU VPU utilization (0-100).", map[string]string{"gpu": gpuLabel}),
				metric.NewGaugeData("vpu_encoder_utilization_percent", float64(util.EncUtil),
					"GPU VPU encoder utilization (0-100).", map[string]string{"gpu": gpuLabel}),
				metric.NewGaugeData("vpu_decoder_utilization_percent", float64(util.DecUtil),
					"GPU VPU decoder utilization (0-100).", map[string]string{"gpu": gpuLabel}),
			)
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

	return out, g, mem, vpu, errors.Join(errs...)
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
		out = append(
			out,
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
	// Bandwidth/count are per-device static specs (not per-link live throughput),
	// so they don't get a `link` label.
	out = append(
		out,
		metric.NewGaugeData("mtlink_link_bandwidth_gb_s", float64(spec.BandWidth),
			"GPU MtLink per-link max bandwidth in GB/s (device static spec, not live throughput).",
			map[string]string{"gpu": gpuLabel}),
		metric.NewGaugeData("mtlink_link_count", float64(spec.LinkNum),
			"GPU MtLink max number of supported links (device static spec).",
			map[string]string{"gpu": gpuLabel}),
	)
	for link := uint32(0); link < spec.LinkNum; link++ {
		linkLabel := strconv.Itoa(int(link))
		if st, err := dev.MtLinkState(link); err == nil {
			out = append(out, metric.NewGaugeData("mtlink_state", float64(st),
				"GPU MtLink state (0=DOWN,1=UP,2=DOWNGRADE).",
				map[string]string{"gpu": gpuLabel, "link": linkLabel}))
		} else {
			record(fmt.Sprintf("mtlink %d state", link), err)
		}
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

// recordStaticRead appends a per-field static-read failure to errs, but
// only if the error is operational. nil and NotSupported are both
// suppressed so that a device which simply lacks a given static field
// (or driver version) does not block cache publication.
func recordStaticRead(gpu uint32, name string, err error, errs *[]error) {
	if err == nil || mtml.IsNotSupported(err) {
		return
	}
	*errs = append(*errs, fmt.Errorf("gpu %d %s: %w", gpu, name, err))
}
