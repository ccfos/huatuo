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

package events

import (
	"testing"

	"github.com/ccfos/huatuo/internal/tracing"
)

func TestMemoryOOMKillBlacklist(t *testing.T) {
	// Disable every event so registry validation does not initialize kernel collectors.
	blacklist := []string{
		"dropwatch", "hungtask", "memory_oom_kill", "memory_reclaim_events",
		"net_rx_latency", "netdev_bonding_lacp", "netdev_events",
		"netdev_txqueue_timeout", "ras", "sched_tick", "softlockup", "tcp_retransmit",
	}
	registered, err := tracing.NewRegister(blacklist)
	if err != nil {
		t.Fatalf("initialize blacklisted events: %v", err)
	}
	if len(registered) != 0 {
		t.Fatalf("registered events = %v, want none", registered)
	}
	status := tracing.EventTracingStatus()
	if got := status["memory_oom_kill"]; got != "disabled" {
		t.Errorf("memory_oom_kill status = %q, want disabled", got)
	}
	for _, name := range []string{"oom", "memory_oom"} {
		if _, ok := status[name]; ok {
			t.Errorf("legacy %s tracer is still registered", name)
		}
	}
}
