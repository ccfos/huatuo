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

cleanup() {
	[[ -n "${DROPWATCH_PID}" ]] && stop_by_pid "${DROPWATCH_PID}" 2 || true
}
trap cleanup EXIT

command -v bpftool > /dev/null 2>&1 || skip "bpftool command is not installed"
[[ -r "${KERNEL_BTF}" ]] || skip "kernel BTF is not readable: ${KERNEL_BTF}"

btf_dump="${HUATUO_BAMAI_TEST_TMPDIR}/vmlinux.btf"
bpftool btf dump file "${KERNEL_BTF}" format raw \
	> "${btf_dump}" 2> "${HUATUO_BAMAI_TEST_TMPDIR}/bpftool.err" \
	|| skip "bpftool cannot parse kernel BTF"
grep -Eq "ENUM(64)? 'skb_drop_reason'" "${btf_dump}" \
	|| skip "kernel BTF does not expose skb_drop_reason"

bpf_tool_setup dropwatch
"${TOOL_BIN}" \
	--bpf-path "${TOOL_BPF}" \
	--filter "udp and port ${TARGET_PORT}" \
	--duration 3 \
	--output text \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
DROPWATCH_PID=$!
sleep 0.5

for ((packet = 0; packet < 100; packet++)); do
	printf x > "/dev/udp/${TARGET_IP}/${TARGET_PORT}" 2> /dev/null || true
done

if ! wait "${DROPWATCH_PID}"; then
	DROPWATCH_PID=""
	fatal "dropwatch failed while tracing software drops"
fi
DROPWATCH_PID=""

assert_log_has_no_failure "${TOOL_ERR}" "dropwatch"
grep -q "IPv4/UDP.* reason=SKB_DROP_REASON_" "${TOOL_OUT}" \
	|| fatal "dropwatch did not resolve a symbolic SKB_DROP_REASON_ value"
log_info "dropwatch resolved a symbolic software drop reason"
