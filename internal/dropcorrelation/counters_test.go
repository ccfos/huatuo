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
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestCounterNormalizerBaselineAndDelta(t *testing.T) {
	normalizer := NewCounterNormalizer()
	start := time.Unix(100, 0)
	baseline, err := normalizer.Normalize(RawSample{
		Timestamp: start,
		Device:    "eth0",
		Driver:    "i40e",
		Sysfs: map[string]uint64{
			"rx_dropped":       10,
			"tx_dropped":       2,
			"tx_errors":        1,
			"tx_fifo_errors":   1,
			"rx_missed_errors": 4,
		},
	})
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if got := baseline.Total(DirectionRX); got != 0 {
		t.Fatalf("baseline RX total = %d, want 0", got)
	}

	next, err := normalizer.Normalize(RawSample{
		Timestamp: start.Add(10 * time.Second),
		Device:    "eth0",
		Driver:    "i40e",
		Sysfs: map[string]uint64{
			"rx_dropped":       15,
			"rx_missed_errors": 7,
			"tx_dropped":       4,
			"tx_errors":        2,
			"tx_fifo_errors":   2,
		},
	})
	if err != nil {
		t.Fatalf("next sample: %v", err)
	}
	if !next.PeriodStart.Equal(start) {
		t.Fatalf("period start = %s, want %s", next.PeriodStart, start)
	}
	if got := next.Delta[CounterRXDropped]; got != 5 {
		t.Errorf("rx_dropped delta = %d, want 5", got)
	}
	if got := next.Delta[CounterRXMissed]; got != 3 {
		t.Errorf("rx_missed delta = %d, want 3", got)
	}
	if got := next.Delta[CounterTXDropped]; got != 2 {
		t.Errorf("tx_dropped delta = %d, want 2", got)
	}
	if got := next.Total(DirectionRX); got != 3 {
		t.Errorf("RX total = %d, want specific rx_missed count 3", got)
	}
	if got := next.Total(DirectionTX); got != 3 {
		t.Errorf("TX total = %d, want dropped + canonical error = 3", got)
	}
}

