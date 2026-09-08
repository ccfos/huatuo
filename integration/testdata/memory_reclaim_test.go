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

//go:build integration && linux

package observability_test

import (
	"context"
	"os"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
)

// This is an attach smoke test, not a global-memory-pressure trigger test.
func TestReclaimLiveAttach(t *testing.T) {
	dir := os.Getenv("HUATUO_TRIGGER_BPF_DIR")
	if dir == "" {
		t.Skip("set HUATUO_TRIGGER_BPF_DIR on a test VM")
	}
	if err := bpf.Init(nil); err != nil {
		t.Fatal(err)
	}
	defer bpf.Shutdown()
	previous := bpf.DefaultObjDir
	bpf.DefaultObjDir = dir
	defer func() { bpf.DefaultObjDir = previous }()
	obj, err := bpf.LoadBPF("memory_reclaim_events.o", map[string]any{
		"reclaim_duration_threshold_ns": uint64(900000000),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, err := obj.AttachAndEventPipe(ctx, "reclaim_perf_events", bpf.DefaultPerfEventBufferBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	t.Log("existing reclaim probes attached; no global memory pressure generated")
}
