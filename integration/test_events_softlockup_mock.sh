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
	skip "softlockup fixture requires Linux 4.18 or newer"
fi
case $(uname -m) in
x86_64 | aarch64) ;;
*) skip "softlockup fixture requires x86_64 or aarch64" ;;
esac
command -v insmod > /dev/null || fatal "insmod is required"
command -v rmmod > /dev/null || fatal "rmmod is required"
command -v jq > /dev/null || fatal "jq is required"
[[ -r "${ROOT_DIR}/_output/bpf/system_softlockup.o" ]] \
	|| fatal "system_softlockup.o is missing; run make build"

readonly softlockup_event_file="${HUATUO_BAMAI_TEST_TMPDIR}/events/softlockup"
readonly softlockup_valid_event="${HUATUO_BAMAI_TEST_TMPDIR}/softlockup-mock-event.json"
readonly softlockup_comm="softlockup-mock"
readonly softlockup_trigger="${HUATUO_BAMAI_TEST_TMPDIR}/${softlockup_comm}"

kernel_build_dir=${KERNEL_BUILD_DIR:-/lib/modules/$(uname -r)/build}
[[ -d "${kernel_build_dir}" ]] \
	|| fatal "kernel build directory is missing: ${kernel_build_dir}"

fixture_dir=${HUATUO_BAMAI_TEST_TMPDIR}/softlockup_mock_module
mkdir -p "${fixture_dir}"
cp "${ROOT_DIR}/integration/testdata/softlockup_mock/Makefile" \
	"${ROOT_DIR}/integration/testdata/softlockup_mock/huatuo_softlockup_mock.c" \
	"${ROOT_DIR}/integration/testdata/softlockup_mock/softlockup_mock.h" \
	"${fixture_dir}/"

make -C "${fixture_dir}" KERNEL_BUILD_DIR="${kernel_build_dir}" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/softlockup-mock-build.log" 2>&1 \
	|| fatal "softlockup fixture module build failed; see softlockup-mock-build.log"

compile_user_fixture "${ROOT_DIR}/integration/testdata/softlockup_mock/trigger.c" "${softlockup_trigger}"

cleanup() {
	huatuo_bamai_stop || true
	rmmod huatuo_softlockup_mock || fatal "failed to unload huatuo_softlockup_mock; remove it with rmmod before retrying"
}

insmod "${fixture_dir}/huatuo_softlockup_mock.ko"
trap cleanup EXIT

# Loading an external module can set module taint bits; check only softlockup.
readonly softlockup_taint_before=$(($(< /proc/sys/kernel/tainted) & (1 << 14)))

wait_until 5 0.1 test -e /dev/huatuo_softlockup_mock \
	|| fatal "softlockup fixture device /dev/huatuo_softlockup_mock did not appear"

integration_huatuo_bamai_start write_softlockup_config \
	--region dev --disable-kubelet --log-debug --bpf-mock

wait_until 15 0.1 \
	grep -q 'attached BPF program.*symbol="add_taint_mock"' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "softlockup BPF program did not attach to mock symbol"
wait_until 15 0.1 \
	grep -q 'attached BPF and created event pipe.*map_name="softlockup_perf_events"' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "softlockup perf event pipe did not start"

softlockup_counter_is() {
	local expected=$1
	huatuo_bamai_collect_metrics || return 1
	awk -v expected="${expected}" '
		/^huatuo_bamai_softlockup_total\{/ { value = $2; found = 1 }
		END { exit !(found && value == expected) }
	' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
wait_until 15 0.1 softlockup_counter_is 0 \
	|| fatal "softlockup counter baseline is not zero"

"${softlockup_trigger}" other
sleep 1
[[ ! -s "${softlockup_event_file}" ]] \
	|| fatal "a non-softlockup taint flag produced an event"
softlockup_counter_is 0 || fatal "a non-softlockup taint flag incremented the counter"

"${softlockup_trigger}" softlockup &
softlockup_pid=$!
wait "${softlockup_pid}"

softlockup_event_is_valid() {
	jq -s -e --arg comm "${softlockup_comm}" --argjson pid "${softlockup_pid}" '
		first(.[] | .tracer_data as $data | select(
			.tracer_name == "softlockup"
			and .tracer_type == "event"
			and $data.pid == $pid
			and $data.comm == $comm
			and $data.cpu >= 0
			and ($data.cpu | type) == "number"
			and ($data.cpus_stack | type) == "string"
		))
	' "${softlockup_event_file}" > "${softlockup_valid_event}" 2> /dev/null
}
wait_until 20 0.1 softlockup_event_is_valid \
	|| fatal "no valid softlockup event from pid=${softlockup_pid} comm=${softlockup_comm}"
wait_until 20 0.2 softlockup_counter_is 1 \
	|| fatal "softlockup counter did not increase to one"

"${softlockup_trigger}" other
# The Go collector suppresses repeated saved events, but counts every BPF event.
# Observe longer than its 10-second metric interval to verify BPF filtering.
for ((attempt = 0; attempt < 12; attempt++)); do
	sleep 1
	softlockup_counter_is 1 || fatal "a non-softlockup taint flag incremented the counter"
done
jq -s -e 'length == 1' "${softlockup_event_file}" > /dev/null \
	|| fatal "expected exactly one saved softlockup event"
[[ $(($(< /proc/sys/kernel/tainted) & (1 << 14))) -eq ${softlockup_taint_before} ]] \
	|| fatal "mock trigger changed the kernel softlockup taint bit"

huatuo_bamai_stop
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
log_info "mock softlockup passed: total 0 -> 1; other taint flags ignored"
jq '.tracer_data | {cpu, pid, comm, cpus_stack_bytes: (.cpus_stack | length)}' "${softlockup_valid_event}"
