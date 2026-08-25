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

//go:build !didi

package pod

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
)

func TestCgroupSubsystemInitializationRetries(t *testing.T) {
	oldIDs, oldLoader := cgroupCssID2SubSysNameMap, cgroupSubSysLoader
	t.Cleanup(func() { cgroupCssID2SubSysNameMap, cgroupSubSysLoader = oldIDs, oldLoader })
	cgroupCssID2SubSysNameMap = nil
	calls := 0
	cgroupSubSysLoader = func() error {
		calls++
		if calls == 1 {
			return errors.New("temporary BTF failure")
		}
		cgroupCssID2SubSysNameMap = map[int]string{0: "memory"}
		return nil
	}
	if cgroupInitSubSysIDs() == nil || cgroupInitSubSysIDs() != nil || cgroupInitSubSysIDs() != nil || calls != 2 {
		t.Fatalf("initialization did not retry failure and cache success: calls=%d", calls)
	}
}

type lifecycleReaderTest struct {
	bpf.PerfEventReader
	event  *containerCssPerfEvent
	closed chan struct{}
	once   sync.Once
	fail   bool
}

func (r *lifecycleReaderTest) ReadInto(dst any) error {
	if r.fail {
		return errors.New("reader failed")
	}
	if r.event != nil {
		*dst.(*containerCssPerfEvent) = *r.event
		r.event = nil
		return nil
	}
	<-r.closed
	return context.Canceled
}

func (r *lifecycleReaderTest) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func TestCgroupReaderRecoversAndStops(t *testing.T) {
	s := lifecycleTestSubscriber(t, 0)
	id := strings.Repeat("a", 64)
	event := &containerCssPerfEvent{Operation: abi.CgroupCSSOperationUpdate}
	copy(event.KnodeName[:], id)
	first := &lifecycleReaderTest{closed: make(chan struct{}), fail: true}
	next := &lifecycleReaderTest{closed: make(chan struct{}), event: event}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseCgroupReader(ctx, first, func() (bpf.PerfEventReader, error) { return next, nil })
	}()
	select {
	case change := <-s.changes:
		if change.ContainerID != id || !s.TakeResync() {
			t.Fatal("missing recovered event or resync")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reader did not recover")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader did not stop")
	}
}

func lifecycleTestSubscriber(t testing.TB, hierarchy int32) *MemoryCgroupSubscription {
	t.Helper()
	s := &MemoryCgroupSubscription{
		changes:   make(chan MemoryCgroupChange, cgroupChangeQueueSize),
		hierarchy: hierarchy, wake: func() {},
	}
	memoryCgroupSubscribersMu.Lock()
	memoryCgroupSubscribers[s] = struct{}{}
	memoryCgroupSubscribersMu.Unlock()
	t.Cleanup(func() {
		memoryCgroupSubscribersMu.Lock()
		delete(memoryCgroupSubscribers, s)
		memoryCgroupSubscribersMu.Unlock()
	})
	return s
}

func TestMemoryCgroupNotifications(t *testing.T) {
	s := lifecycleTestSubscriber(t, 7)
	id := strings.Repeat("a", 64)
	data := containerCssPerfEvent{CgroupRoot: 7, Operation: abi.CgroupCSSOperationUpdate}
	copy(data.KnodeName[:], "cri-containerd-"+id+".scope")
	publishMemoryCgroupChange(&data)
	if got := <-s.Changes(); got.ContainerID != id || got.Removed {
		t.Fatalf("create = %+v", got)
	}
	data.Operation = abi.CgroupCSSOperationRemove
	publishMemoryCgroupChange(&data)
	if got := <-s.Changes(); got.ContainerID != id || !got.Removed {
		t.Fatalf("remove = %+v", got)
	}
	data.CgroupRoot = 8
	publishMemoryCgroupChange(&data)
	data.CgroupRoot = 7
	data.KnodeName[0] = 0
	publishMemoryCgroupChange(&data)
	if len(s.changes) != 0 {
		t.Fatal("published a non-memory or unidentified event")
	}
}

func TestMemoryCgroupQueueFullDoesNotBlock(t *testing.T) {
	s := lifecycleTestSubscriber(t, 0)
	data := containerCssPerfEvent{Operation: abi.CgroupCSSOperationUpdate}
	copy(data.KnodeName[:], strings.Repeat("a", 64))
	for i := 0; i < cgroupChangeQueueSize+1; i++ {
		publishMemoryCgroupChange(&data)
	}
	if len(s.changes) != cgroupChangeQueueSize {
		t.Fatal("unexpected queue size")
	}
	if !s.TakeResync() || s.TakeResync() {
		t.Fatal("overflow recovery request was missing or not consumed")
	}
	resyncMemoryCgroups()
	if !s.TakeResync() {
		t.Fatal("loss during recovery was not retained")
	}
	// Recovery bypasses the full event queue.
	for len(s.changes) > 0 {
		if (<-s.changes).ContainerID == "" {
			t.Fatal("unexpected recovery event")
		}
	}
}

func BenchmarkMemoryCgroupNotification(b *testing.B) {
	s := lifecycleTestSubscriber(b, 0)
	data := containerCssPerfEvent{Operation: abi.CgroupCSSOperationUpdate}
	copy(data.KnodeName[:], strings.Repeat("a", 64))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		publishMemoryCgroupChange(&data)
		<-s.changes
	}
}
