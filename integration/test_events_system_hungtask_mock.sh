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

require_bpf_mock_platform
require_commands make gcc insmod rmmod jq
[[ -r /proc/sys/kernel/hung_task_timeout_secs ]] \
	|| skip "hung task detection is disabled in this kernel build"
[[ -r "${ROOT_DIR}/_output/bpf/system_hungtask.o" ]] \
	|| fatal "system_hungtask.o is missing; run make build"

readonly hungtask_event_file="${HUATUO_BAMAI_TEST_TMPDIR}/es-hungtask-events.json"
readonly hungtask_comm="hungtask-mock"
readonly hungtask_trigger="${HUATUO_BAMAI_TEST_TMPDIR}/${hungtask_comm}"

fixture_dir=${HUATUO_BAMAI_TEST_TMPDIR}/hungtask_mock_module
build_bpf_mock_module system_hungtask "${fixture_dir}" \
	"${ROOT_DIR}/integration/bpf_mock/system_hungtask/system_hungtask_trace.h"

compile_user_fixture "${ROOT_DIR}/integration/bpf_mock/system_hungtask/system_hungtask_trigger.c" "${hungtask_trigger}" -pthread

loaded_module=""
trap 'cleanup_bpf_mock_es "$?" "${loaded_module}" || exit $?' EXIT
start_bpf_mock_es
load_bpf_mock_module system_hungtask "${fixture_dir}"

write_hungtask_mock_config() {
	write_bpf_mock_es_config hungtask
}

# Compare field offsets and sizes as well as names before exercising the BPF reader.
trace_events=/sys/kernel/tracing/events
if [[ ! -r "${trace_events}/sched/sched_process_hang/format" ]]; then
	trace_events=/sys/kernel/debug/tracing/events
fi
readonly hungtask_real_format="${trace_events}/sched/sched_process_hang/format"
readonly hungtask_mock_format="${trace_events}/sched/sched_process_hang_mock/format"
[[ -r "${hungtask_real_format}" ]] || fatal "production hung task tracepoint format is unavailable; mount tracefs"
wait_until 5 0.1 test -r "${hungtask_mock_format}" \
	|| fatal "mock hung task tracepoint did not appear"
awk '/^[[:space:]]*field:/' "${hungtask_real_format}" > "${HUATUO_BAMAI_TEST_TMPDIR}/hungtask-real-fields"
awk '/^[[:space:]]*field:/' "${hungtask_mock_format}" > "${HUATUO_BAMAI_TEST_TMPDIR}/hungtask-mock-fields"
cmp "${HUATUO_BAMAI_TEST_TMPDIR}/hungtask-real-fields" "${HUATUO_BAMAI_TEST_TMPDIR}/hungtask-mock-fields" \
	|| fatal "mock hung task tracepoint layout differs from production"
readonly hungtask_timeout=$(< /proc/sys/kernel/hung_task_timeout_secs)

hungtask_counter_is() {
	local expected=$1
	huatuo_bamai_collect_metrics || return 1
	awk -v expected="${expected}" '
		/^huatuo_bamai_hungtask_total\{/ { value = $2; found = 1 }
		END { exit !(found && value == expected) }
	' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
# Each task gets the first report from a fresh collector, respecting its backoff.
for scenario in main thread; do
	integration_huatuo_bamai_start write_hungtask_mock_config \
		--region dev --disable-kubelet --log-debug --bpf-mock

	await_bpf_mock_attached hungtask_perf_events tracepoint:sched/sched_process_hang_mock
	wait_until 15 0.1 hungtask_counter_is 0 \
		|| fatal "hungtask counter baseline is not zero"
	args=()
	[[ ${scenario} == thread ]] && args+=(thread)
	"${hungtask_trigger}" "${args[@]}" > "${HUATUO_BAMAI_TEST_TMPDIR}/${scenario}.json"
	wait_until 20 0.2 hungtask_counter_is 1 \
		|| fatal "${scenario} hungtask did not increment the counter to one"
	stop_bpf_mock_daemon
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/${scenario}-huatuo.log"
done

verify_bpf_mock_es_events system_hungtask "${hungtask_event_file}" "${HUATUO_BAMAI_TEST_TMPDIR}" "${hungtask_timeout}"
log_info "mock hungtask passed: main and worker ES documents verified"
