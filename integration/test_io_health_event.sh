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

# Exercise the production io_health EventPipe, counters, and local event store
# with a disposable loop device.
# Its backing store is a sealed memfd, so a bounded loop write fails without
# formatting, mounting, or touching a real block device.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
# Exported arrays do not reach this subprocess; restore the shared curl bounds.
CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

loop_device=""
memfd_pid=""
memfd_path_file="${HUATUO_BAMAI_TEST_TMPDIR}/memfd.path"
memfd_seal_request="${HUATUO_BAMAI_TEST_TMPDIR}/memfd.seal"
memfd_sealed="${HUATUO_BAMAI_TEST_TMPDIR}/memfd.sealed"
memfd_stop="${HUATUO_BAMAI_TEST_TMPDIR}/memfd.stop"

io_health_loop_detached() {
	! losetup "$1" > /dev/null 2>&1
}

cleanup_io_health_fixture() {
	local status=$?
	local cleanup_failed=0
	trap - EXIT
	set +e
	if [[ -n "${loop_device}" ]]; then
		if ! losetup -d "${loop_device}"; then
			log_error "failed to detach ${loop_device}"
			cleanup_failed=1
		elif ! wait_until 10 1 \
			io_health_loop_detached "${loop_device}"; then
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

inject_io_health_error() {
	set +e
	timeout 10 dd \
		if=/dev/zero \
		of="${loop_device}" \
		bs=4096 \
		count=1 \
		oflag=direct \
		status=none
	local write_status=$?
	set -e

	[[ ${write_status} -ne 0 ]] \
		|| fatal "sealed loop backing store did not reject the test write"
	[[ ${write_status} -ne 124 ]] \
		|| fatal "loop fault injection did not finish within 10 seconds"
}

io_health_event_observed() {
	local device=$1

	inject_io_health_error
	huatuo_bamai_metrics > "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" \
		|| return 1
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

io_health_event_saved() {
	local record_file="${HUATUO_BAMAI_TEST_TMPDIR}/records/io_health"
	[[ -s "${record_file}" ]] || return 1
	jq -s -e --arg device "$1" '
		any(.[];
			.tracer_name == "io_health" and
			.tracer_data.type == "block_error" and
			.tracer_data.device == $device and
			.tracer_data.operation == "write" and
			.tracer_data.io_error_status == "io_error" and
			.tracer_data.collection_status == "unsupported" and
			(.tracer_data.sector | type == "number"))
	' "${record_file}" > /dev/null 2>&1
}

# Allow request checks to be tested without creating a block device.
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
	return 0
fi
trap cleanup_io_health_fixture EXIT

[[ ${EUID} -eq 0 ]] || skip "IO health event test requires root"

for command in awk blockdev dd jq losetup python3 timeout; do
	command -v "${command}" > /dev/null 2>&1 \
		|| skip "IO health event test requires command: ${command}"
done
if ! python3 - << 'PY' > /dev/null 2>&1; then
import ctypes
import os

if not hasattr(os, "memfd_create"):
    getattr(ctypes.CDLL(None), "memfd_create")
PY
	skip "IO health event test requires memfd_create"
fi

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
if [[ ! -r "${tracefs}/events/block/block_rq_error/id" ]]; then
	kernel_release=$(uname -r)
	kernel_major=${kernel_release%%.*}
	kernel_minor=${kernel_release#*.}
	kernel_minor=${kernel_minor%%.*}
	if ((kernel_major > 5 || (kernel_major == 5 && kernel_minor > 17))); then
		skip "IO health block errors require block_rq_error after Linux 5.17"
	fi
fi

write_io_health_config() {
	write_default_config
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF

[Storage.LocalFile]
    Path = "${HUATUO_BAMAI_TEST_TMPDIR}/records"
EOF
}

integration_huatuo_bamai_start write_io_health_config \
	--region dev --disable-kubelet --log-debug

cat > "${HUATUO_BAMAI_TEST_TMPDIR}/sealed_memfd.py" << 'PY'
import fcntl
import ctypes
import os
from pathlib import Path
import sys
import time

path_file, seal_request, sealed_file, stop_file = map(Path, sys.argv[1:])
if hasattr(os, "memfd_create"):
    fd = os.memfd_create(
        "huatuo-io-health",
        getattr(os, "MFD_ALLOW_SEALING", 2),
    )
else:
    libc = ctypes.CDLL(None, use_errno=True)
    libc.memfd_create.argtypes = [ctypes.c_char_p, ctypes.c_uint]
    libc.memfd_create.restype = ctypes.c_int
    fd = libc.memfd_create(b"huatuo-io-health", 2)
    if fd < 0:
        errno = ctypes.get_errno()
        raise OSError(errno, os.strerror(errno))
os.ftruncate(fd, 8 * 1024 * 1024)
path_file.write_text(f"/proc/{os.getpid()}/fd/{fd}\n", encoding="ascii")

while not seal_request.exists():
    if stop_file.exists():
        sys.exit(0)
    time.sleep(0.05)

fcntl.fcntl(
    fd,
    getattr(fcntl, "F_ADD_SEALS", 1033),
    getattr(fcntl, "F_SEAL_WRITE", 0x0008),
)
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
wait_until 10 1 \
	test -s "${memfd_path_file}" \
	|| fatal "sealed memfd helper did not publish its backing path"
memfd_path=$(< "${memfd_path_file}")

loop_device=$(losetup --find --show "${memfd_path}") \
	|| skip "IO health event test has no available loop device"
loop_name=$(basename "${loop_device}")
[[ -b "${loop_device}" ]] \
	|| fatal "losetup did not create a block device: ${loop_device}"
[[ "$(blockdev --getsize64 "${loop_device}")" -eq $((8 * 1024 * 1024)) ]] \
	|| fatal "unexpected loop capacity for ${loop_device}"

touch "${memfd_seal_request}"
wait_until 10 1 \
	test -e "${memfd_sealed}" \
	|| fatal "memfd helper could not seal the loop backing store"

wait_until 15 1 \
	io_health_event_observed "${loop_name}" \
	|| fatal "io_health did not publish the loop block error"

wait_until 10 1 \
	io_health_event_saved "${loop_name}" \
	|| fatal "io_health counter advanced without a saved block error"

log_info "io_health observed and saved a real block error for ${loop_name}"
