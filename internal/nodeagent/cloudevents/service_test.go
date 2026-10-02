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

package cloudevents

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestNewRequiresValidDependencies(t *testing.T) {
	store := newTestStore(t)
	tests := []struct {
		name             string
		store            *tracingstore.Store
		maxSubscriptions int
	}{
		{name: "missing store", maxSubscriptions: 1},
		{name: "zero capacity", store: store},
		{name: "negative capacity", store: store, maxSubscriptions: -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.store, test.maxSubscriptions); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestServiceSubscribeEnforcesConcurrentLimit(t *testing.T) {
	service := newTestService(t, 1)
	start := make(chan struct{})
	var acquired atomic.Int32
	var subscription *Subscription
	var subscriptionMu sync.Mutex
	var waitGroup sync.WaitGroup

	for range 64 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			got, err := service.Subscribe(t.Context(), nil)
			if err != nil {
				if !errors.Is(err, ErrLimitExceeded) {
					t.Errorf("Subscribe() error = %v", err)
				}
				return
			}
			acquired.Add(1)
			subscriptionMu.Lock()
			subscription = got
			subscriptionMu.Unlock()
		}()
	}

	close(start)
	waitGroup.Wait()
	if got := acquired.Load(); got != 1 {
		t.Fatalf("successful subscriptions = %d, want 1", got)
	}
	subscription.Close()

	next, err := service.Subscribe(t.Context(), nil)
	if err != nil {
		t.Fatalf("Subscribe() after Close error = %v", err)
	}
	next.Close()
}

func TestServiceSubscribeFiltersAndConvertsEvents(t *testing.T) {
	store := newTestStore(t)
	service, err := New(store, 1)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	subscription, err := service.Subscribe(t.Context(), &Filters{
		TracerName:             "^cpu$",
		Hostname:               "^node-1$",
		ContainerHostname:      "^app-",
		ContainerHostNamespace: "^prod$",
		ContainerQoS:           "^guaranteed$",
		Region:                 "^cn$",
	})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer subscription.Close()

	nonEvent := newTestDocument("cpu", types.TracerRunTypeTracing)
	if err := store.Save(nonEvent); err != nil {
		t.Fatalf("Save() non-event error = %v", err)
	}
	nonMatching := newTestDocument("memory", types.TracerRunTypeEvent)
	if err := store.Save(nonMatching); err != nil {
		t.Fatalf("Save() non-matching event error = %v", err)
	}
	matching := newTestDocument("cpu", types.TracerRunTypeEvent)
	matching.ContainerHostname = "app-1"
	matching.ContainerHostNamespace = "prod"
	matching.ContainerQoS = "guaranteed"
	if err := store.Save(matching); err != nil {
		t.Fatalf("Save() matching event error = %v", err)
	}

	select {
	case event := <-subscription.Events():
		data, ok := event.Data.(types.WatchEventData)
		if !ok {
			t.Fatalf("event data type = %T", event.Data)
		}
		if data.TracerName != "cpu" || data.ContainerHostname != "app-1" {
			t.Errorf("event data = %+v", data)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for matching event")
	}
}

func TestServiceSubscribeRejectsInvalidFilterWithoutConsumingCapacity(t *testing.T) {
	service := newTestService(t, 1)
	if _, err := service.Subscribe(t.Context(), &Filters{TracerName: "[invalid"}); !errors.Is(err, ErrInvalidFilters) {
		t.Fatalf("Subscribe() error = %v, want ErrInvalidFilters", err)
	}

	subscription, err := service.Subscribe(t.Context(), nil)
	if err != nil {
		t.Fatalf("Subscribe() after invalid filter error = %v", err)
	}
	subscription.Close()
}

func newTestService(t *testing.T, maxSubscriptions int) *Service {
	t.Helper()
	service, err := New(newTestStore(t), maxSubscriptions)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return service
}

func newTestStore(t *testing.T) *tracingstore.Store {
	t.Helper()
	store, err := tracingstore.NewFromConfig(t.Context(), tracingstore.Config{})
	if err != nil {
		t.Fatalf("tracingstore.NewFromConfig() error = %v", err)
	}
	return store
}
