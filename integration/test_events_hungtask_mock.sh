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

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

if kernel_version_le 4 17; then
	skip "hungtask fixture requires Linux 4.18 or newer"
fi
case $(uname -m) in
x86_64 | aarch64) ;;
*) skip "hungtask fixture requires x86_64 or aarch64" ;;
esac
command -v insmod > /dev/null || fatal "insmod is required"
command -v rmmod > /dev/null || fatal "rmmod is required"
command -v jq > /dev/null || fatal "jq is required"
[[ -r /proc/sys/kernel/hung_task_timeout_secs ]] \
	|| skip "hung task detection is disabled in this kernel build"
[[ -r "${ROOT_DIR}/_output/bpf/system_hungtask.o" ]] \
	|| fatal "system_hungtask.o is missing; run make build"

readonly hungtask_event_file="${HUATUO_BAMAI_TEST_TMPDIR}/events/hungtask"
readonly hungtask_valid_event="${HUATUO_BAMAI_TEST_TMPDIR}/hungtask-mock-event.json"
readonly hungtask_comm="hungtask-mock"
readonly hungtask_trigger="${HUATUO_BAMAI_TEST_TMPDIR}/${hungtask_comm}"

kernel_build_dir=${KERNEL_BUILD_DIR:-/lib/modules/$(uname -r)/build}
[[ -d "${kernel_build_dir}" ]] \
	|| fatal "kernel build directory is missing: ${kernel_build_dir}"

fixture_dir=${HUATUO_BAMAI_TEST_TMPDIR}/hungtask_mock_module
mkdir -p "${fixture_dir}"
cp "${ROOT_DIR}/integration/testdata/hungtask_mock/Makefile" \
	"${ROOT_DIR}/integration/testdata/hungtask_mock/huatuo_hungtask_mock.c" \
	"${ROOT_DIR}/integration/testdata/hungtask_mock/hungtask_mock.h" \
	"${ROOT_DIR}/integration/testdata/hungtask_mock/hungtask_trace.h" \
	"${fixture_dir}/"

make -C "${fixture_dir}" KERNEL_BUILD_DIR="${kernel_build_dir}" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/hungtask-mock-build.log" 2>&1 \
	|| fatal "hungtask fixture module build failed; see hungtask-mock-build.log"

compile_user_fixture "${ROOT_DIR}/integration/testdata/hungtask_mock/trigger.c" "${hungtask_trigger}"

cleanup() {
	huatuo_bamai_stop || true
	rmmod huatuo_hungtask_mock || fatal "failed to unload huatuo_hungtask_mock; remove it with rmmod before retrying"
}

insmod "${fixture_dir}/huatuo_hungtask_mock.ko"
trap cleanup EXIT

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

wait_until 5 0.1 test -e /dev/huatuo_hungtask_mock \
	|| fatal "hung task fixture device /dev/huatuo_hungtask_mock did not appear"

integration_huatuo_bamai_start write_hungtask_config \
	--region dev --disable-kubelet --log-debug --bpf-mock

wait_until 15 0.1 \
	grep -q 'attached BPF tracepoint.*attach_target="sched/sched_process_hang_mock"' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "hung task BPF program did not attach to mock tracepoint"
wait_until 15 0.1 \
	grep -q 'attached BPF and created event pipe.*map_name="hungtask_perf_events"' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "hung task perf event pipe did not start"

hungtask_counter_is() {
	local expected=$1
	huatuo_bamai_collect_metrics || return 1
	awk -v expected="${expected}" '
		/^huatuo_bamai_hungtask_total\{/ { value = $2; found = 1 }
		END { exit !(found && value == expected) }
	' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
wait_until 15 0.1 hungtask_counter_is 0 \
	|| fatal "hung task counter baseline is not zero"
[[ ! -s "${hungtask_event_file}" ]] || fatal "unexpected hung task event before the trigger"

"${hungtask_trigger}" &
hungtask_pid=$!
wait "${hungtask_pid}"

hungtask_event_is_valid() {
	jq -s -e --arg comm "${hungtask_comm}" --argjson pid "${hungtask_pid}" \
		--argjson timeout "${hungtask_timeout}" '
		first(.[] | .tracer_data as $data | select(
			.tracer_name == "hungtask"
			and .tracer_type == "event"
			and $data.tid == $pid
			and $data.comm == $comm
			and $data.hung_task_timeout_secs == $timeout
			and ($data.cpus_stack | type) == "string"
			and ($data.blocked_processes_stack | type) == "string"
		))
	' "${hungtask_event_file}" > "${hungtask_valid_event}" 2> /dev/null
}
wait_until 30 0.1 hungtask_event_is_valid \
	|| fatal "no valid hung task event from tid=${hungtask_pid} comm=${hungtask_comm}"
wait_until 20 0.2 hungtask_counter_is 1 \
	|| fatal "hung task counter did not increase to one"

# The collector counts repeated events while its backoff suppresses saved reports.
"${hungtask_trigger}"
wait_until 20 0.2 hungtask_counter_is 2 \
	|| fatal "second hung task trigger did not increment the counter"

# The counter advances before storage; wait for the collector to finish first.
readonly hungtask_daemon_pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
stop_and_wait_by_pid "${hungtask_daemon_pid}" 60 \
	|| fatal "huatuo-bamai did not shut down cleanly"
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
jq -s -e 'length == 1' "${hungtask_event_file}" > /dev/null \
	|| fatal "expected one saved hung task event during collector backoff"

log_info "mock hung task passed: total 0 -> 1 -> 2; repeated report suppressed"
jq '.tracer_data | {tid, comm, hung_task_timeout_secs, cpus_stack_bytes: (.cpus_stack | length), blocked_processes_stack_bytes: (.blocked_processes_stack | length)}' "${hungtask_valid_event}"