func TestCounterNormalizerRejectsInvalidSamples(t *testing.T) {
	normalizer := NewCounterNormalizer()
	tests := []struct {
		name string
		raw  RawSample
		want error
	}{
		{name: "empty device", raw: RawSample{Timestamp: time.Now()}, want: ErrEmptyDevice},
		{name: "whitespace device", raw: RawSample{Timestamp: time.Now(), Device: "  "}, want: ErrEmptyDevice},
		{name: "empty timestamp", raw: RawSample{Device: "eth0"}, want: ErrEmptyTimestamp},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizer.Normalize(test.raw)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCounterNormalizerDetectsResetWithoutUnderflow(t *testing.T) {
	normalizer := NewCounterNormalizer()
	start := time.Unix(200, 0)
	_, _ = normalizer.Normalize(RawSample{
		Timestamp: start,
		Device:    "ens5",
		Sysfs:     map[string]uint64{"rx_dropped": 1_000, "tx_errors": 500},
	})
	sample, err := normalizer.Normalize(RawSample{
		Timestamp: start.Add(time.Second),
		Device:    "ens5",
		Sysfs:     map[string]uint64{"rx_dropped": 3, "tx_errors": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sample.Reset {
		t.Fatal("Reset = false, want true")
	}
	if got := sample.Delta[CounterRXDropped]; got != 0 {
		t.Fatalf("reset delta underflowed to %d", got)
	}
	if got := sample.Delta[CounterTXErrors]; got != 0 {
		t.Fatalf("reset TX delta underflowed to %d", got)
	}
}

func TestCounterNormalizerForgetTreatsReusedNameAsBaseline(t *testing.T) {
	normalizer := NewCounterNormalizer()
	now := time.Unix(300, 0)
	_, _ = normalizer.Normalize(RawSample{
		Timestamp: now,
		Device:    "eth9",
		Sysfs:     map[string]uint64{"rx_dropped": 999},
	})
	normalizer.Forget("eth9")
	sample, err := normalizer.Normalize(RawSample{
		Timestamp: now.Add(time.Minute),
		Device:    "eth9",
		Sysfs:     map[string]uint64{"rx_dropped": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sample.Reset {
		t.Fatal("reused device name reported a reset")
	}
	if sample.Delta[CounterRXDropped] != 0 {
		t.Fatal("reused device name produced a non-baseline delta")
	}
}

func TestCounterNormalizerDevicesAreSortedCopies(t *testing.T) {
	normalizer := NewCounterNormalizer()
	now := time.Unix(400, 0)
	for _, device := range []string{"z0", "a0", "m0"} {
		_, err := normalizer.Normalize(RawSample{Timestamp: now, Device: device})
		if err != nil {
			t.Fatal(err)
		}
	}
	got := normalizer.Devices()
	want := []string{"a0", "m0", "z0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("devices = %v, want %v", got, want)
	}
	got[0] = "mutated"
	if reflect.DeepEqual(got, normalizer.Devices()) {
		t.Fatal("Devices returned internal storage")
	}
}

func TestNormalizeAbsoluteDriverAliases(t *testing.T) {
	tests := []struct {
		name    string
		driver  string
		stats   map[string]uint64
		counter Counter
		want    uint64
	}{
		{
			name:    "mellanox out of buffer",
			driver:  "mlx5_core",
			stats:   map[string]uint64{"rx_out_of_buffer": 12},
			counter: CounterRXDropped,
			want:    12,
		},
		{
			name:    "intel i40e dma starvation",
			driver:  "i40e",
			stats:   map[string]uint64{"rx-no-dma-resources": 8},
			counter: CounterRXMissed,
			want:    8,
		},
		{
			name:    "intel ixgbe queue restart",
			driver:  "ixgbe",
			stats:   map[string]uint64{"tx restart queue": 3},
			counter: CounterTXTimeout,
			want:    3,
		},
		{
			name:    "broadcom oom discard",
			driver:  "bnxt_en",
			stats:   map[string]uint64{"rx_oom_discards": 5},
			counter: CounterRXMissed,
			want:    5,
		},
		{
			name:    "virtio rx queue drop",
			driver:  "virtio_net",
			stats:   map[string]uint64{"rx_queue_0_xdp_drops": 7},
			counter: CounterRXNoBuf,
			want:    7,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values, sources := normalizeAbsolute(test.driver, nil, test.stats)
			if got := values[test.counter]; got != test.want {
				t.Fatalf("normalized value = %d, want %d; all=%v", got, test.want, values)
			}
			if len(sources[test.counter]) == 0 {
				t.Fatalf("missing source for %s", test.counter)
			}
		})
	}
}

func TestNormalizeAbsolutePrefersEthtoolOverSysfs(t *testing.T) {
	values, sources := normalizeAbsolute(
		"i40e",
		map[string]uint64{"rx_missed_errors": 100},
		map[string]uint64{"rx_no_dma_resources": 7},
	)
	if got := values[CounterRXMissed]; got != 7 {
		t.Fatalf("rx missed = %d, want ethtool value 7", got)
	}
	if want := []string{"ethtool:rx_no_dma_resources"}; !reflect.DeepEqual(sources[CounterRXMissed], want) {
		t.Fatalf("sources = %v, want %v", sources[CounterRXMissed], want)
	}
}

func TestNormalizeAbsoluteFallsBackToSysfs(t *testing.T) {
	values, sources := normalizeAbsolute(
		"unknown_driver",
		map[string]uint64{"rx_dropped": 11, "tx_errors": 4},
		nil,
	)
	if values[CounterRXDropped] != 11 || values[CounterTXErrors] != 4 {
		t.Fatalf("values = %v", values)
	}
	if got := sources[CounterRXDropped]; !reflect.DeepEqual(got, []string{"sysfs:rx_dropped"}) {
		t.Fatalf("rx sources = %v", got)
	}
}

func TestCanonicalCounterName(t *testing.T) {
	tests := map[string]string{
		" RX-Dropped ":        "rx_dropped",
		"tx.queue stopped":    "tx_queue_stopped",
		"__rx__crc__errors__": "rx_crc_errors",
		"rx no buffer":        "rx_no_buffer",
	}
	for input, want := range tests {
		if got := canonicalCounterName(input); got != want {
			t.Errorf("canonicalCounterName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHardwareSampleTotalAvoidsGenericRXDoubleCounting(t *testing.T) {
	sample := HardwareSample{Delta: map[Counter]uint64{
		CounterRXDropped: 100,
		CounterRXMissed:  4,
		CounterRXErrors:  2,
		CounterRXNoBuf:   1,
	}}
	if got := sample.Total(DirectionRX); got != 7 {
		t.Fatalf("Total(RX) = %d, want specific counters only (7)", got)
	}
	sample.Delta[CounterRXMissed] = 0
	sample.Delta[CounterRXErrors] = 0
	sample.Delta[CounterRXNoBuf] = 0
	if got := sample.Total(DirectionRX); got != 100 {
		t.Fatalf("Total(RX) fallback = %d, want rx_dropped (100)", got)
	}
}

func TestAllCountersReturnsCopy(t *testing.T) {
	first := AllCounters()
	second := AllCounters()
	if len(first) == 0 {
		t.Fatal("AllCounters is empty")
	}
	first[0] = "mutated"
	if second[0] == first[0] {
		t.Fatal("AllCounters returned shared backing storage")
	}
}
