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

skip_known_mock_kprobe_kernel

if kernel_version_le 4 17; then
	skip "softlockup fixture requires Linux 4.18 or newer"
fi
case $(uname -m) in
x86_64 | aarch64) ;;
*) skip "softlockup fixture requires x86_64 or aarch64" ;;
esac
require_commands make gcc insmod rmmod jq
[[ -r "${ROOT_DIR}/_output/bpf/system_softlockup.o" ]] \
	|| fatal "system_softlockup.o is missing; run make build"

readonly softlockup_event_file="${HUATUO_BAMAI_TEST_TMPDIR}/es-softlockup-events.json"
readonly softlockup_comm="softlockup-mock"
readonly softlockup_trigger="${HUATUO_BAMAI_TEST_TMPDIR}/${softlockup_comm}"

kernel_build_dir=${KERNEL_BUILD_DIR:-/lib/modules/$(uname -r)/build}
[[ -d "${kernel_build_dir}" ]] \
	|| skip "kernel build directory is missing: ${kernel_build_dir}"

fixture_dir=${HUATUO_BAMAI_TEST_TMPDIR}/softlockup_mock_module
mkdir -p "${fixture_dir}"
cp "${ROOT_DIR}/integration/bpf_mock/softlockup/Makefile" \
	"${ROOT_DIR}/integration/bpf_mock/softlockup/softlockup_mock_kmod.c" \
	"${ROOT_DIR}/integration/bpf_mock/softlockup/softlockup_mock.h" \
	"${fixture_dir}/"

make -C "${fixture_dir}" KERNEL_BUILD_DIR="${kernel_build_dir}" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/softlockup-mock-build.log" 2>&1 \
	|| fatal "softlockup fixture module build failed; see softlockup-mock-build.log"

compile_user_fixture "${ROOT_DIR}/integration/bpf_mock/softlockup/trigger.c" "${softlockup_trigger}" -pthread

module_loaded=0
cleanup() {
	local status=$?
	huatuo_bamai_stop || status=1
	if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
		storage_dump_logs || true
	fi
	stop_bpf_mock_es || status=1
	if [[ ${module_loaded} == 1 ]]; then
		rmmod huatuo_softlockup_mock || status=1
	fi
	return "${status}"
}
trap 'cleanup || exit $?' EXIT
start_bpf_mock_es
insmod "${fixture_dir}/huatuo_softlockup_mock.ko"
module_loaded=1

write_softlockup_mock_config() {
	write_bpf_mock_es_config softlockup
}

# Loading an external module can set module taint bits; check only softlockup.
readonly softlockup_taint_before=$(($(< /proc/sys/kernel/tainted) & (1 << 14)))

wait_until 5 0.1 test -e /dev/huatuo_softlockup_mock \
	|| fatal "softlockup fixture device /dev/huatuo_softlockup_mock did not appear"

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

	wait_until 15 0.1 \
		grep -q 'attached BPF program.*symbol="add_taint_mock"' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "softlockup BPF program did not attach to mock symbol"
	wait_until 15 0.1 \
		grep -q 'attached BPF and created event pipe.*map_name="softlockup_perf_events"' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "softlockup perf event pipe did not start"
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

es_events_are_complete() {
	read_bpf_mock_es_events "${softlockup_event_file}" || return 1
	jq -se 'length == 2' "${softlockup_event_file}" > /dev/null
}
wait_until 30 0.5 es_events_are_complete || fatal "expected exactly two ES softlockup documents"
run_bpf_mock_checker softlockup "${softlockup_event_file}" "${HUATUO_BAMAI_TEST_TMPDIR}"
[[ $(($(< /proc/sys/kernel/tainted) & (1 << 14))) -eq ${softlockup_taint_before} ]] \
	|| fatal "mock trigger changed the kernel softlockup taint bit"
log_info "mock softlockup passed: main and worker ES documents verified"
