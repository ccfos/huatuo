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

# Verify a cgroup-limited OOM reaches BPF, event storage, and metrics.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_cgroup.sh"

readonly MEMORY_OOM_KILL_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/memory_oom_kill"
readonly MEMORY_OOM_KILL_VALID_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/memory-oom-event.json"
readonly MEMORY_OOM_KILL_LIMIT=$((8 * 1024 * 1024))
readonly MEMORY_OOM_KILL_COMM="huoom-$(head -c 8 /proc/sys/kernel/random/uuid)"
readonly MEMORY_OOM_KILL_BIN="${HUATUO_BAMAI_TEST_TMPDIR}/${MEMORY_OOM_KILL_COMM}"
readonly MEMORY_OOM_KILL_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/memory-oom-workload.log"

command -v timeout > /dev/null || skip "timeout command is not installed"
command -v jq > /dev/null || skip "jq command is not installed"
command -v findmnt > /dev/null || skip "findmnt command is not installed"
[[ -r "${ROOT_DIR}/_output/bpf/memory_oom_kill.o" ]] \
	|| fatal "memory_oom_kill BPF object not found: ${ROOT_DIR}/_output/bpf/memory_oom_kill.o"

kprobe_available oom_kill_process \
	|| skip "oom_kill_process is not available for kprobe"

memory_oom_kill_cgroup=""
trap '[[ -z "${memory_oom_kill_cgroup}" ]] || cgroup_delete "${memory_oom_kill_cgroup}"' EXIT

awk '/^MemAvailable:/ { exit ($2 < 32768) }' /proc/meminfo \
	|| skip "requires at least 32 MiB available memory"

compile_user_fixture "${ROOT_DIR}/integration/testdata/test_profiler_physical_usage.user.c" "${MEMORY_OOM_KILL_BIN}"

memory_oom_kill_cgroup=$(cgroup_create "${MEMORY_OOM_KILL_COMM}") \
	|| skip "cannot create a memory cgroup"
cgroup_configure_memory "${memory_oom_kill_cgroup}" "${MEMORY_OOM_KILL_LIMIT}" \
	|| skip "memory cgroup cannot enforce the memory limit with swap disabled"

# Enable local persistence and use real /proc data for the captured memory snapshot.
integration_huatuo_bamai_start write_memory_oom_kill_config \
	--region dev --disable-kubelet --log-debug

# The HTTP endpoint can become ready before the asynchronous BPF attachment.
wait_until 15 0.1 \
	grep -q 'attached BPF and created event pipe.*map_name="oom_perf_events"' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "memory_oom_kill BPF event pipe did not attach"

memory_oom_kill_counter() {
	huatuo_bamai_collect_metrics || return 1
	awk '/^huatuo_bamai_memory_oom_kill_host_total\{/ { print $2; found = 1 }
		END { exit !found }' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
memory_oom_kill_before=$(memory_oom_kill_counter) || fatal "memory_oom_kill baseline counter is missing"

memory_oom_kill_exit=0
cgroup_run "${memory_oom_kill_cgroup}" 10 "${MEMORY_OOM_KILL_BIN}" "$((2 * MEMORY_OOM_KILL_LIMIT))" \
	> "${MEMORY_OOM_KILL_LOG}" 2>&1 || memory_oom_kill_exit=$?
[[ ${memory_oom_kill_exit} -eq 137 ]] \
	|| fatal "expected OOM kill: exit=${memory_oom_kill_exit}: $(< "${MEMORY_OOM_KILL_LOG}")"

memory_oom_kill_event_is_valid() {
	# BPF reports initial-namespace PIDs; a unique comm correlates across PID namespaces.
	jq -s -e --arg comm "${MEMORY_OOM_KILL_COMM}" '
		first(.[] | .tracer_data as $data | select(
			.tracer_name == "memory_oom_kill"
			and .tracer_type == "event"
			and $data.victim.pid > 0
			and $data.trigger.pid == $data.victim.pid
			and $data.trigger.comm == $comm
			and $data.victim.comm == $comm
			and ($data.victim.memory_cgroup_css_addr | test("^0x[0-9a-f]*[1-9a-f][0-9a-f]*$"))
			and $data.trigger.memory_cgroup_css_addr == $data.victim.memory_cgroup_css_addr
			and $data.memory_snapshot.host_meminfo.MemTotal > 0
		))
	' "${MEMORY_OOM_KILL_EVENT}" > "${MEMORY_OOM_KILL_VALID_EVENT}" 2> /dev/null
}

wait_until 15 0.1 memory_oom_kill_event_is_valid \
	|| fatal "no valid memory_oom_kill event for allocator ${MEMORY_OOM_KILL_COMM}"
# Persistence follows the counter increment; this unregistered cgroup uses host_total.
memory_oom_kill_after=$(memory_oom_kill_counter) || fatal "memory_oom_kill final counter is missing"
awk -v before="${memory_oom_kill_before}" -v after="${memory_oom_kill_after}" 'BEGIN { exit !(after >= before + 1) }' \
	|| fatal "memory_oom_kill counter did not increase: ${memory_oom_kill_before} -> ${memory_oom_kill_after}"

huatuo_bamai_stop
assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"

log_info "memory_oom_kill passed: host_total ${memory_oom_kill_before} -> ${memory_oom_kill_after}"
jq . "${MEMORY_OOM_KILL_VALID_EVENT}"
