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

# Controlled events exercise the daemon; they do not create real blocked tasks.
set -euo pipefail
source "${ROOT_DIR}/integration/lib_observability.sh"
require_commands python3 systemd-detect-virt
systemd-detect-virt --vm --quiet || skip "hungtask stack capture requires a disposable VM"
[[ -r /proc/sys/kernel/hung_task_timeout_secs ]] || skip "kernel hungtask detection unavailable"
tracepoint_available syscalls sys_enter_getpid || skip "getpid tracepoint unavailable"
observability_component=hungtask
hungtask_attached() {
	grep -F 'map_name="hungtask_perf_events"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" | grep -Fq 'msg="attached BPF and created event pipe"'
}
# Accept only the specific fallback warning, with a matching successful attach.
assert_hungtask_logs() {
	local mode=$1 raw_target=$2 fallback_target=$3
	local log="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	local warning='msg="hungtask container identity unavailable; collecting host events only"'
	local count
	count=$(grep -Ec "level=\"(warn|warning)\" ${warning}" "${log}" || true)
	[[ ${count} -le 1 ]] || fatal "hungtask repeatedly fell back"
	[[ ${mode} != raw || ${count} == 0 ]] || fatal "raw fixture unexpectedly fell back"
	[[ ${mode} != fallback || ${count} == 1 ]] || fatal "fallback was not reported"
	if [[ ${count} == 1 ]]; then
		grep -F 'msg="attached BPF tracepoint"' "${log}" | grep -Fq "attach_target=\"${fallback_target}\"" \
			|| fatal "fallback tracepoint was not attached"
	else
		grep -F 'msg="attached BPF raw tracepoint"' "${log}" | grep -Fq "attach_target=\"${raw_target}\"" \
			|| fatal "raw tracepoint was not attached"
	fi
	awk -v warning="${warning}" '
 /level="(warn|warning)"/ && index($0,warning) {next}
 {print}
 ' "${log}" > "${HUATUO_BAMAI_TEST_TMPDIR}/hungtask-checked.log"
	assert_daemon_logs "${HUATUO_BAMAI_TEST_TMPDIR}/hungtask-checked.log"
}
integration_huatuo_bamai_start write_config_bamai_observability \
	--region integration --disable-kubelet --log-debug --bpf-dir "${ROOT_DIR}/_output/bpf"
wait_until 15 .1 hungtask_attached || fatal "production hungtask probes did not attach"
assert_huatuo_bamai_shutdown_with_signal TERM
assert_hungtask_logs auto sched_process_hang sched/sched_process_hang
cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/hungtask-production.log"

assert_hungtask_count() {
	huatuo_bamai_collect_metrics
	awk -v expected="$1" '
 $0=="# TYPE huatuo_bamai_hungtask_total counter" {typed=1}
 /^huatuo_bamai_hungtask_total[{]/ {rows++; if ($NF!=expected || $0!~/host="[^"]+"/ || $0!~/region="integration"/) bad=1}
 /^huatuo_bamai_hungtask_container_total[{]/ {bad=1}
 END {exit !typed || rows!=1 || bad}
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
assert_hungtask_event() {
	[[ -s ${HUATUO_BAMAI_TEST_TMPDIR}/events/hungtask ]] || return 1
	jq -e -s 'length==1 and (.[0] |
 .tracer_name=="hungtask" and .tracer_type=="event" and (.container_id // "")==""
 and .tracer_data.tid>0 and .tracer_data.comm=="huatuo-hung"
 and (.tracer_data.cpus_stack|type)=="string"
 and (.tracer_data.blocked_processes_stack|type)=="string")' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/events/hungtask" > /dev/null
}
for hungtask_mode in raw fallback; do
	hungtask_fixture="${HUATUO_BAMAI_TEST_TMPDIR}/hungtask-${hungtask_mode}"
	mkdir "${hungtask_fixture}"
	hungtask_flags=""
	[[ ${hungtask_mode} != fallback ]] || hungtask_flags="-DFORCE_FALLBACK"
	compile_bpf_fixture "${ROOT_DIR}/integration/testdata/test_hungtask_events.bpf.c" \
		"${hungtask_fixture}/system_hungtask.o" "${hungtask_flags}"
	rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/events/hungtask"
	integration_huatuo_bamai_start write_config_bamai_observability \
		--region integration --disable-kubelet --log-debug --bpf-dir "${hungtask_fixture}"
	wait_until 15 .1 hungtask_attached || fatal "${hungtask_mode} fixture did not attach"
	wait_until 5 .1 assert_hungtask_count 0 || fatal "initial hungtask count was not zero"
	python3 - << 'PYTHON'
import ctypes, os
lib = ctypes.CDLL(None)
if lib.prctl(15, ctypes.c_char_p(b"huatuo-hung"), 0, 0, 0) != 0:
    raise OSError("cannot set fixture comm")
for _ in range(8):
    lib.getpid()
lib.prctl(15, ctypes.c_char_p(b"python3"), 0, 0, 0)
PYTHON
	wait_until 10 .1 assert_hungtask_count 8 || fatal "hungtask events lost during backoff"
	wait_until 10 .1 assert_hungtask_event || fatal "hungtask event did not persist correctly"
	assert_huatuo_bamai_shutdown_with_signal TERM
	assert_hungtask_logs "${hungtask_mode}" sys_enter syscalls/sys_enter_getpid
	assert_hungtask_event || fatal "backoff allowed duplicate persisted events"
done
# Container discovery/attribution and actual sched_process_hang triggering are not covered.
