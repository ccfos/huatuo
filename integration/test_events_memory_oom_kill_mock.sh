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
	skip "OOM fixture requires Linux 4.18 or newer"
fi
case $(uname -m) in
x86_64 | aarch64) ;;
*) skip "OOM fixture requires x86_64 or aarch64" ;;
esac
command -v insmod > /dev/null || fatal "insmod is required"
command -v rmmod > /dev/null || fatal "rmmod is required"
command -v jq > /dev/null || fatal "jq is required"
[[ -r "${ROOT_DIR}/_output/bpf/memory_oom_kill.o" ]] \
	|| fatal "memory_oom_kill.o is missing; run make build"

readonly oom_event_file="${HUATUO_BAMAI_TEST_TMPDIR}/events/memory_oom_kill"
readonly oom_valid_event="${HUATUO_BAMAI_TEST_TMPDIR}/oom-mock-event.json"
readonly oom_comm="oom-mock-$(head -c 6 /proc/sys/kernel/random/uuid)"
readonly oom_trigger="${HUATUO_BAMAI_TEST_TMPDIR}/${oom_comm}"

kernel_build_dir=${KERNEL_BUILD_DIR:-/lib/modules/$(uname -r)/build}
[[ -d "${kernel_build_dir}" ]] \
	|| fatal "kernel build directory is missing: ${kernel_build_dir}"

fixture_dir=${HUATUO_BAMAI_TEST_TMPDIR}/oom_mock_module
mkdir -p "${fixture_dir}"
cp "${ROOT_DIR}/integration/testdata/oom_mock/Makefile" \
	"${ROOT_DIR}/integration/testdata/oom_mock/huatuo_oom_mock.c" \
	"${fixture_dir}/"

make -C "${fixture_dir}" KERNEL_BUILD_DIR="${kernel_build_dir}" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/oom-mock-build.log" 2>&1 \
	|| fatal "OOM fixture module build failed; see oom-mock-build.log"

compile_user_fixture "${ROOT_DIR}/integration/testdata/oom_mock/trigger.c" "${oom_trigger}"

module_loaded=0
cleanup() {
	huatuo_bamai_stop || true
	if [[ ${module_loaded} -eq 1 ]]; then
		rmmod huatuo_oom_mock || log_warn "failed to unload huatuo_oom_mock"
	fi
}
trap cleanup EXIT

insmod "${fixture_dir}/huatuo_oom_mock.ko"
module_loaded=1

wait_until 5 0.1 test -e /dev/huatuo_oom_mock \
	|| fatal "OOM fixture device /dev/huatuo_oom_mock did not appear"

integration_huatuo_bamai_start write_memory_oom_kill_config \
	--region dev --disable-kubelet --log-debug \
	--bpf-attach-override memory_oom_kill.o:oom_kill_process=oom_kill_process_mock

wait_until 15 0.1 \
	grep -q 'attached BPF program.*symbol="oom_kill_process_mock"' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "OOM BPF program did not attach to mock symbol"
wait_until 15 0.1 \
	grep -q 'attached BPF and created event pipe.*map_name="oom_perf_events"' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "OOM perf event pipe did not start"

oom_counter() {
	huatuo_bamai_collect_metrics || return 1
	awk '/^huatuo_bamai_memory_oom_kill_host_total\{/ { print $2; found = 1 }
		END { exit !found }' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
oom_before=$(oom_counter) || fatal "OOM host counter baseline is missing"

"${oom_trigger}" 0x1ace

oom_event_is_valid() {
	jq -s -e --arg comm "${oom_comm}" '
		first(.[] | .tracer_data as $data | select(
			.tracer_name == "memory_oom_kill"
			and .tracer_type == "event"
			and $data.trigger.pid > 0
			and $data.trigger.pid == $data.victim.pid
			and $data.trigger.comm == $comm
			and $data.victim.comm == $comm
			and ($data.victim.memory_cgroup_css_addr | test("^0x[0-9a-f]*[1-9a-f][0-9a-f]*$"))
			and $data.trigger.memory_cgroup_css_addr == $data.victim.memory_cgroup_css_addr
			and $data.memory_snapshot.host_meminfo.MemTotal > 0
		))
	' "${oom_event_file}" > "${oom_valid_event}" 2> /dev/null
}
wait_until 15 0.1 oom_event_is_valid \
	|| fatal "no valid OOM event from ${oom_comm}"

oom_counter_increased() {
	local after
	after=$(oom_counter) || return 1
	awk -v before="${oom_before}" -v after="${after}" \
		'BEGIN { exit !(after >= before + 1) }'
}
wait_until 20 0.2 oom_counter_increased \
	|| fatal "OOM host counter did not increase from ${oom_before}"
oom_after=$(oom_counter)

huatuo_bamai_stop
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
log_info "mock OOM passed: host_total ${oom_before} -> ${oom_after}"
jq . "${oom_valid_event}"
