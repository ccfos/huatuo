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

require_bpf_mock_platform
require_commands make gcc insmod rmmod jq
[[ -r "${ROOT_DIR}/_output/bpf/memory_oom_kill.o" ]] \
	|| fatal "memory_oom_kill.o is missing; run make build"

readonly oom_event_file="${HUATUO_BAMAI_TEST_TMPDIR}/es-oom-events.json"
readonly -a oom_scenarios=(same-task other-task memcg other-task-memcg null-control null-victim)
readonly oom_comm="oom-mock-$(head -c 6 /proc/sys/kernel/random/uuid)"
readonly oom_trigger="${HUATUO_BAMAI_TEST_TMPDIR}/${oom_comm}"

fixture_dir=${HUATUO_BAMAI_TEST_TMPDIR}/oom_mock_module
build_bpf_mock_module memory_oom_kill "${fixture_dir}"

compile_user_fixture "${ROOT_DIR}/integration/bpf_mock/memory_oom_kill/memory_oom_kill_trigger.c" "${oom_trigger}"

loaded_module=""
trap 'cleanup_bpf_mock_es "$?" "${loaded_module}" || exit $?' EXIT
start_bpf_mock_es
load_bpf_mock_module memory_oom_kill "${fixture_dir}"

write_oom_mock_config() {
	write_bpf_mock_es_config memory_oom_kill
}

integration_huatuo_bamai_start write_oom_mock_config \
	--region dev --disable-kubelet --log-debug \
	--bpf-mock

await_bpf_mock_attached oom_perf_events kprobe:oom_kill_process_mock

oom_counter() {
	huatuo_bamai_collect_metrics || return 1
	awk '/^huatuo_bamai_memory_oom_kill_host_total\{/ { print $2; found = 1 }
		END { exit !found }' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
oom_before=$(oom_counter) || fatal "OOM host counter baseline is missing"

for scenario in "${oom_scenarios[@]}"; do
	"${oom_trigger}" "$scenario" 0x1ace 321 \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/${scenario}.json"
done
# Structural coverage uses a fabricated victim, independently of real-task cases.
"${oom_trigger}" pointer-chain 0x1ace 321 \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/pointer-chain.json"

oom_counter_is_expected() {
	local after
	after=$(oom_counter) || return 1
	awk -v before="${oom_before}" -v after="${after}" \
		'BEGIN { exit !(after == before + 6) }'
}
wait_until 20 0.2 oom_counter_is_expected \
	|| fatal "OOM host counter did not increase by exactly six from ${oom_before}"
oom_after=$(oom_counter)

stop_bpf_mock_daemon
verify_bpf_mock_es_events memory_oom_kill "${oom_event_file}" "${HUATUO_BAMAI_TEST_TMPDIR}" "${oom_scenarios[@]}" pointer-chain
log_info "mock OOM passed: ES documents verified, host_total ${oom_before} -> ${oom_after}"
