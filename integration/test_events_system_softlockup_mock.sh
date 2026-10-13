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
[[ -r "${ROOT_DIR}/_output/bpf/system_softlockup.o" ]] \
	|| fatal "system_softlockup.o is missing; run make build"

readonly softlockup_event_file="${HUATUO_BAMAI_TEST_TMPDIR}/es-softlockup-events.json"
readonly softlockup_comm="softlockup-mock"
readonly softlockup_trigger="${HUATUO_BAMAI_TEST_TMPDIR}/${softlockup_comm}"

fixture_dir=${HUATUO_BAMAI_TEST_TMPDIR}/softlockup_mock_module
build_bpf_mock_module system_softlockup "${fixture_dir}"

compile_user_fixture "${ROOT_DIR}/integration/bpf_mock/system_softlockup/system_softlockup_trigger.c" "${softlockup_trigger}" -pthread

loaded_module=""
trap 'cleanup_bpf_mock_es "$?" "${loaded_module}" || exit $?' EXIT
start_bpf_mock_es
load_bpf_mock_module system_softlockup "${fixture_dir}"

write_softlockup_mock_config() {
	write_bpf_mock_es_config softlockup
}

# Loading an external module can set module taint bits; check only softlockup.
readonly softlockup_taint_before=$(($(< /proc/sys/kernel/tainted) & (1 << 14)))

softlockup_counter_is() {
	local expected=$1
	huatuo_bamai_collect_metrics || return 1
	awk -v expected="${expected}" '
		/^huatuo_bamai_softlockup_total\{/ { value = $2; found = 1 }
		END { exit !(found && value == expected) }
	' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
# Each task gets the first report from a fresh collector, respecting its backoff.
for scenario in main thread; do
	integration_huatuo_bamai_start write_softlockup_mock_config \
		--region dev --disable-kubelet --log-debug --bpf-mock

	await_bpf_mock_attached softlockup_perf_events kprobe:add_taint_mock
	wait_until 15 0.1 softlockup_counter_is 0 \
		|| fatal "softlockup counter baseline is not zero"
	if [[ ${scenario} == main ]]; then
		"${softlockup_trigger}" other > "${HUATUO_BAMAI_TEST_TMPDIR}/other-main.json"
		"${softlockup_trigger}" other thread > "${HUATUO_BAMAI_TEST_TMPDIR}/other-thread.json"
	fi
	args=(softlockup)
	[[ ${scenario} == thread ]] && args+=(thread)
	"${softlockup_trigger}" "${args[@]}" > "${HUATUO_BAMAI_TEST_TMPDIR}/${scenario}.json"
	wait_until 20 0.2 softlockup_counter_is 1 \
		|| fatal "${scenario} softlockup did not increment the counter to one"
	stop_bpf_mock_daemon
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/${scenario}-huatuo.log"
done

verify_bpf_mock_es_events system_softlockup "${softlockup_event_file}" "${HUATUO_BAMAI_TEST_TMPDIR}"
[[ $(($(< /proc/sys/kernel/tainted) & (1 << 14))) -eq ${softlockup_taint_before} ]] \
	|| fatal "mock trigger changed the kernel softlockup taint bit"
log_info "mock softlockup passed: main and worker ES documents verified"
