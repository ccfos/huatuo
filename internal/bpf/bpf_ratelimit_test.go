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

package bpf

import "testing"

func TestRateLimiterApplyConstants(t *testing.T) {
	t.Parallel()

	limiter := NewRateLimiter("tcp_retransmit")

	t.Run("disabled returns original map", func(t *testing.T) {
		t.Parallel()

		consts := map[string]any{"existing": uint64(7)}
		got := limiter.ApplyConstants(consts, 0)

		if len(got) != 1 || got["existing"] != uint64(7) {
			t.Fatalf("constants = %#v, want original map only", got)
		}
	})

	t.Run("enabled initializes nil map", func(t *testing.T) {
		t.Parallel()

		got := limiter.ApplyConstants(nil, 100)

		if got["bpf_rlimit_interval_tcp_retransmit"] != uint64(1) {
			t.Fatalf("interval = %v, want 1", got["bpf_rlimit_interval_tcp_retransmit"])
		}
		if got["bpf_rlimit_burst_tcp_retransmit"] != uint64(100) {
			t.Fatalf("burst = %v, want 100", got["bpf_rlimit_burst_tcp_retransmit"])
		}
		if got["bpf_rlimit_max_burst_tcp_retransmit"] != uint64(0) {
			t.Fatalf("max burst = %v, want 0", got["bpf_rlimit_max_burst_tcp_retransmit"])
		}
	})

	t.Run("enabled preserves existing constants", func(t *testing.T) {
		t.Parallel()

		consts := map[string]any{"existing": uint64(7)}
		got := limiter.ApplyConstants(consts, 10)

		if got["existing"] != uint64(7) {
			t.Fatalf("existing constant = %v, want 7", got["existing"])
		}
		if got["bpf_rlimit_burst_tcp_retransmit"] != uint64(10) {
			t.Fatalf("burst = %v, want 10", got["bpf_rlimit_burst_tcp_retransmit"])
		}
	})
}
