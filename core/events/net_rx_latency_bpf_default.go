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

package events

import (
	"context"
	"strings"

	"github.com/ccfos/huatuo/internal/bpf"
)

// netRxLatencyFentryVariantSuffix is appended to the object named after this
// tracer's source file to name the object built with both tcp_v4_rcv entry
// points.
const netRxLatencyFentryVariantSuffix = "_fentry"

// netRxLatencyTracingPairs pairs the entry points of the tcp_v4_rcv hook. The
// pairing is explicit so the loader cannot select a program that was never
// migrated to an fentry entry point.
func netRxLatencyTracingPairs() []bpf.TracingVariantPair {
	return []bpf.TracingVariantPair{{
		Kprobe: "tcp_v4_rcv_prog",
		Fentry: "tcp_v4_rcv_fentry_prog",
		Target: "tcp_v4_rcv",
	}}
}

// startNetRxLatencyBPF loads the object carrying both entry points, lets the
// loader select the entry point the running kernel supports, and attaches it.
// The returned object is already attached and the reader is already created,
// so the caller must not attach again.
func startNetRxLatencyBPF(
	ctx context.Context,
	bpfName string,
	consts map[string]any,
) (bpf.BPF, bpf.PerfEventReader, error) {
	return bpf.LoadAttachAndEventPipeWithFallback(
		ctx,
		netRxLatencyVariantObj(bpfName),
		consts,
		netRxLatencyTracingPairs(),
		netRxLatencyEventMap,
		bpf.DefaultPerfEventBufferBytes,
	)
}

// netRxLatencyVariantObj maps the object named after the tracer source file to
// the object built with the fentry entry point, e.g. net_rx_latency.o to
// net_rx_latency_fentry.o.
func netRxLatencyVariantObj(bpfName string) string {
	return strings.TrimSuffix(bpfName, ".o") + netRxLatencyFentryVariantSuffix + ".o"
}
