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

package bpf

import (
	"bytes"
	"os"
	"testing"

	"github.com/ccfos/huatuo/internal/log"

	"github.com/stretchr/testify/require"
)

// TestLogTracingSelectionCarriesTheReason pins both kprobe paths to the log
// line an operator reads. A kernel the probe rules out selects kprobe without
// an attempt, so it never logs the fallback warning, and an unreported reason
// there is indistinguishable from a tracer that stopped covering the hook.
func TestLogTracingSelectionCarriesTheReason(t *testing.T) {
	var buf bytes.Buffer

	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })

	// Direct selection: what a kernel without fentry support reports.
	logTracingSelection("net_rx_latency_fentry.o", []TracingVariantPair{testTracingPair()},
		tracingSelection{
			Mode:   tracingModeKprobe,
			Reason: `bpf: tracing target unsupported: kernel BTF has no function "tcp_v4_rcv"`,
		})
	direct := buf.String()
	buf.Reset()

	require.Contains(t, direct, "loaded BPF with a selected tracing entry point")
	require.Contains(t, direct, `mode="kprobe"`, "the line must name the selected entry point")
	require.Contains(t, direct, `reason="bpf: tracing target unsupported`,
		"a kprobe selection without an attempt must carry its reason")

	// Fallback: the fentry attempt failed and the kprobe needs no further
	// attempt, so the reason names the attempt that failed.
	logTracingSelection("net_rx_latency_fentry.o", []TracingVariantPair{testTracingPair()},
		tracingSelection{Mode: tracingModeKprobe, Reason: errAttemptFailed.Error()})

	fallback := buf.String()
	require.Contains(t, fallback, `mode="kprobe"`)
	require.Contains(t, fallback, `reason="attempt failed"`,
		"a kprobe fallback must carry the error that caused it")
}
