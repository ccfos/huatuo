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
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Correlator owns a bounded pending-event queue and cumulative diagnostics.
// All public methods are safe for concurrent event ingestion and metric scrapes.
type Correlator struct {
	mu sync.RWMutex

	options Options
	nextID  atomic.Uint64
	pending []KernelDrop
	recent  []Incident

	droppedPending    uint64
	events            uint64
	incidents         uint64
	resets            uint64
	degradedSamples   uint64
	unmatchedHardware uint64
	byKey             map[MetricKey]uint64
	reasonSlots       map[string]struct{}
	lastHardware      map[string]HardwareSample
}

// New creates a correlator with normalized options.
func New(options Options) *Correlator {
	return &Correlator{
		options:      normalizeOptions(options),
		byKey:        make(map[MetricKey]uint64),
		reasonSlots:  make(map[string]struct{}),
		lastHardware: make(map[string]HardwareSample),
	}
}

// ObserveKernel queues one dropwatch event. It returns false when an invalid
// event is rejected. When the bounded queue is full, the oldest event is
// finalized immediately rather than silently discarded.
func (c *Correlator) ObserveKernel(event KernelDrop) bool {
	if err := validateKernelDrop(event); err != nil {
		return false
	}
	event.Device = strings.TrimSpace(event.Device)
	event.Reason = normalizeReason(event.Reason)
	event.Protocol = strings.ToLower(strings.TrimSpace(event.Protocol))
	if event.ID == 0 {
		event.ID = c.nextID.Add(1)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.events++

	if len(c.pending) >= c.options.PendingLimit {
		oldest := c.pending[0]
		copy(c.pending, c.pending[1:])
		c.pending = c.pending[:len(c.pending)-1]
		c.droppedPending++
		incident := c.classifyWithoutHardwareLocked(oldest, event.Timestamp, "pending queue limit reached")
		c.recordIncidentLocked(incident)
	}
	c.pending = append(c.pending, event)
	return true
}

// ObserveHardware records a normalized hardware sample and finalizes events
// covered by its measurement period. Events that outlive the correlation window
// are finalized using stack/reason evidence.
func (c *Correlator) ObserveHardware(sample HardwareSample) []Incident {
	if sample.Timestamp.IsZero() || strings.TrimSpace(sample.Device) == "" {
		return nil
	}
	if sample.PeriodStart.IsZero() || sample.PeriodStart.After(sample.Timestamp) {
		sample.PeriodStart = sample.Timestamp.Add(-c.options.Window)
	}
	sample.Device = strings.TrimSpace(sample.Device)
	sample = cloneHardwareSample(sample)

	c.mu.Lock()
	defer c.mu.Unlock()
	if sample.Reset {
		c.resets++
	}
	if sample.ReadDegraded {
		c.degradedSamples++
	}
	c.lastHardware[sample.Device] = sample

	finalized := make([]Incident, 0)
	kept := c.pending[:0]
	matchedRX, matchedTX := false, false
	for _, event := range c.pending {
		if c.sampleCoversEventLocked(sample, event) {
			incident := c.classifyWithHardwareLocked(event, sample)
			finalized = append(finalized, incident)
			c.recordIncidentLocked(incident)
			switch event.Direction {
			case DirectionRX:
				matchedRX = true
			case DirectionTX:
				matchedTX = true
			}
			continue
		}
		if event.Timestamp.Before(sample.Timestamp.Add(-c.options.Window)) {
			incident := c.classifyWithoutHardwareLocked(event, sample.Timestamp, "correlation window expired")
			finalized = append(finalized, incident)
			c.recordIncidentLocked(incident)
			continue
		}
		kept = append(kept, event)
	}
	c.pending = kept

	if sample.Total(DirectionRX) > 0 && !matchedRX {
		c.unmatchedHardware++
	}
	if sample.Total(DirectionTX) > 0 && !matchedTX {
		c.unmatchedHardware++
	}
	return cloneIncidents(finalized)
}

// Flush finalizes events older than the configured window. Collectors call it
// even when a device read fails so missing hardware data cannot retain events.
func (c *Correlator) Flush(now time.Time) []Incident {
	if now.IsZero() {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	finalized := make([]Incident, 0)
	kept := c.pending[:0]
	deadline := now.Add(-c.options.Window)
	for _, event := range c.pending {
		if event.Timestamp.After(deadline) {
			kept = append(kept, event)
			continue
		}
		incident := c.classifyWithoutHardwareLocked(event, now, "correlation window expired")
		finalized = append(finalized, incident)
		c.recordIncidentLocked(incident)
	}
	c.pending = kept
	return cloneIncidents(finalized)
}

func (c *Correlator) sampleCoversEventLocked(sample HardwareSample, event KernelDrop) bool {
	if sample.Reset || sample.Device != event.Device {
		return false
	}
	if event.Timestamp.Before(sample.PeriodStart.Add(-c.options.FutureTolerance)) ||
		event.Timestamp.After(sample.Timestamp.Add(c.options.FutureTolerance)) {
		return false
	}
	return event.Direction == DirectionRX || event.Direction == DirectionTX
}

func (c *Correlator) classifyWithHardwareLocked(event KernelDrop, sample HardwareSample) Incident {
	evidenceCount := sample.Total(event.Direction)
	if evidenceCount == 0 {
		return c.classifyWithoutHardwareLocked(event, sample.Timestamp, "hardware counters unchanged")
	}

	evidence := make([]string, 0, len(sample.Delta)+2)
	evidence = append(evidence, "hardware counter delta overlaps dropwatch event")
	for _, counter := range allCounters {
		if value := sample.Delta[counter]; value > 0 && counterDirection(counter) == event.Direction {
			evidence = append(evidence, string(counter)+"="+formatUint(value))
		}
	}
	if sample.ReadDegraded {
		evidence = append(evidence, "hardware sample was partially degraded")
	}

	confidence := 0.82 + math.Min(0.15, math.Log10(float64(evidenceCount)+1)/10)
	if sample.ReadDegraded {
		confidence -= 0.12
	}
	return Incident{
		Event:            event,
		Layer:            LayerHardware,
		Confidence:       clampConfidence(confidence),
		Evidence:         evidence,
		HardwareDelta:    cloneCounterMap(sample.Delta),
		HardwareSampleAt: sample.Timestamp,
		Driver:           sample.Driver,
		CorrelationLag:   absoluteDuration(sample.Timestamp.Sub(event.Timestamp)),
		FinalizedAt:      sample.Timestamp,
	}
}

func (c *Correlator) classifyWithoutHardwareLocked(event KernelDrop, finalizedAt time.Time, cause string) Incident {
	layer, confidence, evidence := classifyKernelEvidence(event)
	if cause != "" {
		evidence = append(evidence, cause)
	}
	return Incident{
		Event:          event,
		Layer:          layer,
		Confidence:     confidence,
		Evidence:       evidence,
		CorrelationLag: absoluteDuration(finalizedAt.Sub(event.Timestamp)),
		FinalizedAt:    finalizedAt,
	}
}

func classifyKernelEvidence(event KernelDrop) (Layer, float64, []string) {
	stack := strings.ToLower(event.Stack)
	reason := strings.ToLower(event.Reason)

	for _, marker := range driverStackMarkers {
		if strings.Contains(stack, marker) {
			return LayerDriver, 0.86, []string{"driver stack marker: " + marker, "no overlapping hardware counter delta"}
		}
	}
	for _, marker := range driverReasonMarkers {
		if strings.Contains(reason, marker) {
			return LayerDriver, 0.76, []string{"driver drop reason marker: " + marker, "no overlapping hardware counter delta"}
		}
	}
	for _, marker := range protocolStackMarkers {
		if strings.Contains(stack, marker) {
			return LayerProtocolStack, 0.84, []string{"protocol stack marker: " + marker, "no overlapping hardware counter delta"}
		}
	}
	if event.Protocol != "" || event.Reason != "unknown" {
		return LayerProtocolStack, 0.64, []string{"kernel dropwatch event without hardware evidence"}
	}
	return LayerUnknown, 0.3, []string{"insufficient device, stack, and hardware evidence"}
}

var driverStackMarkers = []string{
	"mlx5_", "mlx5e_", "i40e_", "ixgbe_", "bnxt_", "virtio_net", "netdev_rx_handler",
	"napi_gro", "napi_consume", "dev_hard_start_xmit", "sch_direct_xmit",
}

var driverReasonMarkers = []string{
	"rx_handler", "driver", "dev_ready", "tc_egress", "qdisc", "xdp",
}

var protocolStackMarkers = []string{
	"ip_rcv", "ip6_rcv", "tcp_", "udp_", "icmp_", "nf_hook", "nft_",
	"br_handle", "neigh_", "__netif_receive_skb", "kfree_skb_reason",
}

func counterDirection(counter Counter) Direction {
	if strings.HasPrefix(string(counter), "rx_") {
		return DirectionRX
	}
	if strings.HasPrefix(string(counter), "tx_") {
		return DirectionTX
	}
	return DirectionUnknown
}

// InferDirection derives packet direction from dropwatch type, reason, and
// stack. It deliberately returns unknown when evidence conflicts instead of
// forcing an incorrect hardware correlation.
func InferDirection(eventType, reason, stack string) Direction {
	text := strings.ToLower(eventType + "\n" + reason + "\n" + stack)
	rxScore, txScore := 0, 0
	for _, marker := range []string{"netif_receive", "napi_", "ip_rcv", "ip6_rcv", "rx_", "ingress"} {
		if strings.Contains(text, marker) {
			rxScore++
		}
	}
	for _, marker := range []string{"dev_queue_xmit", "hard_start_xmit", "sch_", "tx_", "egress"} {
		if strings.Contains(text, marker) {
			txScore++
		}
	}
	switch {
	case rxScore > txScore:
		return DirectionRX
	case txScore > rxScore:
		return DirectionTX
	default:
		return DirectionUnknown
	}
}

func (c *Correlator) recordIncidentLocked(incident Incident) {
	c.incidents++
	reason := c.boundedReasonLocked(incident.Event.Reason)
	key := MetricKey{
		Device:    boundedDevice(incident.Event.Device),
		Direction: incident.Event.Direction,
		Layer:     incident.Layer,
		Reason:    reason,
	}
	c.byKey[key]++
	c.recent = append(c.recent, cloneIncident(incident))
	if len(c.recent) > c.options.IncidentLimit {
		copy(c.recent, c.recent[len(c.recent)-c.options.IncidentLimit:])
		c.recent = c.recent[:c.options.IncidentLimit]
	}
}

func (c *Correlator) boundedReasonLocked(reason string) string {
	reason = normalizeReason(reason)
	if _, exists := c.reasonSlots[reason]; exists {
		return reason
	}
	if len(c.reasonSlots) >= c.options.ReasonLimit {
		return "other"
	}
	c.reasonSlots[reason] = struct{}{}
	return reason
}

// Snapshot returns a deep copy that callers may retain or mutate.
func (c *Correlator) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	snapshot := Snapshot{
		Pending:             len(c.pending),
		DroppedPending:      c.droppedPending,
		Events:              c.events,
		Incidents:           c.incidents,
		Resets:              c.resets,
		DegradedSamples:     c.degradedSamples,
		UnmatchedHardware:   c.unmatchedHardware,
		ByKey:               make(map[MetricKey]uint64, len(c.byKey)),
		LastHardwareByIface: make(map[string]HardwareSample, len(c.lastHardware)),
		Recent:              cloneIncidents(c.recent),
	}
	for key, value := range c.byKey {
		snapshot.ByKey[key] = value
	}
	for device, sample := range c.lastHardware {
		snapshot.LastHardwareByIface[device] = cloneHardwareSample(sample)
	}
	return snapshot
}

// RecentSince returns finalized incidents within a time range, oldest first.
func (c *Correlator) RecentSince(since time.Time) []Incident {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]Incident, 0)
	for _, incident := range c.recent {
		if !incident.FinalizedAt.Before(since) {
			result = append(result, cloneIncident(incident))
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].FinalizedAt.Before(result[j].FinalizedAt)
	})
	return result
}

func normalizeReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return "unknown"
	}
	reason = strings.NewReplacer(" ", "_", "-", "_", "/", "_").Replace(reason)
	if len(reason) > 96 {
		reason = reason[:96]
	}
	return reason
}

func boundedDevice(device string) string {
	device = strings.TrimSpace(device)
	if device == "" {
		return "unknown"
	}
	if len(device) > 32 {
		return device[:32]
	}
	return device
}

func clampConfidence(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func absoluteDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

func formatUint(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}

func cloneIncidents(in []Incident) []Incident {
	out := make([]Incident, len(in))
	for index := range in {
		out[index] = cloneIncident(in[index])
	}
	return out
}

var defaultCorrelator = struct {
	mu    sync.RWMutex
	value *Correlator
}{value: New(DefaultOptions())}

// Default returns the process-wide correlator shared by dropwatch and netdev_hw.
func Default() *Correlator {
	defaultCorrelator.mu.RLock()
	value := defaultCorrelator.value
	defaultCorrelator.mu.RUnlock()
	return value
}

// ConfigureDefault atomically replaces the process-wide correlator. Existing
// callers retain their old pointer, while new collector instances share the new
// configuration. This is intended for startup configuration and tests.
func ConfigureDefault(options Options) *Correlator {
	value := New(options)
	defaultCorrelator.mu.Lock()
	defaultCorrelator.value = value
	defaultCorrelator.mu.Unlock()
	return value
}
