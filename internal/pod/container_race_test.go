// Copyright 2025, 2026 The HuaTuo Authors
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
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf/abi"
)

func TestContainerViewConcurrentCommitDrainAndClose(t *testing.T) {
	store := newTestContainerStore()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ctx, cancel := context.WithCancel(t.Context())
				s, err := store.subscribe(ctx)
				if err != nil {
					t.Error(err)
					cancel()
					return
				}
				_, _ = s.DrainEvents()
				cancel()
				s.Close()
			}
		}()
	}
	for i := 0; i < 100; i++ {
		record := containerRecordForTest(fmt.Sprintf("%064x", i))
		store.commit(containerSnapshot{records: map[string]*containerRecord{record.ref.Key.ID: record}, isComplete: true}, nil)
	}
	wg.Wait()
	if len(store.subscribers) != 0 {
		t.Fatal("closed subscriptions retained")
	}
}

// TestPodViewsConcurrentRefresh 验证公共单对象和列表查询与连续提交并发时，
// 每个只读视图的名称和标签来自同一次提交，不会读到正在原地修改的元数据。
func TestPodViewsConcurrentRefresh(t *testing.T) {
	previous := containerView
	store := newTestContainerStore()
	containerView = store
	t.Cleanup(func() { containerView = previous })
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			view, err := PodByID("checkout-sandbox")
			if err != nil {
				t.Error(err)
				return
			}
			if view != nil && view.Name != view.Labels["revision"] {
				t.Errorf("inconsistent single view: name=%q, revision=%v", view.Name, view.Labels["revision"])
				return
			}
			all, err := Pods()
			if err != nil {
				t.Error(err)
				return
			}
			for _, view := range all {
				if view.Name != view.Labels["revision"] {
					t.Errorf("inconsistent list view: name=%q, revision=%v", view.Name, view.Labels["revision"])
					return
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop); <-done })
	for index := range 100 {
		name := fmt.Sprintf("checkout-revision-%d", index)
		record := &containerRecord{pod: &Pod{
			ID: "checkout-sandbox", Type: TypeCubeSandbox, Name: name,
			Labels: map[string]any{"revision": name},
		}}
		store.commit(containerSnapshot{records: map[string]*containerRecord{record.pod.ID: record}, isComplete: true}, nil)
	}
}

// TestPodCgroupIndexConcurrentCommit 验证资源索引更新与内核事件查询并发时，
// 只能返回当前 workload 或未知，不会读到残留归属；删除后索引清空。
func TestPodCgroupIndexConcurrentCommit(t *testing.T) {
	store := newTestContainerStore()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		event := &containerCssPerfEvent{Operation: abi.CgroupCSSOperationUpdate}
		event.CSS[0] = 7500
		for {
			select {
			case <-stop:
				return
			default:
			}
			if id := store.cgroupEvent(event); id != "" && id != "checkout-sandbox" {
				t.Errorf("unexpected concurrent owner %q", id)
				return
			}
		}
	}()
	for index := range 100 {
		record := &containerRecord{pod: &Pod{ID: "checkout-sandbox", Type: TypeCubeSandbox, Cgroups: CgroupBinding{
			CSS: map[string]uint64{"cpu": uint64(7500 + index%2)}, directories: map[string]string{"cpu": "/bound-checkout"},
		}}}
		store.commit(containerSnapshot{records: map[string]*containerRecord{record.pod.ID: record}, isComplete: true}, nil)
	}
	store.commit(containerSnapshot{isComplete: true}, nil)
	close(stop)
	<-done
	if len(store.cgroupOwners)+len(store.cssOwners) != 0 {
		t.Error("concurrent deletion retained cgroup ownership")
	}
}
