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

# Exercise signal exit status through the real command, including startup
# rollback and an output failure during shutdown.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

require_commands python3 mkfifo readlink timeout
require_readable /sys/kernel/btf/vmlinux
CANCEL_PORT=$(allocate_available_port udp) || fatal "cannot allocate dropwatch UDP target port"
readonly CANCEL_PORT

cancel_pid="" cancel_peer_pid="" cancel_fifo_fd=""
cleanup() {
	[[ -z ${cancel_fifo_fd} ]] || exec {cancel_fifo_fd}>&-
	[[ -z ${cancel_pid} ]] || stop_by_pid "${cancel_pid}" 2 || true
	[[ -z ${cancel_peer_pid} ]] || stop_by_pid "${cancel_peer_pid}" 2 || true
}
trap cleanup EXIT

cancel_reading_fifo() {
	local fd
	for fd in /proc/"${cancel_pid}"/fd/*; do
		[[ $(readlink "${fd}") != "${TOOL_WORK_DIR}/object" ]] || return 0
	done
	return 1
}

cancel_has_event() {
	printf x > "/dev/udp/127.0.0.99/${CANCEL_PORT}" 2> /dev/null || true
	[[ -s ${TOOL_OUT} ]]
}

assert_cancel_exit() {
	local expected=$1 status=0
	wait_until 15 0.1 process_exited "${cancel_pid}" \
		|| fatal "dropwatch did not exit after cancellation"
	wait "${cancel_pid}" || status=$?
	cancel_pid=""
	assert_eq "${status}" "${expected}" "dropwatch cancellation exit status"
}

assert_cancel_output_failure() {
	grep -q 'close event sink' "${TOOL_ERR}" \
		|| fatal "expected close event sink failure; see ${TOOL_ERR}"
}

for cancel_signal in TERM INT; do
	bpf_tool_setup dropwatch net_dropwatch "cancel-start-${cancel_signal}"
	mkfifo "${TOOL_WORK_DIR}/object"
	exec {cancel_fifo_fd}<> "${TOOL_WORK_DIR}/object"
	"${TOOL_BIN}" --bpf-path "${TOOL_WORK_DIR}/object" \
		> "${TOOL_OUT}" 2> "${TOOL_ERR}" {cancel_fifo_fd}>&- &
	cancel_pid=$!
	# Holding the object read guarantees NotifyContext is installed and Open
	# is in progress before the signal, without depending on startup timing.
	wait_until 15 0.1 cancel_reading_fifo || fatal "dropwatch did not open the BPF FIFO"
	kill -"${cancel_signal}" "${cancel_pid}"
	timeout 10 cat "${TOOL_BPF}" >&"${cancel_fifo_fd}"
	exec {cancel_fifo_fd}>&-
	cancel_fifo_fd=""
	assert_cancel_exit 0
	assert_log_has_no_failure "${TOOL_ERR}" "dropwatch startup cancellation"

	bpf_tool_setup dropwatch net_dropwatch "cancel-running-${cancel_signal}"
	"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
		--filter "udp and dst host 127.0.0.99 and dst port ${CANCEL_PORT}" \
		> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
	cancel_pid=$!
	wait_until 15 0.1 cancel_has_event || fatal "dropwatch did not emit a software drop"
	assert_process_shutdown_by_pid "${cancel_pid}" 15 "${cancel_signal}"
	cancel_pid=""
	assert_log_has_no_failure "${TOOL_ERR}" "dropwatch running cancellation"
done

bpf_tool_setup dropwatch net_dropwatch cancel-output
python3 "${ROOT_DIR}/integration/testdata/test_dropwatch_closed_sink.py" \
	"${TOOL_WORK_DIR}/sink" "${TOOL_WORK_DIR}/closed" "${TOOL_WORK_DIR}/ready" \
	> "${TOOL_WORK_DIR}/peer.log" 2>&1 &
cancel_peer_pid=$!
wait_until 5 0.1 test -e "${TOOL_WORK_DIR}/ready" || fatal "output peer did not listen"
"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" --filter 'udp and dst host 192.0.2.254' \
	--output-storage "${TOOL_WORK_DIR}/sink" > "${TOOL_OUT}" 2> "${TOOL_ERR}" &
cancel_pid=$!
wait_until 15 0.1 test -e "${TOOL_WORK_DIR}/closed" || fatal "output peer did not close"
wait "${cancel_peer_pid}"
cancel_peer_pid=""
kill -TERM "${cancel_pid}"
assert_cancel_exit 1
assert_cancel_output_failure

log_info "dropwatch cancellation preserves successful exits and real output failures"
