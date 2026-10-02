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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// restoreKubeletRuntimeState restores the shared kubelet cache when the test ends.
func restoreKubeletRuntimeState(t *testing.T) {
	t.Helper()

	previous := kubeletRuntimeSnapshot()
	t.Cleanup(func() {
		updateKubeletRuntimeState(func(state *kubeletRuntimeState) {
			*state = previous
		})
	})
}

// TestKubeletRuntimeStateIsCoherentUnderConcurrentAccess covers the two sides of
// the kubelet runtime cache: the retry goroutine that InitManager starts once
// kubelet answers republishes the pod list client, URL and enabled flag while the
// metric collectors read them. A reader must always observe a complete cache — an
// enabled cache always carries the client and URL published with it — and, with
// -race, no unsynchronized access.
func TestKubeletRuntimeStateIsCoherentUnderConcurrentAccess(t *testing.T) {
	restoreKubeletRuntimeState(t)

	const (
		writers        = 2
		readers        = 4
		readIterations = 2000
	)

	stop := make(chan struct{})
	var writerGroup, readerGroup sync.WaitGroup

	for writer := 0; writer < writers; writer++ {
		writerGroup.Add(1)
		go func(seed int) {
			defer writerGroup.Done()

			for round := 0; ; round++ {
				select {
				case <-stop:
					return
				default:
				}

				client := &http.Client{}
				updateKubeletRuntimeState(func(state *kubeletRuntimeState) {
					state.podListURL = fmt.Sprintf("http://127.0.0.1:%d/pods", seed+round)
					state.podListClient = client
					state.podListRunningEnabled = true
				})
			}
		}(writer)
	}

	for reader := 0; reader < readers; reader++ {
		readerGroup.Add(1)
		go func() {
			defer readerGroup.Done()

			for read := 0; read < readIterations; read++ {
				snapshot := kubeletRuntimeSnapshot()

				if snapshot.podCgroupDriver == "" || snapshot.runtimeEndpoint == "" {
					t.Errorf("kubelet cache lost the kubelet config values: driver=%q runtime=%q",
						snapshot.podCgroupDriver, snapshot.runtimeEndpoint)
					return
				}
				if !snapshot.podListRunningEnabled {
					continue
				}
				if snapshot.podListClient == nil || snapshot.podListURL == "" {
					t.Errorf("enabled kubelet cache without client or URL: client=%v url=%q",
						snapshot.podListClient, snapshot.podListURL)
					return
				}
			}
		}()
	}

	readerGroup.Wait()
	close(stop)
	writerGroup.Wait()
}

// TestKubeletGetPodListUsesPublishedCache drives the cache through the real reads
// and writes: while kubelet is unknown the pod list is rejected, and once the
// retry path publishes a reachable kubelet the same call returns its response.
func TestKubeletGetPodListUsesPublishedCache(t *testing.T) {
	restoreKubeletRuntimeState(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pods" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(corev1.PodList{})
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	port, err := strconv.ParseUint(parsed.Port(), 10, 32)
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}

	updateKubeletRuntimeState(func(state *kubeletRuntimeState) {
		state.podListRunningEnabled = false
		state.podListClient = nil
		state.podListURL = ""
	})

	if _, err := kubeletGetPodList(t.Context()); err == nil {
		t.Fatal("kubeletGetPodList() with an unpublished cache returned no error")
	}

	managerCtx := &ManagerCtx{
		PodReadOnlyPort:   uint32(port),
		PodAuthorizedPort: uint32(port),
	}
	if err := kubeletPodListPortCacheUpdate(t.Context(), managerCtx); err != nil {
		t.Fatalf("kubeletPodListPortCacheUpdate() error = %v", err)
	}

	pods, err := kubeletGetPodList(t.Context())
	if err != nil {
		t.Fatalf("kubeletGetPodList() after the cache update error = %v", err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("kubeletGetPodList() returned %d pods, want 0", len(pods.Items))
	}
}
