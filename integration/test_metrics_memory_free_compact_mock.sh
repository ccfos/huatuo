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
source "${ROOT_DIR}/integration/bpf_mock/common/lib.sh"
skip_known_mock_kprobe_kernel
require_commands make gcc insmod rmmod jq cmp
require_bpf_mock_platform
readonly compact_source="${ROOT_DIR}/integration/bpf_mock/memory_free_compact"
readonly compact_trigger="${HUATUO_BAMAI_TEST_TMPDIR}/compact-trigger"
fixture_dir="${HUATUO_BAMAI_TEST_TMPDIR}/module"
build_bpf_mock_module memory_free_compact "${fixture_dir}" "${ROOT_DIR}/integration/bpf_mock/common/kernel_hook.awk" "${compact_source}/memory_free_compact_event.awk" "${compact_source}/memory_free_compact_trace.h"
compile_user_fixture "${compact_source}/memory_free_compact_trigger.c" "${compact_trigger}" -pthread
loaded_module=""
trap 'cleanup_bpf_mock "$?" "${loaded_module}" || exit $?' EXIT
load_bpf_mock_module memory_free_compact "${fixture_dir}"
trace_events=/sys/kernel/tracing/events
[[ -d ${trace_events} ]] || trace_events=/sys/kernel/debug/tracing/events
for event in mm_vmscan_direct_reclaim_begin mm_vmscan_direct_reclaim_end; do
	for suffix in "" _mock; do
		awk '/^[[:space:]]*field:/' "${trace_events}/vmscan/${event}${suffix}/format" > "${HUATUO_BAMAI_TEST_TMPDIR}/${event}${suffix}.fields"
	done
	cmp "${HUATUO_BAMAI_TEST_TMPDIR}/${event}.fields" "${HUATUO_BAMAI_TEST_TMPDIR}/${event}_mock.fields" || fatal "reclaim tracepoint layout differs"
done
write_compact_config() { write_single_event_config memory_free ""; }
compact_has_samples() {
	huatuo_bamai_metrics > "${compact_case}/after.metrics" || return 1
	awk '/^huatuo_bamai_memory_free_(compaction_stall|allocpages_stall)\{/ && $2 >= 40 { count++ } END { exit count != 2 }' "${compact_case}/after.metrics"
}
for first in reclaim compaction; do
	compact_case="${HUATUO_BAMAI_TEST_TMPDIR}/${first}-first"
	mkdir -p "${compact_case}"
	integration_huatuo_bamai_start write_compact_config --region dev --disable-kubelet --log-debug --bpf-mock
	await_bpf_mock_attached "" kprobe:try_to_compact_pages_mock \
		tracepoint:vmscan/mm_vmscan_direct_reclaim_begin_mock tracepoint:vmscan/mm_vmscan_direct_reclaim_end_mock
	wait_until 20 0.2 huatuo_bamai_await_metrics
	huatuo_bamai_metrics > "${compact_case}/before.metrics"
	order=(reclaim compaction)
	[[ ${first} == compaction ]] && order=(compaction reclaim)
	# An unmatched return must leave both the map and subsequent paired calls intact.
	"${compact_trigger}" unmatched-end > "${compact_case}/unmatched-before.json"
	huatuo_bamai_metrics > "${compact_case}/unmatched-before.metrics"
	for scenario in "${order[@]}"; do
		for iteration in 1 2; do
			"${compact_trigger}" "${scenario}" > "${compact_case}/${scenario}-${iteration}.json"
		done
	done
	wait_until 30 0.5 compact_has_samples || fatal "compaction metrics did not update"
	cp "${compact_case}/after.metrics" "${compact_case}/paired.metrics"
	"${compact_trigger}" unmatched-end > "${compact_case}/unmatched-after.json"
	huatuo_bamai_metrics > "${compact_case}/after.metrics"
	run_bpf_mock_checker memory_free_compact "${compact_case}"
	stop_bpf_mock_daemon
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${compact_case}/huatuo.log"
done
