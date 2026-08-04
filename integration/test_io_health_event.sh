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

# Exercise the production io_health EventPipe with a disposable loop device.
# Its backing store is a sealed memfd, so a bounded loop write fails without
# formatting, mounting, or touching a real block device.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

loop_device=""
memfd_pid=""
memfd_path_file="${HUATUO_BAMAI_TEST_TMPDIR}/memfd.path"
memfd_seal_request="${HUATUO_BAMAI_TEST_TMPDIR}/memfd.seal"
memfd_sealed="${HUATUO_BAMAI_TEST_TMPDIR}/memfd.sealed"
memfd_stop="${HUATUO_BAMAI_TEST_TMPDIR}/memfd.stop"

cleanup_io_health_fixture() {
	local status=$?
	local cleanup_failed=0
	trap - EXIT
	set +e
	if [[ -n "${loop_device}" ]]; then
		if ! losetup -d "${loop_device}"; then
			log_error "failed to detach ${loop_device}"
			cleanup_failed=1
		elif losetup "${loop_device}" > /dev/null 2>&1; then
			log_error "${loop_device} remains attached after cleanup"
			cleanup_failed=1
		fi
	fi
	if [[ -n "${memfd_pid}" ]]; then
		touch "${memfd_stop}"
		if ! wait "${memfd_pid}"; then
			log_error "sealed memfd helper exited unsuccessfully"
			cleanup_failed=1
		fi
	fi
	if [[ ${cleanup_failed} -ne 0 ]]; then
		status=1
	fi
	exit "${status}"
}
trap cleanup_io_health_fixture EXIT

write_io_health_event_config() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << 'EOF'
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "netdev_rdma_link", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq", "iotracing"]

[MetricCollector.IOHealth]
    Enabled = true
EOF
}

io_health_event_observed() {
	local device=$1

	huatuo_bamai_collect_metrics || return 1
	awk -v device="${device}" '
		$1 ~ /^huatuo_bamai_io_health_block_errors_total\{/ &&
		$1 ~ ("device=\"" device "\"") &&
		$1 ~ /operation="write"/ &&
		$1 ~ /status="io_error"/ &&
		$2 + 0 > 0 {
			block_error = 1
		}
		$1 ~ /^huatuo_bamai_io_health_collection_errors_total\{/ &&
		$1 ~ ("device=\"" device "\"") &&
		$1 ~ /reason="target_unsupported"/ &&
		$2 + 0 > 0 {
			unsupported = 1
		}
		END { exit !(block_error && unsupported) }
	' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}

[[ ${EUID} -eq 0 ]] || skip "IO health event test requires root"

for command in awk blockdev dd losetup python3 timeout; do
	command -v "${command}" > /dev/null 2>&1 \
		|| skip "IO health event test requires command: ${command}"
done

[[ -r /sys/kernel/btf/vmlinux ]] \
	|| skip "IO health event test requires /sys/kernel/btf/vmlinux"
[[ -x "${HUATUO_BAMAI_BIN}" ]] \
	|| fatal "huatuo-bamai binary not found: ${HUATUO_BAMAI_BIN}"
[[ -r "${ROOT_DIR}/_output/bpf/io_health.o" ]] \
	|| fatal "io_health BPF object not found: ${ROOT_DIR}/_output/bpf/io_health.o"

tracefs=""
for candidate in /sys/kernel/tracing /sys/kernel/debug/tracing; do
	if [[ -r "${candidate}/events/block/block_rq_complete/id" ]]; then
		tracefs="${candidate}"
		break
	fi
done
[[ -n "${tracefs}" ]] \
	|| skip "IO health event test requires block_rq_complete"
if [[ -r "${tracefs}/events/block/block_rq_error/id" ]]; then
	expected_block_hook='msg="attached BPF tracepoint" attach_target="block/block_rq_error"'
else
	if ! kernel_version_le 5 15 && kernel_version_le 5 17; then
		skip "IO health block errors do not support the Linux 5.16-5.17 ABI"
	fi
	expected_block_hook='msg="attached BPF raw tracepoint" attach_target="block_rq_complete"'
fi

cat > "${HUATUO_BAMAI_TEST_TMPDIR}/sealed_memfd.py" << 'PY'
import fcntl
import os
from pathlib import Path
import sys
import time

path_file, seal_request, sealed_file, stop_file = map(Path, sys.argv[1:])
fd = os.memfd_create("huatuo-io-health", os.MFD_ALLOW_SEALING)
os.ftruncate(fd, 8 * 1024 * 1024)
path_file.write_text(f"/proc/{os.getpid()}/fd/{fd}\n", encoding="ascii")

while not seal_request.exists():
    if stop_file.exists():
        sys.exit(0)
    time.sleep(0.05)

fcntl.fcntl(fd, fcntl.F_ADD_SEALS, fcntl.F_SEAL_WRITE)
sealed_file.touch()
while not stop_file.exists():
    time.sleep(0.05)
PY

python3 "${HUATUO_BAMAI_TEST_TMPDIR}/sealed_memfd.py" \
	"${memfd_path_file}" \
	"${memfd_seal_request}" \
	"${memfd_sealed}" \
	"${memfd_stop}" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/sealed_memfd.log" 2>&1 &
memfd_pid=$!
wait_until 10 1 test -s "${memfd_path_file}" \
	|| fatal "sealed memfd helper did not publish its backing path"
memfd_path=$(< "${memfd_path_file}")

loop_device=$(losetup --find --show "${memfd_path}") \
	|| skip "IO health event test has no available loop device"
loop_name=$(basename "${loop_device}")
[[ -b "${loop_device}" ]] \
	|| fatal "losetup did not create a block device: ${loop_device}"
[[ "$(blockdev --getsize64 "${loop_device}")" -eq $((8 * 1024 * 1024)) ]] \
	|| fatal "unexpected loop capacity for ${loop_device}"

integration_huatuo_bamai_start write_io_health_event_config \
	"--region" "dev" \
	"--disable-storage" \
	"--disable-kubelet" \
	"--log-debug"

wait_until 10 1 grep -qF \
	"${expected_block_hook}" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "io_health did not attach ${expected_block_hook}"

touch "${memfd_seal_request}"
wait_until 10 1 test -e "${memfd_sealed}" \
	|| fatal "memfd helper could not seal the loop backing store"
set +e
timeout 10 dd \
	if=/dev/zero \
	of="${loop_device}" \
	bs=4096 \
	count=1 \
	oflag=direct \
	status=none
write_status=$?
set -e
if [[ ${write_status} -eq 0 ]]; then
	fatal "sealed loop backing store did not reject the test write"
fi
[[ ${write_status} -ne 124 ]] \
	|| fatal "loop fault injection did not finish within 10 seconds"

wait_until 15 1 io_health_event_observed "${loop_name}" \
	|| fatal "io_health did not publish the loop block error"

log_info "io_health observed a real block error for ${loop_name}"
