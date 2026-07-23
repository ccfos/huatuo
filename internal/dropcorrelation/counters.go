// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dropcorrelation

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// CounterNormalizer converts driver-specific ethtool names and generic sysfs
// names into monotonic deltas. It is safe for concurrent use because dynamic
// interface discovery can overlap with metric scrapes.
type CounterNormalizer struct {
	mu       sync.Mutex
	previous map[string]normalizedAbsolute
}

type normalizedAbsolute struct {
	timestamp int64
	values    map[Counter]uint64
}

// NewCounterNormalizer creates an empty normalizer. The first sample for each
// device establishes a baseline and therefore has zero deltas.
func NewCounterNormalizer() *CounterNormalizer {
	return &CounterNormalizer{previous: make(map[string]normalizedAbsolute)}
}

// Normalize converts and differences one absolute sample.
func (n *CounterNormalizer) Normalize(raw RawSample) (HardwareSample, error) {
	if err := validateRawSample(raw); err != nil {
		return HardwareSample{}, err
	}

	values, sources := normalizeAbsolute(raw.Driver, raw.Sysfs, raw.Ethtool)
	result := HardwareSample{
		Timestamp: raw.Timestamp,
		Device:    raw.Device,
		Driver:    raw.Driver,
		Delta:     make(map[Counter]uint64, len(values)),
		Sources:   sources,
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	previous, found := n.previous[raw.Device]
	if found {
		result.PeriodStart = unixNanoTime(previous.timestamp)
		for counter, current := range values {
			old, existed := previous.values[counter]
			if !existed {
				continue
			}
			if current < old {
				result.Reset = true
				continue
			}
			result.Delta[counter] = current - old
		}
	} else {
		result.PeriodStart = raw.Timestamp
	}

	n.previous[raw.Device] = normalizedAbsolute{
		timestamp: raw.Timestamp.UnixNano(),
		values:    values,
	}
	return result, nil
}

// Forget removes a device baseline after hot-unplug so a device with a reused
// name does not generate a false reset or enormous delta.
func (n *CounterNormalizer) Forget(device string) {
	n.mu.Lock()
	delete(n.previous, device)
	n.mu.Unlock()
}

// Devices returns current baseline devices in deterministic order.
func (n *CounterNormalizer) Devices() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	devices := make([]string, 0, len(n.previous))
	for device := range n.previous {
		devices = append(devices, device)
	}
	sort.Strings(devices)
	return devices
}

// unixNanoTime is kept behind a helper to make the representation explicit.
func unixNanoTime(value int64) time.Time {
	return time.Unix(0, value)
}

type aliasSet struct {
	counter Counter
	names   []string
}

var genericAliases = []aliasSet{
	{CounterRXDropped, []string{"rx_dropped", "rx_drops", "rx_discards", "rx_discard"}},
	{CounterRXMissed, []string{"rx_missed_errors", "rx_missed", "rx_missed_packets", "rx_no_dma_resources"}},
	{CounterRXErrors, []string{"rx_errors", "rx_error", "rx_crc_errors", "rx_length_errors"}},
	{CounterRXNoBuf, []string{"rx_no_buffer", "rx_no_buffer_count", "rx_out_of_buffer", "rx_buffer_passed_thres_phy"}},
	{CounterTXDropped, []string{"tx_dropped", "tx_drops", "tx_discards", "tx_discard"}},
	{CounterTXErrors, []string{"tx_errors", "tx_error", "tx_carrier_errors", "tx_fifo_errors"}},
	{CounterTXTimeout, []string{"tx_timeout", "tx_timeouts", "tx_timeout_count", "tx_queue_stopped"}},
}

var driverAliases = map[string][]aliasSet{
	"mlx5_core": {
		{CounterRXDropped, []string{"rx_out_of_buffer", "rx_vport_unicast_packets_drop", "rx_vport_multicast_packets_drop"}},
		{CounterRXErrors, []string{"rx_crc_errors_phy", "rx_in_range_len_errors_phy", "rx_out_of_range_len_phy"}},
		{CounterTXDropped, []string{"tx_vport_unicast_packets_drop", "tx_vport_multicast_packets_drop"}},
	},
	"i40e": {
		{CounterRXMissed, []string{"rx_no_dma_resources", "rx_alloc_fail"}},
		{CounterRXErrors, []string{"rx_crc_errors", "rx_length_errors"}},
		{CounterTXDropped, []string{"tx_busy", "tx_dropped_link_down"}},
	},
	"ixgbe": {
		{CounterRXMissed, []string{"rx_no_dma_resources", "rx_missed_errors"}},
		{CounterRXErrors, []string{"rx_crc_errors", "rx_csum_offload_errors"}},
		{CounterTXTimeout, []string{"tx_restart_queue", "tx_timeout_count"}},
	},
	"bnxt_en": {
		{CounterRXMissed, []string{"rx_oom_discards", "rx_netpoll_discards"}},
		{CounterRXErrors, []string{"rx_l4_csum_errors", "rx_resets"}},
		{CounterTXDropped, []string{"tx_discard_pkts", "tx_error_pkts"}},
	},
	"virtio_net": {
		{CounterRXNoBuf, []string{"rx_queue_0_drops", "rx_queue_0_xdp_drops"}},
		{CounterTXDropped, []string{"tx_queue_0_drops", "tx_queue_0_xdp_drops"}},
	},
}

func normalizeAbsolute(driver string, sysfs, ethtool map[string]uint64) (map[Counter]uint64, map[Counter][]string) {
	values := make(map[Counter]uint64, len(allCounters))
	sources := make(map[Counter][]string, len(allCounters))
	seen := make(map[string]struct{})

	sets := make([]aliasSet, 0, len(genericAliases)+len(driverAliases[driver]))
	sets = append(sets, driverAliases[driver]...)
	sets = append(sets, genericAliases...)
	for _, set := range sets {
		for _, name := range set.names {
			canonical := canonicalCounterName(name)
			key := string(set.counter) + "\x00" + canonical
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			if value, exists := lookupCanonical(ethtool, canonical); exists {
				values[set.counter] += value
				sources[set.counter] = append(sources[set.counter], "ethtool:"+canonical)
			}
		}
	}

	// Sysfs counters are fallbacks. Adding both sysfs and driver counters would
	// double-count the same packets on many drivers.
	for _, set := range genericAliases {
		if len(sources[set.counter]) != 0 {
			continue
		}
		for _, name := range set.names {
			canonical := canonicalCounterName(name)
			if value, exists := lookupCanonical(sysfs, canonical); exists {
				values[set.counter] = value
				sources[set.counter] = []string{"sysfs:" + canonical}
				break
			}
		}
	}

	for counter := range sources {
		sort.Strings(sources[counter])
	}
	return values, sources
}

func canonicalCounterName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(name)
	for strings.Contains(name, "__") {
		name = strings.ReplaceAll(name, "__", "_")
	}
	return strings.Trim(name, "_")
}

func lookupCanonical(values map[string]uint64, wanted string) (uint64, bool) {
	for name, value := range values {
		if canonicalCounterName(name) == wanted {
			return value, true
		}
	}
	return 0, false
}
