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

// Package dropcorrelation joins low-frequency NIC counter samples with
// high-frequency dropwatch events. It intentionally contains no Linux or
// storage dependencies so the correlation policy can be tested deterministically.
package dropcorrelation

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Layer identifies the lowest layer at which packet loss can be proven.
// Values are stable because they are exported as metric and storage labels.
type Layer string

const (
	LayerHardware      Layer = "hardware"
	LayerDriver        Layer = "driver"
	LayerProtocolStack Layer = "protocol_stack"
	LayerUnknown       Layer = "unknown"
)

// Direction is the packet direction associated with a counter or event.
type Direction string

const (
	DirectionRX      Direction = "rx"
	DirectionTX      Direction = "tx"
	DirectionUnknown Direction = "unknown"
)

// Counter is a normalized, monotonic NIC statistic. Drivers use many names for
// the same condition; normalization keeps the rest of the pipeline independent
// of Intel, Mellanox, Broadcom, or virtio naming conventions.
type Counter string

const (
	CounterRXDropped Counter = "rx_dropped"
	CounterRXMissed  Counter = "rx_missed"
	CounterRXErrors  Counter = "rx_errors"
	CounterRXNoBuf   Counter = "rx_no_buffer"
	CounterTXDropped Counter = "tx_dropped"
	CounterTXErrors  Counter = "tx_errors"
	CounterTXTimeout Counter = "tx_timeout"
)

var allCounters = []Counter{
	CounterRXDropped,
	CounterRXMissed,
	CounterRXErrors,
	CounterRXNoBuf,
	CounterTXDropped,
	CounterTXErrors,
	CounterTXTimeout,
}

// AllCounters returns normalized counters in deterministic order.
func AllCounters() []Counter {
	return append([]Counter(nil), allCounters...)
}

// Options controls memory use and time matching. Zero values are replaced by
// conservative production defaults by New.
type Options struct {
	Window          time.Duration
	PendingLimit    int
	IncidentLimit   int
	ReasonLimit     int
	FutureTolerance time.Duration
}

// DefaultOptions returns settings suitable for a ten-second hardware polling
// interval without allowing an event storm to grow memory without bound.
func DefaultOptions() Options {
	return Options{
		Window:          15 * time.Second,
		PendingLimit:    4096,
		IncidentLimit:   1024,
		ReasonLimit:     64,
		FutureTolerance: time.Second,
	}
}

func normalizeOptions(in Options) Options {
	defaults := DefaultOptions()
	if in.Window <= 0 {
		in.Window = defaults.Window
	}
	if in.PendingLimit <= 0 {
		in.PendingLimit = defaults.PendingLimit
	}
	if in.IncidentLimit <= 0 {
		in.IncidentLimit = defaults.IncidentLimit
	}
	if in.ReasonLimit <= 0 {
		in.ReasonLimit = defaults.ReasonLimit
	}
	if in.FutureTolerance < 0 {
		in.FutureTolerance = 0
	} else if in.FutureTolerance == 0 {
		in.FutureTolerance = defaults.FutureTolerance
	}
	return in
}

// RawSample is one absolute snapshot collected from sysfs and ethtool.
type RawSample struct {
	Timestamp time.Time
	Device    string
	Driver    string
	Sysfs     map[string]uint64
	Ethtool   map[string]uint64
}

// HardwareSample is the normalized delta between two RawSamples.
type HardwareSample struct {
	Timestamp    time.Time
	PeriodStart  time.Time
	Device       string
	Driver       string
	Delta        map[Counter]uint64
	Reset        bool
	Sources      map[Counter][]string
	ReadDegraded bool
}

// Total returns the hardware evidence for one packet direction.
func (s HardwareSample) Total(direction Direction) uint64 {
	var counters []Counter
	switch direction {
	case DirectionRX:
		counters = []Counter{CounterRXMissed, CounterRXErrors, CounterRXNoBuf}
		// rx_dropped is only used when a more specific hardware counter is absent.
		if sumCounters(s.Delta, counters) == 0 {
			counters = append(counters, CounterRXDropped)
		}
	case DirectionTX:
		counters = []Counter{CounterTXDropped, CounterTXErrors, CounterTXTimeout}
	default:
		return 0
	}
	return sumCounters(s.Delta, counters)
}

func sumCounters(values map[Counter]uint64, counters []Counter) uint64 {
	var total uint64
	for _, counter := range counters {
		total += values[counter]
	}
	return total
}

// KernelDrop is the subset of a dropwatch event needed for correlation.
type KernelDrop struct {
	ID          uint64
	Timestamp   time.Time
	Device      string
	IfIndex     uint32
	Direction   Direction
	Reason      string
	Stack       string
	ContainerID string
	Protocol    string
	PacketLen   uint32
}

// Incident is an immutable correlation result suitable for persistent storage.
type Incident struct {
	Event            KernelDrop
	Layer            Layer
	Confidence       float64
	Evidence         []string
	HardwareDelta    map[Counter]uint64
	HardwareSampleAt time.Time
	Driver           string
	CorrelationLag   time.Duration
	FinalizedAt      time.Time
}

// MetricKey is the bounded label set used for cumulative incident metrics.
type MetricKey struct {
	Device    string
	Direction Direction
	Layer     Layer
	Reason    string
}

// Snapshot is a point-in-time, race-free view of correlator state.
type Snapshot struct {
	Pending             int
	DroppedPending      uint64
	Events              uint64
	Incidents           uint64
	Resets              uint64
	DegradedSamples     uint64
	UnmatchedHardware   uint64
	ByKey               map[MetricKey]uint64
	LastHardwareByIface map[string]HardwareSample
	Recent              []Incident
}

// SortedKeys returns metric keys in a stable order for tests and exporters.
func (s Snapshot) SortedKeys() []MetricKey {
	keys := make([]MetricKey, 0, len(s.ByKey))
	for key := range s.ByKey {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := keys[i], keys[j]
		if left.Device != right.Device {
			return left.Device < right.Device
		}
		if left.Direction != right.Direction {
			return left.Direction < right.Direction
		}
		if left.Layer != right.Layer {
			return left.Layer < right.Layer
		}
		return left.Reason < right.Reason
	})
	return keys
}

var (
	ErrEmptyDevice    = errors.New("drop correlation: empty device")
	ErrEmptyTimestamp = errors.New("drop correlation: empty timestamp")
)

func validateRawSample(sample RawSample) error {
	if strings.TrimSpace(sample.Device) == "" {
		return ErrEmptyDevice
	}
	if sample.Timestamp.IsZero() {
		return ErrEmptyTimestamp
	}
	return nil
}

func validateKernelDrop(event KernelDrop) error {
	if event.Timestamp.IsZero() {
		return ErrEmptyTimestamp
	}
	if event.Direction == "" {
		return fmt.Errorf("drop correlation: direction is required")
	}
	return nil
}

func cloneCounterMap(in map[Counter]uint64) map[Counter]uint64 {
	out := make(map[Counter]uint64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneStringSlices(in map[Counter][]string) map[Counter][]string {
	out := make(map[Counter][]string, len(in))
	for key, value := range in {
		out[key] = append([]string(nil), value...)
	}
	return out
}

func cloneHardwareSample(in HardwareSample) HardwareSample {
	in.Delta = cloneCounterMap(in.Delta)
	in.Sources = cloneStringSlices(in.Sources)
	return in
}

func cloneIncident(in Incident) Incident {
	in.Evidence = append([]string(nil), in.Evidence...)
	in.HardwareDelta = cloneCounterMap(in.HardwareDelta)
	return in
}
