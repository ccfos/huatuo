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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestContainerLifecycleSubscriptionClose(t *testing.T) {
	oldLastUpdatedAt := lastUpdatedAt
	t.Cleanup(func() { lastUpdatedAt = oldLastUpdatedAt })
	lastUpdatedAt = time.Now()

	var calls atomic.Uint64
	subscription := SubscribeContainerLifecycle(func() {
		calls.Add(1)
	})

	notifyContainerLifecycle()
	assert.Equal(t, uint64(1), calls.Load())
	assert.True(t, lastUpdatedAt.IsZero())

	subscription.Close()
	notifyContainerLifecycle()
	assert.Equal(t, uint64(1), calls.Load())
}

func TestContainerLifecycleSubscriptionCloseWaitsForCallback(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	subscription := SubscribeContainerLifecycle(func() {
		close(started)
		<-release
	})

	notified := make(chan struct{})
	go func() {
		notifyContainerLifecycle()
		close(notified)
	}()
	<-started

	closed := make(chan struct{})
	go func() {
		subscription.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while the callback was running")
	case <-time.After(10 * time.Millisecond):
	}

	close(release)
	<-notified
	<-closed
}
