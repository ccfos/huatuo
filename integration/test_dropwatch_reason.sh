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

# Verify that dropwatch resolves software drop reasons from kernel BTF. The
# assertion is mandatory only when bpftool can parse the required enum.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly KERNEL_BTF="/sys/kernel/btf/vmlinux"
readonly TARGET_IP="127.0.0.99"
readonly TARGET_PORT=9998

DROPWATCH_PID=""
TRAFFIC_PID=""

cleanup() {
	if [[ -n "${TRAFFIC_PID}" ]]; then
		kill "${TRAFFIC_PID}" 2> /dev/null || true
		wait "${TRAFFIC_PID}" 2> /dev/null || true
	fi
	[[ -n "${DROPWATCH_PID}" ]] && stop_by_pid "${DROPWATCH_PID}" 2 || true
}
trap cleanup EXIT

command -v jq > /dev/null 2>&1 || skip "jq command is not installed"
command -v bpftool > /dev/null 2>&1 || skip "bpftool command is not installed"
[[ -r "${KERNEL_BTF}" ]] || skip "kernel BTF is not readable: ${KERNEL_BTF}"

btf_dump="${HUATUO_BAMAI_TEST_TMPDIR}/vmlinux.btf"
bpftool btf dump file "${KERNEL_BTF}" format raw \
	> "${btf_dump}" 2> "${HUATUO_BAMAI_TEST_TMPDIR}/bpftool.err" \
	|| skip "bpftool cannot parse kernel BTF"
grep -Eq "ENUM(64)? 'skb_drop_reason'" "${btf_dump}" \
	|| skip "kernel BTF does not expose skb_drop_reason"

bpf_tool_setup dropwatch net_dropwatch
"${TOOL_BIN}" \
	--bpf-path "${TOOL_BPF}" \
	--filter "udp and port ${TARGET_PORT}" \
	--duration 3 \
	--output json \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
DROPWATCH_PID=$!

(
	while kill -0 "${DROPWATCH_PID}" 2> /dev/null; do
		printf x > "/dev/udp/${TARGET_IP}/${TARGET_PORT}" 2> /dev/null || true
		sleep 0.05
	done
) &
TRAFFIC_PID=$!

if ! wait "${DROPWATCH_PID}"; then
	DROPWATCH_PID=""
	fatal "dropwatch failed while tracing software drops"
fi
DROPWATCH_PID=""
if ! wait "${TRAFFIC_PID}"; then
	TRAFFIC_PID=""
	fatal "drop-reason traffic generator failed"
fi
TRAFFIC_PID=""

assert_log_has_no_failure "${TOOL_ERR}" "dropwatch"
assert_kernel_observation_timestamps "${TOOL_OUT}"
jq -e -s 'any(.[]; .drop_reason | startswith("SKB_DROP_REASON_"))' "${TOOL_OUT}" > /dev/null \
	|| fatal "dropwatch did not resolve a symbolic SKB_DROP_REASON_ value"
log_info "dropwatch resolved a symbolic software drop reason"
