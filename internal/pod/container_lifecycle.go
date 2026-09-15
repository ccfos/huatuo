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

package pod

import (
	"sync"
	"time"
)

var containerLifecycleSubscribers = struct {
	sync.Mutex
	nextID      uint64
	subscribers map[uint64]*ContainerLifecycleSubscription
}{
	subscribers: make(map[uint64]*ContainerLifecycleSubscription),
}

// ContainerLifecycleSubscription receives notifications after cgroup CSS
// metadata has been created or removed.
type ContainerLifecycleSubscription struct {
	id       uint64
	callback func()

	mu     sync.Mutex
	closed bool
	active sync.WaitGroup
	once   sync.Once
}

// SubscribeContainerLifecycle registers a cgroup lifecycle callback.
func SubscribeContainerLifecycle(
	callback func(),
) *ContainerLifecycleSubscription {
	if callback == nil {
		return nil
	}

	subscription := &ContainerLifecycleSubscription{
		callback: callback,
	}
	containerLifecycleSubscribers.Lock()
	containerLifecycleSubscribers.nextID++
	subscription.id = containerLifecycleSubscribers.nextID
	containerLifecycleSubscribers.subscribers[subscription.id] = subscription
	containerLifecycleSubscribers.Unlock()
	return subscription
}

func (subscription *ContainerLifecycleSubscription) invoke() {
	subscription.mu.Lock()
	if subscription.closed {
		subscription.mu.Unlock()
		return
	}
	subscription.active.Add(1)
	subscription.mu.Unlock()

	defer subscription.active.Done()
	subscription.callback()
}

// Close unregisters the callback and waits for an invocation already running.
func (subscription *ContainerLifecycleSubscription) Close() {
	if subscription == nil {
		return
	}
	subscription.once.Do(func() {
		containerLifecycleSubscribers.Lock()
		delete(containerLifecycleSubscribers.subscribers, subscription.id)
		containerLifecycleSubscribers.Unlock()

		subscription.mu.Lock()
		subscription.closed = true
		subscription.mu.Unlock()
		subscription.active.Wait()
	})
}

func notifyContainerLifecycle() {
	// The CSS event has changed the source data used by the next kubelet
	// synchronization. Invalidate the five-second container cache before
	// waking subscribers so their next Containers call observes the change.
	containersMapLock.Lock()
	lastUpdatedAt = time.Time{}
	containersMapLock.Unlock()

	containerLifecycleSubscribers.Lock()
	subscriptions := make([]*ContainerLifecycleSubscription, 0,
		len(containerLifecycleSubscribers.subscribers))
	for _, subscription := range containerLifecycleSubscribers.subscribers {
		subscriptions = append(subscriptions, subscription)
	}
	containerLifecycleSubscribers.Unlock()

	for _, subscription := range subscriptions {
		subscription.invoke()
	}
}
