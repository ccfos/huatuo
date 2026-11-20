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

package collector

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

var (
	errCardListDiscovery = errors.New("card list unavailable")
	errCardOneDiscovery  = errors.New("card 1 device count unavailable")
)

type recordingTopologyProvider struct {
	mu sync.Mutex

	cardListCalls    int
	deviceCountCalls map[int32]int
	cardListFn       func(int) ([]int32, error)
	deviceCountFn    func(int32, int) (int32, error)
}

func (p *recordingTopologyProvider) cardList(context.Context) ([]int32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cardListCalls++
	return p.cardListFn(p.cardListCalls)
}

func (p *recordingTopologyProvider) deviceCount(_ context.Context, cardID int32) (int32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.deviceCountCalls == nil {
		p.deviceCountCalls = make(map[int32]int)
	}
	p.deviceCountCalls[cardID]++
	return p.deviceCountFn(cardID, p.deviceCountCalls[cardID])
}

func (p *recordingTopologyProvider) calls() (int, map[int32]int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	counts := make(map[int32]int, len(p.deviceCountCalls))
	for cardID, count := range p.deviceCountCalls {
		counts[cardID] = count
	}
	return p.cardListCalls, counts
}

func newRecordingTopologyProvider(
	cardList func(int) ([]int32, error),
	deviceCount func(int32, int) (int32, error),
) *recordingTopologyProvider {
	provider := &recordingTopologyProvider{
		cardListFn:    cardList,
		deviceCountFn: deviceCount,
	}
	return provider
}

func TestAscendRefreshCacheRejectsPartialTopology(t *testing.T) {
	provider := newRecordingTopologyProvider(
		func(int) ([]int32, error) { return []int32{0, 1}, nil },
		func(cardID int32, _ int) (int32, error) {
			if cardID == 1 {
				return 0, errCardOneDiscovery
			}
			return 2, nil
		},
	)
	collector := &ascendNpuCollector{topology: provider}

	devices, err := collector.refreshCache(t.Context())
	if !errors.Is(err, errCardOneDiscovery) {
		t.Fatalf("refreshCache() error=%v, want card discovery cause", err)
	}
	if !strings.Contains(err.Error(), "Ascend card 1") {
		t.Fatalf("refreshCache() error=%q, want affected card", err)
	}
	if devices != nil {
		t.Fatalf("refreshCache() devices=%v, want nil after partial failure", devices)
	}
	if cached := collector.cache.Load(); cached != nil {
		t.Fatalf("cache=%v, want no published partial topology", cached.devices)
	}

	cardCalls, deviceCalls := provider.calls()
	if cardCalls != 1 || deviceCalls[0] != 1 || deviceCalls[1] != 1 {
		t.Fatalf("discovery calls=(cards=%d devices=%v), want cards=1 and each device count=1",
			cardCalls, deviceCalls)
	}
}

func TestAscendGetDevicesRetriesIncompleteTopology(t *testing.T) {
	provider := newRecordingTopologyProvider(
		func(int) ([]int32, error) { return []int32{0, 1}, nil },
		func(cardID int32, call int) (int32, error) {
			if cardID == 1 && call == 1 {
				return 0, errCardOneDiscovery
			}
			if cardID == 1 {
				return 2, nil
			}
			return 1, nil
		},
	)
	collector := &ascendNpuCollector{topology: provider}

	if devices, err := collector.getDevices(t.Context()); !errors.Is(err, errCardOneDiscovery) {
		t.Fatalf("first getDevices()=(%v, %v), want discovery failure", devices, err)
	}
	want := []deviceKey{
		{cardId: 0, deviceId: 0},
		{cardId: 1, deviceId: 0},
		{cardId: 1, deviceId: 1},
	}
	got, err := collector.getDevices(t.Context())
	if err != nil {
		t.Fatalf("second getDevices() error=%v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("second getDevices()=%v, want %v", got, want)
	}

	got, err = collector.getDevices(t.Context())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("cached getDevices()=(%v, %v), want (%v, nil)", got, err, want)
	}
	cardCalls, deviceCalls := provider.calls()
	if cardCalls != 2 || deviceCalls[0] != 2 || deviceCalls[1] != 2 {
		t.Fatalf("discovery calls after cache hit=(cards=%d devices=%v), want two attempts only",
			cardCalls, deviceCalls)
	}
}

