#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail
source "${ROOT_DIR}/integration/lib_observability.sh"
observability_component=memory_reclaim_events
integration_huatuo_bamai_start write_observability_config \
	--region integration --disable-kubelet --log-debug --bpf-dir "${ROOT_DIR}/_output/bpf"
observability_reclaim_attached() {
	grep -F 'map_name="reclaim_perf_events"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" | grep -Fq 'msg="attached BPF and created event pipe"'
}
wait_until 30 .1 observability_reclaim_attached || fatal "reclaim probes did not attach"
assert_memsnapshot_tracing_metrics memory_reclaim_events integration 1
assert_observability_reclaim_metric() {
	huatuo_bamai_collect_metrics
	awk '
 /^huatuo_bamai_memory_reclaim_events_unresolved_suppressed_total\{/ {
   found++; if ($NF !~ /^[0-9]+$/) exit 1
 }
 END { if (found != 1) exit 1 }
' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" || fatal "reclaim suppression counter missing or invalid"
}
assert_observability_reclaim_metric
assert_observability_stop
log_info "PASS: reclaim attachment and exported counter; event pressure/persistence not exercised"
