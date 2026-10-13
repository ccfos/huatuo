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
source "${ROOT_DIR}/integration/lib_bpf_mock.sh"
source "${ROOT_DIR}/integration/bpf_mock/common/elasticsearch.sh"

if kernel_version_le 4 17; then
	skip "hungtask fixture requires Linux 4.18 or newer"
fi
case $(uname -m) in
x86_64 | aarch64) ;;
*) skip "hungtask fixture requires x86_64 or aarch64" ;;
esac
require_commands make gcc insmod rmmod jq
[[ -r /proc/sys/kernel/hung_task_timeout_secs ]] \
	|| skip "hung task detection is disabled in this kernel build"
[[ -r "${ROOT_DIR}/_output/bpf/system_hungtask.o" ]] \
	|| fatal "system_hungtask.o is missing; run make build"

readonly hungtask_event_file="${HUATUO_BAMAI_TEST_TMPDIR}/es-hungtask-events.json"
readonly hungtask_comm="hungtask-mock"
readonly hungtask_trigger="${HUATUO_BAMAI_TEST_TMPDIR}/${hungtask_comm}"

kernel_build_dir=${KERNEL_BUILD_DIR:-/lib/modules/$(uname -r)/build}
[[ -d "${kernel_build_dir}" ]] \
	|| skip "kernel build directory is missing: ${kernel_build_dir}"

fixture_dir=${HUATUO_BAMAI_TEST_TMPDIR}/hungtask_mock_module
mkdir -p "${fixture_dir}"
cp "${ROOT_DIR}/integration/bpf_mock/hungtask/Makefile" \
	"${ROOT_DIR}/integration/bpf_mock/hungtask/hungtask_mock_kmod.c" \
	"${ROOT_DIR}/integration/bpf_mock/hungtask/hungtask_mock.h" \
	"${ROOT_DIR}/integration/bpf_mock/hungtask/hungtask_trace.h" \
	"${fixture_dir}/"

make -C "${fixture_dir}" KERNEL_BUILD_DIR="${kernel_build_dir}" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/hungtask-mock-build.log" 2>&1 \
	|| fatal "hungtask fixture module build failed; see hungtask-mock-build.log"

compile_user_fixture "${ROOT_DIR}/integration/bpf_mock/hungtask/trigger.c" "${hungtask_trigger}" -pthread

module_loaded=0
cleanup() {
	local status=$?
	huatuo_bamai_stop || status=1
	if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
		storage_dump_logs || true
	fi
	stop_bpf_mock_es || status=1
	if [[ ${module_loaded} == 1 ]]; then
		rmmod huatuo_hungtask_mock || status=1
	fi
	return "${status}"
}
trap 'cleanup || exit $?' EXIT
start_bpf_mock_es
insmod "${fixture_dir}/huatuo_hungtask_mock.ko"
module_loaded=1

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

wait_until 5 0.1 test -e /dev/huatuo_hungtask_mock \
	|| fatal "hung task fixture device /dev/huatuo_hungtask_mock did not appear"

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

	wait_until 15 0.1 \
		grep -q 'attached BPF tracepoint.*attach_target="sched/sched_process_hang_mock"' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "hung task BPF program did not attach to mock tracepoint"
	wait_until 15 0.1 \
		grep -q 'attached BPF and created event pipe.*map_name="hungtask_perf_events"' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "hung task perf event pipe did not start"
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

es_events_are_complete() {
	read_bpf_mock_es_events "${hungtask_event_file}" || return 1
	jq -se 'length == 2' "${hungtask_event_file}" > /dev/null
}
wait_until 30 0.5 es_events_are_complete || fatal "expected exactly two ES hungtask documents"
run_bpf_mock_checker hungtask "${hungtask_event_file}" "${HUATUO_BAMAI_TEST_TMPDIR}" "${hungtask_timeout}"
log_info "mock hungtask passed: main and worker ES documents verified"
