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

package handlers

import (
	"sync"
	"sync/atomic"
	"testing"

	"huatuo-bamai/pkg/tracing"

	"github.com/stretchr/testify/require"
)

func TestEventsHandler_AcquireClientConcurrent(t *testing.T) {
	h := NewEventsHandler(1, 0)
	start := make(chan struct{})
	var acquired atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if h.acquireClient() {
				acquired.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	require.Equal(t, int32(1), acquired.Load())
	require.Equal(t, int32(1), h.activeClients.Load())

	h.releaseClient()
	require.Equal(t, int32(0), h.activeClients.Load())
}

// --- WatchFilters.matcher() ---

func TestWatchFilters_Matcher_Empty(t *testing.T) {
	wf := WatchFilters{}
	m, err := wf.matcher()

	require.NoError(t, err)
	require.NotNil(t, m)
	// empty matcher matches everything
	require.True(t, m.Match(&tracing.Document{TracerName: "any"}))
}

func TestWatchFilters_Matcher_ValidPattern(t *testing.T) {
	wf := WatchFilters{TracerName: "^cpu$"}
	m, err := wf.matcher()

	require.NoError(t, err)
	require.True(t, m.Match(&tracing.Document{TracerName: "cpu"}))
	require.False(t, m.Match(&tracing.Document{TracerName: "mem"}))
}

func TestWatchFilters_Matcher_InvalidPattern(t *testing.T) {
	wf := WatchFilters{TracerName: "[invalid"}
	_, err := wf.matcher()

	require.Error(t, err)
	require.Contains(t, err.Error(), "tracer_name")
}

func TestWatchFilters_Matcher_AllFields(t *testing.T) {
	wf := WatchFilters{
		TracerName:             "cpu",
		Hostname:               "node-1",
		ContainerHostname:      "app",
		ContainerHostNamespace: "prod",
		Region:                 "cn",
	}
	m, err := wf.matcher()

	require.NoError(t, err)

	match := &tracing.Document{
		TracerName:             "cpu",
		Hostname:               "node-1",
		ContainerHostname:      "app-123",
		ContainerHostNamespace: "prod-ns",
		Region:                 "cn-north",
	}
	require.True(t, m.Match(match))

	noMatch := &tracing.Document{
		TracerName:             "mem",
		Hostname:               "node-1",
		ContainerHostname:      "app-123",
		ContainerHostNamespace: "prod-ns",
		Region:                 "cn-north",
	}
	require.False(t, m.Match(noMatch))
}

func TestWatchFilters_Matcher_HostnameFilter(t *testing.T) {
	wf := WatchFilters{Hostname: "^node-[0-9]+$"}
	m, _ := wf.matcher()

	require.True(t, m.Match(&tracing.Document{Hostname: "node-42"}))
	require.False(t, m.Match(&tracing.Document{Hostname: "worker-1"}))
}

func TestWatchFilters_Matcher_ContainerHostnameFilter(t *testing.T) {
	wf := WatchFilters{ContainerHostname: "^app-.*"}
	m, _ := wf.matcher()

	require.True(t, m.Match(&tracing.Document{ContainerHostname: "app-123"}))
	require.False(t, m.Match(&tracing.Document{ContainerHostname: "db-456"}))
}

func TestWatchFilters_Matcher_RegionFilter(t *testing.T) {
	wf := WatchFilters{Region: "^cn"}
	m, _ := wf.matcher()

	require.True(t, m.Match(&tracing.Document{Region: "cn-north"}))
	require.False(t, m.Match(&tracing.Document{Region: "us-east"}))
}