func TestAscendRefreshCachePublishesCandidateAtomically(t *testing.T) {
	provider := newRecordingTopologyProvider(
		func(call int) ([]int32, error) {
			if call == 1 {
				return []int32{4}, nil
			}
			return []int32{5, 6}, nil
		},
		func(cardID int32, _ int) (int32, error) {
			if cardID == 6 {
				return 0, errCardOneDiscovery
			}
			return 1, nil
		},
	)
	collector := &ascendNpuCollector{topology: provider}

	initial, err := collector.refreshCache(t.Context())
	if err != nil {
		t.Fatalf("initial refreshCache() error=%v", err)
	}
	if _, err := collector.refreshCache(t.Context()); !errors.Is(err, errCardOneDiscovery) {
		t.Fatalf("failed refreshCache() error=%v, want card discovery cause", err)
	}

	cached := collector.cache.Load()
	if cached == nil || !reflect.DeepEqual(cached.devices, initial) {
		t.Fatalf("cache after failed replacement=%v, want prior complete topology %v",
			cached, initial)
	}
	cardCalls, _ := provider.calls()
	if cardCalls != 2 {
		t.Fatalf("card list calls=%d, want 2", cardCalls)
	}
}

func TestAscendCollectorRetriesStartupTopologyFailure(t *testing.T) {
	provider := newRecordingTopologyProvider(
		func(call int) ([]int32, error) {
			if call == 1 {
				return nil, errCardListDiscovery
			}
			return []int32{2}, nil
		},
		func(int32, int) (int32, error) { return 1, nil },
	)
	collector := newAscendNpuCollectorWithTopology(provider)

	if cached := collector.cache.Load(); cached != nil {
		t.Fatalf("startup cache=%v, want nil after discovery failure", cached.devices)
	}
	got, err := collector.getDevices(t.Context())
	if err != nil {
		t.Fatalf("getDevices() retry error=%v", err)
	}
	want := []deviceKey{{cardId: 2, deviceId: 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("getDevices()=%v, want %v", got, want)
	}
	cardCalls, deviceCalls := provider.calls()
	if cardCalls != 2 || deviceCalls[2] != 1 {
		t.Fatalf("discovery calls=(cards=%d devices=%v), want startup failure plus one retry",
			cardCalls, deviceCalls)
	}
}

func TestAscendUpdateReturnsTopologyDiscoveryError(t *testing.T) {
	provider := newRecordingTopologyProvider(
		func(int) ([]int32, error) { return []int32{3}, nil },
		func(int32, int) (int32, error) { return 0, errCardOneDiscovery },
	)
	collector := &ascendNpuCollector{topology: provider}

	metrics, err := collector.Update()
	if !errors.Is(err, errCardOneDiscovery) {
		t.Fatalf("Update() error=%v, want topology discovery cause", err)
	}
	if metrics != nil {
		t.Fatalf("Update() metrics=%v, want nil for incomplete topology", metrics)
	}
}

func BenchmarkAscendGetDevicesCached(b *testing.B) {
	collector := &ascendNpuCollector{}
	collector.cache.Store(&npuCache{devices: []deviceKey{{cardId: 0, deviceId: 0}}})
	ctx := context.Background()

	b.ReportAllocs()
	for range b.N {
		devices, err := collector.getDevices(ctx)
		if err != nil || len(devices) != 1 {
			b.Fatalf("getDevices()=(%v, %v), want one cached device", devices, err)
		}
	}
}
