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

//go:build integration && !didi

package pod

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Exercise HTTP decoding, the active producer and real /proc/cgroup identity.
// CSS probe delivery and memorywatch kernel delivery have separate kernel tests.
func TestContainerControllerHTTPAndKernelBinding(t *testing.T) {
	useContainerdForTest(t)
	id := strings.Repeat("a", 64)
	runtimeRoot := t.TempDir()
	runtimeDir := filepath.Join(runtimeRoot, "io.containerd.runtime.v2.task", "k8s.io", id)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "init.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	initMu.Lock()
	previousRoot := containerdStateDir
	containerdStateDir = runtimeRoot
	initMu.Unlock()
	t.Cleanup(func() { initMu.Lock(); containerdStateDir = previousRoot; initMu.Unlock() })
	var mu sync.Mutex
	list := runningPodListForTest(id)
	list.Items[0].Status.Phase = corev1.PodRunning
	deleting := false
	staleFetched := make(chan struct{})
	var staleOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if err := json.NewEncoder(w).Encode(list); err != nil {
			t.Error(err)
		}
		if deleting && len(list.Items) != 0 {
			staleOnce.Do(func() { close(staleFetched) })
		}
	}))
	t.Cleanup(server.Close)
	store := newContainerStore()
	previousView := containerView
	containerView = store
	t.Cleanup(func() { containerView = previousView })
	controller := newContainerController(store)
	controller.resyncInterval = 25 * time.Millisecond
	controller.fetch = func(ctx context.Context) (corev1.PodList, error) {
		return kubeletFetchPodList(ctx, server.Client(), server.URL)
	}

	controller.start()
	t.Cleanup(func() { controller.cancel(); <-controller.done })
	sub := subscribeForTest(t, store)
	waitContainerViewForTest(t, sub, 1)
	store.mu.RLock()
	record := store.records[id]
	store.mu.RUnlock()
	if record.ref.InitPID != os.Getpid() || record.startTime == 0 || record.directory == nil {
		t.Fatal("kernel binding missing")
	}
	// Runtime teardown and its CSS hint can precede kubelet's next status.
	if err := os.Remove(filepath.Join(runtimeDir, "init.pid")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	deleting = true
	mu.Unlock()
	controller.request(id, true)
	select {
	case <-staleFetched:
	case <-time.After(2 * time.Second):
		t.Fatal("deletion hint did not fetch the stale kubelet status")
	}
	mu.Lock()
	list = corev1.PodList{}
	mu.Unlock()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-sub.Notify():
		case <-timeout:
			t.Fatal("HTTP deletion did not reach subscription")
		}
		update, err := sub.DrainEvents()
		if err != nil {
			t.Fatal(err)
		}
		if len(update.Events) == 1 && update.Events[0].Kind == ContainerDeleted && update.Events[0].Container.Key == record.ref.Key {
			if cached, err := ContainerByID(id); err != nil || cached != nil {
				t.Fatalf("deleted container remains available to the API: cached=%v, err=%v", cached, err)
			}
			return
		}
	}
}
