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

package dropwatch

import (
	"fmt"
	"os"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	pcap "github.com/ccfos/huatuo/internal/pcapfilter"
)

const (
	hardwareProgramSection = "raw_tracepoint/devlink_trap_report"
	perfEventMapName       = "perf_events"
)

// Config validation precedes detection; disabled mode must not access tracefs.
func resolveHardwareEnabled(mode HardwareMode) (bool, error) {
	if mode != HardwareAuto {
		return false, nil
	}
	enabled, err := bpf.TracepointAvailable("devlink", "devlink_trap_report")
	if err != nil {
		return false, fmt.Errorf("detect hardware drop support: %w", err)
	}
	return enabled, nil
}

func loadBPF(cfg *Config, deviceMode uint32, limiter *bpf.RateLimiter, hardwareEnabled bool) (bpf.BPF, error) {
	objectBytes, err := os.ReadFile(cfg.BPFPath)
	if err != nil {
		return nil, fmt.Errorf("read dropwatch BPF object %q: %w", cfg.BPFPath, err)
	}

	var excludedSections []string
	if !hardwareEnabled {
		excludedSections = append(excludedSections, hardwareProgramSection)
	}

	return pcap.Load(
		fmt.Sprintf("dropwatch_%d.o", time.Now().UnixNano()), objectBytes,
		cfg.FilterExpression,
		limiter.Constants(map[string]any{"filter_dev_mode": deviceMode}),
		excludedSections...,
	)
}

// Readers must be ready before probes emit events. newTracer owns rollback,
// including resources acquired before a failed attach.
func (t *Tracer) attachBPF(netdev netdevOptions) error {
	t.perfStatusMap = t.bpf.MapIDByName(perfStatusMapName)
	t.rateLimitStateMap = t.bpf.MapIDByName(rateLimitStateMapName)

	if t.perfStatusMap == 0 || t.rateLimitStateMap == 0 {
		return fmt.Errorf("dropwatch BPF maps %q, %q not found", perfStatusMapName, rateLimitStateMapName)
	}
	if err := applyNetdevOptions(t.bpf, netdev); err != nil {
		return fmt.Errorf("configure dropwatch devices: %w", err)
	}
	if t.limiter.Enabled() {
		if err := t.limiter.OpenEventPipe(t.ctx, t.bpf); err != nil {
			return err
		}
	}

	reader, err := t.bpf.EventPipeByName(t.ctx, perfEventMapName, bpf.DefaultPerfEventBufferBytes)
	if err != nil {
		return fmt.Errorf("open dropwatch event pipe: %w", err)
	}

	t.reader = reader
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if err := t.bpf.Attach(); err != nil {
		return fmt.Errorf("attach dropwatch probes: %w", err)
	}
	return nil
}
