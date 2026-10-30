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

# Verify block errors, MD states and SCSI faults against public evidence.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory
source "${ROOT_DIR}/integration/lib_io_health.sh"

test_block_health_events() {
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
		# A direct-write fallback may only dirty the cache; fsync must expose
		# the sealed backing store's writeback error before dd reports success.
		set +e
		timeout 10 dd \
			if=/dev/zero \
			of="${loop_device}" \
			bs=4096 \
			count=1 \
			oflag=direct \
			conv=fsync \
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

		huatuo_bamai_metrics > "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" \
			|| return 1
		awk -v device="${device}" '
		$1 ~ /^huatuo_bamai_io_health_block_errors_total\{/ &&
		$1 ~ ("disk=\"" device "\"") &&
		$1 ~ /operation="write"/ &&
		$1 ~ /status="io_error"/ &&
		$2 + 0 > 0 {
			block_error = 1
		}
		$1 ~ /^huatuo_bamai_io_health_collection_errors_total\{/ &&
		$1 ~ ("disk=\"" device "\"") &&
		$1 ~ /reason="target_unsupported"/ &&
		$2 + 0 > 0 {
			unsupported = 1
		}
		END { exit !(block_error && unsupported) }
	' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	}

	io_health_event_saved() {
		local record_file="${HUATUO_BAMAI_TEST_TMPDIR}/events/io_health"
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

	tracefs=""
	for candidate in /sys/kernel/tracing /sys/kernel/debug/tracing; do
		if [[ -r "${candidate}/events/block/block_rq_complete/id" ]]; then
			tracefs="${candidate}"
			break
		fi
	done
	[[ -n "${tracefs}" ]] \
		|| skip "IO health event test requires block_rq_complete"

	io_test_start_bamai write_config_bamai_io_health \
		--region dev --disable-kubelet --log-debug
	assert_io_health_bpf_ready 'block/block_rq_error|block_rq_complete' \
		|| fatal "block error collection is not ready; no fault was injected"

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
		|| fatal "cannot bind the IO health loop fixture"
	loop_name=$(basename "${loop_device}")
	[[ -b "${loop_device}" ]] \
		|| fatal "losetup did not create a block device: ${loop_device}"
	[[ "$(blockdev --getsize64 "${loop_device}")" -eq $((8 * 1024 * 1024)) ]] \
		|| fatal "unexpected loop capacity for ${loop_device}"

	touch "${memfd_seal_request}"
	wait_until 10 1 \
		test -e "${memfd_sealed}" \
		|| fatal "memfd helper could not seal the loop backing store"

	log_info "IO Health fault injection: block disk=${loop_name}"
	inject_io_health_error
	wait_until 15 1 \
		io_health_event_observed "${loop_name}" \
		|| fatal "io_health did not publish the loop block error"

	wait_until 10 1 \
		io_health_event_saved "${loop_name}" \
		|| fatal "io_health counter advanced without a saved block error"

	log_info "io_health observed and saved a real block error for ${loop_name}"
}

test_md_health_events() {
	set -euo pipefail

	source "${ROOT_DIR}/integration/lib.sh"
	source "${ROOT_DIR}/integration/config.sh"
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	WAIT_HUATUO_BAMAI_TIMEOUT=30

	require_commands awk jq losetup mdadm timeout truncate
	require_readable /proc/mdstat /proc/sys/kernel/random/uuid
	[[ ${EUID} -eq 0 ]] || skip "MD health test requires root"

	readonly MD_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/md"
	readonly MD_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/io_health"
	mkdir -p "${MD_WORK}"
	images=("${MD_WORK}/member-0.img" "${MD_WORK}/member-1.img")
	io_health_md_init "${MD_WORK}" htio
	cleanup_md_health() {
		local status=$? failed=0
		trap - EXIT
		trap '' INT TERM
		set +e
		if io_health_md_stop_daemon; then
			io_health_md_cleanup || failed=1
			((failed)) || io_test_cleanup_disks || failed=1
		else
			failed=1
		fi
		if ((failed != 0)); then
			log_error "MD cleanup incomplete; resource identities and recovery order are in ${MD_WORK}/resources.txt"
			status=1
		fi
		exit "${status}"
	}

	trap cleanup_md_health EXIT
	trap 'log_error "MD health test interrupted by INT; entering cleanup"; exit 130' INT
	trap 'log_error "MD health test interrupted by TERM or execution timeout; entering cleanup"; exit 143' TERM

	assert_md_member_has_state() {
		local member=$1 state=$2
		[[ -r "${md_sysfs}/md/dev-${member}/state" ]] || return 1
		grep -Eq "(^|,)${state}(,|$)" "${md_sysfs}/md/dev-${member}/state"
	}

	assert_md_degraded_is() {
		[[ -r "${md_sysfs}/md/degraded" ]] && [[ "$(< "${md_sysfs}/md/degraded")" == "$1" ]]
	}

	assert_md_initial_events() {
		[[ -s ${MD_EVENT} ]] || return 1
		jq -e -s --arg array "${md_name}" '
		all(.[]; .tracer_name != "io_health" or .tracer_data.array != $array or
			(.tracer_data.type != "md_degraded" and .tracer_data.type != "md_member_state"))
	' "${MD_EVENT}" > /dev/null 2>&1
	}

	assert_md_fault_events_saved() {
		[[ -s ${MD_EVENT} ]] || return 1
		jq -e -s --arg array "${md_name}" --arg member "${failed_member}" '
		[.[] | select(.tracer_name == "io_health" and .tracer_type == "event"
			and .tracer_data.array == $array) | .tracer_data] as $events
		| any($events[]; .type == "md_degraded" and .old_state == "0" and .new_state == "1")
		and any($events[]; .type == "md_member_state" and .member == $member
			and (.old_state | split(",") | index("in_sync")) != null
			and (.new_state | split(",") | index("faulty")) != null)
		and all($events[] | select(.type == "md_degraded" or .type == "md_member_state");
			(.old_state | type == "string" and length > 0)
			and (.new_state | type == "string" and length > 0)
			and .old_state != .new_state)
	' "${MD_EVENT}" > /dev/null 2>&1
	}

	assert_md_removed_event_saved() {
		[[ -s ${MD_EVENT} ]] || return 1
		jq -e -s --arg array "${md_name}" --arg member "${failed_member}" '
		any(.[]; .tracer_name == "io_health" and .tracer_type == "event"
			and .tracer_data.type == "md_member_state" and .tracer_data.array == $array
			and .tracer_data.member == $member and .tracer_data.new_state == "removed"
			and (.tracer_data.old_state | split(",") | index("faulty")) != null)
	' "${MD_EVENT}" > /dev/null 2>&1
	}

	for image in "${images[@]}"; do
		io_health_md_create_loop "${image}"
	done
	loop_devices=("${io_health_loop_devices[@]}")
	io_health_md_create --size=4096 "${loop_devices[@]}"
	md_name=${md_device##*/}
	failed_member=${loop_devices[1]##*/}
	wait_until 10 0.2 assert_md_member_has_state "${failed_member}" in_sync || fatal "test member never reached in_sync"
	wait_until 10 0.2 assert_md_degraded_is 0 || fatal "test array was not initially healthy"
	cp "${md_sysfs}/md/dev-${failed_member}/state" "${MD_WORK}/member.before"
	cp "${md_sysfs}/md/degraded" "${MD_WORK}/degraded.before"

	io_test_start_bamai write_config_bamai_io_health --region dev --disable-kubelet --log-debug
	assert_io_health_md_observer_ready "${MD_EVENT}" || fatal "MD observer is not ready for fault injection"
	io_health_md_wait_udev || fatal "udev did not settle before member fault injection"
	assert_io_health_md_array_healthy || fatal "RAID1 is not healthy and writable before fault; inspect ${MD_WORK}/array-health.txt"
	assert_md_initial_events || fatal "healthy initial RAID1 state produced a member or degraded transition"
	io_health_md_array_is_owned || fatal "array ownership changed before member fault injection"
	io_health_md_command mdadm "${md_device}" --fail "${loop_devices[1]}" || fatal "cannot fault the test RAID1 member"
	wait_until 10 0.2 assert_md_member_has_state "${failed_member}" faulty || fatal "mdadm did not produce faulty member state"
	wait_until 10 0.2 assert_md_degraded_is 1 || fatal "array degraded count did not become 1"
	cp "${md_sysfs}/md/dev-${failed_member}/state" "${MD_WORK}/member.failed"
	cp "${md_sysfs}/md/degraded" "${MD_WORK}/degraded.failed"
	wait_until 30 0.5 assert_md_fault_events_saved \
		|| fatal "confirmed RAID1 fault lacks saved member/degraded health events"
	cp "${MD_EVENT}" "${MD_WORK}/fault-events.json"

	io_health_md_array_is_owned || fatal "array ownership changed before member removal"
	io_health_md_command mdadm "${md_device}" --remove "${loop_devices[1]}" || fatal "cannot remove the test faulty member"
	wait_until 10 0.2 test ! -e "${md_sysfs}/md/dev-${failed_member}" \
		|| fatal "removed member still exists in public MD sysfs"
	wait_until 30 0.5 assert_md_removed_event_saved \
		|| fatal "confirmed RAID1 member removal lacks saved removed health event"
	assert_md_fault_events_saved || fatal "MD events contain an invalid or missing state transition after member removal"
	cp "${MD_EVENT}" "${MD_WORK}/removed-events.json"
	log_info "H04: saved member fault, degraded 0->1, and member removal match public MD state"
	exit 0
}

test_scsi_health_events() {
	set -euo pipefail
	source "${ROOT_DIR}/integration/config.sh"
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
	WAIT_HUATUO_BAMAI_TIMEOUT=30
	require_commands python3 jq timeout modprobe modinfo udevadm readlink
	[[ ${EUID} -eq 0 ]] || skip "SCSI health events require root"
	[[ ! -e /sys/module/scsi_debug ]] || skip "SCSI fault injection requires an owned scsi_debug module"

	readonly SCSI_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/scsi-events"
	readonly SCSI_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/io_health"
	readonly SCSI_CONTROLS=/sys/bus/pseudo/drivers/scsi_debug
	mkdir -p "${SCSI_WORK}"
	scsi_trace=""
	scsi_trace_owned=0
	scsi_trace_pid=""
	scsi_trace_log="${SCSI_WORK}/scsi.trace"
	scsi_io_pid=""
	scsi_settings_saved=0
	scsi_disk=""

	scsi_fixture_is_owned() {
		local owner
		((io_test_scsi_owned)) || return 1
		read -r owner < /sys/module/scsi_debug/parameters/inq_vendor || return 1
		[[ ${owner} == "${io_test_scsi_owner}" ]] || return 1
		read -r owner < "/sys/class/block/${scsi_disk}/device/vendor" || return 1
		[[ ${owner} == "${io_test_scsi_owner}" ]]
	}

	restore_scsi_settings() {
		((scsi_settings_saved)) || return 0
		scsi_fixture_is_owned || return 1
		printf '0\n' > "${SCSI_CONTROLS}/every_nth" || return 1
		printf '%s\n' "${scsi_original_opts}" > "${SCSI_CONTROLS}/opts" || return 1
		printf '%s\n' "${scsi_original_nth}" > "${SCSI_CONTROLS}/every_nth" || return 1
		printf '%s\n' "${scsi_original_timeout}" > "/sys/class/block/${scsi_disk}/device/timeout" || return 1
		[[ $(< "${SCSI_CONTROLS}/opts") == "${scsi_original_opts}" &&
		$(< "${SCSI_CONTROLS}/every_nth") == "${scsi_original_nth}" &&
		$(< "/sys/class/block/${scsi_disk}/device/timeout") == "${scsi_original_timeout}" ]]
	}

	cleanup_scsi_health() {
		local status=$? failed=0 trace_status=0
		trap - EXIT
		trap '' INT TERM
		set +e
		restore_scsi_settings || failed=1
		if [[ -n ${scsi_io_pid} ]]; then
			# The request already has a timeout. Killing its wrapper before the
			# kernel finishes error recovery can strand a command on our disk.
			wait "${scsi_io_pid}" > "${SCSI_WORK}/io-cleanup.log" 2>&1 || failed=1
			scsi_io_pid=""
		fi
		huatuo_bamai_stop || failed=1
		if ((scsi_trace_owned)); then
			printf '0\n' > "${scsi_trace}/tracing_on" || failed=1
			printf '0\n' > "${scsi_trace}/events/enable" || failed=1
			if [[ -n ${scsi_trace_pid} ]]; then
				# The runner may have already signaled the reader; reap it either way.
				kill -TERM "${scsi_trace_pid}" 2> /dev/null || true
				wait "${scsi_trace_pid}" || trace_status=$?
				[[ ${trace_status} == 0 || ${trace_status} == 143 ]] || failed=1
				scsi_trace_pid=""
				cp "${scsi_trace_log}" "${SCSI_WORK}/cleanup.trace" || failed=1
			fi
			rmdir -- "${scsi_trace}" || failed=1
		fi
		if ((failed == 0)); then io_test_cleanup_disks || failed=1; fi
		if ((failed)); then
			log_error "SCSI cleanup incomplete; inspect ${SCSI_WORK}/resources.txt and io-disks.resources"
			status=1
		fi
		exit "${status}"
	}
	trap cleanup_scsi_health EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM

	io_test_prepare_scsi_disks 1
	((io_test_scsi_owned)) || skip "SCSI fault injection has no owned scsi_debug disk"
	scsi_disk=${io_test_disks[0]}
	scsi_fixture_is_owned || fatal "SCSI fixture ownership changed"
	tracefs=""
	for candidate in /sys/kernel/tracing /sys/kernel/debug/tracing; do
		[[ -d ${candidate}/instances && -r ${candidate}/events/scsi/scsi_dispatch_cmd_timeout/format &&
			-r ${candidate}/events/scsi/scsi_dispatch_cmd_error/format ]] || continue
		tracefs=${candidate}
		break
	done
	[[ -n ${tracefs} ]] || skip "SCSI health events require private tracefs instances and both SCSI tracepoints"
	timeout --kill-after=1s 10s udevadm settle --timeout=8 || fatal "udev did not settle before SCSI fault injection"
	require_readable "${SCSI_CONTROLS}/opts" "${SCSI_CONTROLS}/every_nth" \
		"/sys/class/block/${scsi_disk}/device/timeout"
	scsi_original_opts=$(< "${SCSI_CONTROLS}/opts")
	scsi_original_nth=$(< "${SCSI_CONTROLS}/every_nth")
	scsi_original_timeout=$(< "/sys/class/block/${scsi_disk}/device/timeout")
	scsi_settings_saved=1
	printf 'disk=%s owner=%s opts=%s every_nth=%s timeout=%s\n' \
		"${scsi_disk}" "${io_test_scsi_owner}" "${scsi_original_opts}" \
		"${scsi_original_nth}" "${scsi_original_timeout}" > "${SCSI_WORK}/resources.txt"
	printf '1\n' > "/sys/class/block/${scsi_disk}/device/timeout"
	scsi_hctl=$(basename "$(readlink -f "/sys/class/block/${scsi_disk}/device")")
	[[ ${scsi_hctl} =~ ^[0-9]+:[0-9]+:[0-9]+:[0-9]+$ ]] || fatal "invalid SCSI fixture address: ${scsi_hctl}"
	IFS=: read -r scsi_host scsi_channel scsi_target scsi_lun <<< "${scsi_hctl}"
	scsi_trace="${tracefs}/instances/huatuo-scsi-${io_test_scsi_owner}"
	mkdir "${scsi_trace}" || skip "cannot create a private SCSI tracefs instance"
	scsi_trace_owned=1
	printf 'trace_instance=%s hctl=%s\n' "${scsi_trace}" "${scsi_hctl}" >> "${SCSI_WORK}/resources.txt"
	for event in scsi_dispatch_cmd_timeout scsi_dispatch_cmd_error; do
		cp "${scsi_trace}/events/scsi/${event}/format" "${SCSI_WORK}/${event}.format"
		printf 'host_no == %s && channel == %s && id == %s && lun == %s\n' \
			"${scsi_host}" "${scsi_channel}" "${scsi_target}" "${scsi_lun}" \
			> "${scsi_trace}/events/scsi/${event}/filter"
		printf '1\n' > "${scsi_trace}/events/scsi/${event}/enable"
		cp "${scsi_trace}/events/scsi/${event}/filter" "${SCSI_WORK}/${event}.filter"
		cp "${scsi_trace}/events/scsi/${event}/enable" "${SCSI_WORK}/${event}.enable"
	done
	printf '1\n' > "${scsi_trace}/tracing_on"
	cp "${scsi_trace}/tracing_on" "${SCSI_WORK}/tracing_on"
	# Reading trace pauses recording. Drain trace_pipe continuously and poll
	# its ordinary log so the independent witness has no polling gaps.
	cat "${scsi_trace}/trace_pipe" > "${scsi_trace_log}" 2> "${SCSI_WORK}/trace-reader.err" &
	scsi_trace_pid=$!
	printf 'trace_reader_pid=%s trace_log=%s\n' "${scsi_trace_pid}" "${scsi_trace_log}" >> "${SCSI_WORK}/resources.txt"

	assert_scsi_trace_event() {
		kill -0 "${scsi_trace_pid}" 2> /dev/null \
			|| fatal "SCSI trace reader exited; inspect ${SCSI_WORK}/trace-reader.err"
		grep -q "${scsi_trace_event}:" "${scsi_trace_log}"
	}

	# SG_IO supplies a bounded READ(10); Shell owns injection and all assertions.
	cat > "${SCSI_WORK}/read.py" << 'PY'
import ctypes as c
import json
import os
import sys
import time


class Request(c.Structure):
    _fields_ = [
        ("interface_id", c.c_int), ("dxfer_direction", c.c_int),
        ("cmd_len", c.c_ubyte), ("mx_sb_len", c.c_ubyte),
        ("iovec_count", c.c_ushort), ("dxfer_len", c.c_uint),
        ("dxferp", c.c_void_p), ("cmdp", c.c_void_p), ("sbp", c.c_void_p),
        ("timeout", c.c_uint), ("flags", c.c_uint), ("pack_id", c.c_int),
        ("usr_ptr", c.c_void_p), ("status", c.c_ubyte),
        ("masked_status", c.c_ubyte), ("msg_status", c.c_ubyte),
        ("sb_len_wr", c.c_ubyte), ("host_status", c.c_ushort),
        ("driver_status", c.c_ushort), ("resid", c.c_int),
        ("duration", c.c_uint), ("info", c.c_uint),
    ]


command = (c.c_ubyte * 10)(0x28, 0, 0, 0, 0, 0, 0, 0, 1, 0)
sense = c.create_string_buffer(64)
data = c.create_string_buffer(int(sys.argv[2]))
request = Request()
request.interface_id = ord("S")
request.dxfer_direction = -3
request.cmd_len = len(command)
request.mx_sb_len = len(sense)
request.dxfer_len = len(data)
request.dxferp = c.addressof(data)
request.cmdp = c.addressof(command)
request.sbp = c.addressof(sense)
request.timeout = 1000
libc = c.CDLL(None, use_errno=True)
libc.ioctl.argtypes = [c.c_int, c.c_ulong, c.c_void_p]
libc.ioctl.restype = c.c_int
started = time.monotonic()
fd = os.open(sys.argv[1], os.O_RDONLY | os.O_NONBLOCK)
try:
    result = libc.ioctl(fd, 0x2285, c.byref(request))
finally:
    os.close(fd)
print(json.dumps({"result": result, "errno": c.get_errno(),
                  "status": request.status, "host_status": request.host_status,
                  "driver_status": request.driver_status,
                  "seconds": time.monotonic() - started}), flush=True)
PY
	scsi_block_size=$(< "/sys/class/block/${scsi_disk}/queue/logical_block_size")
	run_scsi_read() {
		local result=0
		timeout --kill-after=2s 12s python3 "${SCSI_WORK}/read.py" \
			"/dev/${scsi_disk}" "${scsi_block_size}" > "${SCSI_WORK}/$1.read.json" \
			2> "${SCSI_WORK}/$1.read.err" &
		scsi_io_pid=$!
		wait "${scsi_io_pid}" || result=$?
		scsi_io_pid=""
		((result == 0)) || fatal "SCSI READ(10) helper failed (${result}); inspect ${SCSI_WORK}/$1.read.err"
	}
	assert_scsi_read_succeeded() {
		jq -e '.result == 0 and .status == 0 and .host_status == 0 and .driver_status == 0' \
			"${SCSI_WORK}/$1.read.json" > /dev/null
	}
	assert_scsi_dispatch_host_busy() {
		grep -Eq 'rtn=(4181|SCSI_MLQUEUE_HOST_BUSY)( |$)' "$1" \
			|| fatal "SCSI dispatch witness is not HOST_BUSY; inspect $1"
	}
	run_scsi_read baseline
	assert_scsi_read_succeeded baseline || fatal "owned SCSI disk did not complete the baseline read"
	io_test_start_bamai write_config_bamai_io_health --region dev --disable-kubelet --log-debug
	assert_io_health_bpf_ready scsi/scsi_dispatch_cmd_timeout scsi/scsi_dispatch_cmd_error \
		|| fatal "SCSI error collection is not ready; no fault was injected"
	scsi_fixture_hostname=$(< /proc/sys/kernel/hostname)

	assert_scsi_counter_value() {
		jq -er --arg name "${scsi_metric}" --arg disk "${scsi_disk}" \
			--arg status "${scsi_status}" --arg host "${scsi_fixture_hostname}" '
			[.[] | select(.name == $name and .labels.disk == $disk
				and ($status == "" or .labels.status == $status))] as $samples
			| if ($samples | length) == 0 then 0
			  elif ($samples | length) == 1
				and ($samples[0] | .type == "counter" and .type_count == 1
					and .labels.host == $host and .labels.region == "dev"
					and (.value | type == "number" and . >= 0 and floor == .))
			  then $samples[0].value
			  else error("invalid SCSI Counter type, labels or value") end
		' "$1"
	}
	assert_scsi_health_output() {
		local value input="${SCSI_WORK}/$1.metrics"
		huatuo_bamai_metrics > "${input}" || return 1
		io_test_metrics_json "${input}" > "${input}.json" \
			|| fatal "cannot decode SCSI health metrics"
		value=$(assert_scsi_counter_value "${input}.json") \
			|| fatal "invalid SCSI health Counter; inspect ${input}.json"
		((value > scsi_before)) || return 1
		[[ -s ${SCSI_EVENT} ]] || return 1
		jq -e -s --arg type "${scsi_type}" --arg disk "${scsi_disk}" --arg status "${scsi_status}" '
			any(.[]; .tracer_name == "io_health" and .tracer_type == "event"
				and .tracer_data.type == $type and .tracer_data.device == $disk
				and ($status == "" or .tracer_data.io_error_status == $status))
		' "${SCSI_EVENT}" > /dev/null 2>&1
	}

	for scsi_kind in timeout dispatch; do
		scsi_status=""
		scsi_type=scsi_timeout
		scsi_metric=huatuo_bamai_io_health_scsi_timeouts_total
		scsi_trace_event=scsi_dispatch_cmd_timeout
		# Media-access timeouts leave discovery and recovery commands working.
		scsi_opts=128
		if [[ ${scsi_kind} == dispatch ]]; then
			scsi_status=host_busy
			scsi_type=scsi_dispatch_error
			scsi_metric=huatuo_bamai_io_health_scsi_dispatch_errors_total
			scsi_trace_event=scsi_dispatch_cmd_error
			scsi_opts=32768
		fi
		huatuo_bamai_metrics > "${SCSI_WORK}/${scsi_kind}.before.metrics"
		io_test_metrics_json "${SCSI_WORK}/${scsi_kind}.before.metrics" > "${SCSI_WORK}/${scsi_kind}.before.json"
		scsi_before=$(assert_scsi_counter_value "${SCSI_WORK}/${scsi_kind}.before.json")
		scsi_label="${scsi_kind}-1"
		log_info "IO Health fault injection: SCSI ${scsi_kind} disk=${scsi_disk}"
		scsi_fixture_is_owned || fatal "SCSI fixture ownership changed before injection"
		# Use the driver's runtime controls so its option-dependent state updates.
		printf '%s\n' "${scsi_opts}" > "${SCSI_CONTROLS}/opts"
		printf '1\n' > "${SCSI_CONTROLS}/every_nth"
		printf '1\n' > "/sys/class/block/${scsi_disk}/device/timeout"
		timeout --kill-after=2s 12s python3 "${SCSI_WORK}/read.py" \
			"/dev/${scsi_disk}" "${scsi_block_size}" > "${SCSI_WORK}/${scsi_label}.read.json" \
			2> "${SCSI_WORK}/${scsi_label}.read.err" &
		scsi_io_pid=$!
		printf 'phase=%s io_pid=%s\n' "${scsi_label}" "${scsi_io_pid}" >> "${SCSI_WORK}/resources.txt"
		scsi_witness=0
		if wait_until 10 0.1 assert_scsi_trace_event; then scsi_witness=1; fi
		restore_scsi_settings || fatal "cannot restore SCSI fault settings"
		scsi_read_status=0
		wait "${scsi_io_pid}" || scsi_read_status=$?
		scsi_io_pid=""
		cp "${scsi_trace_log}" "${SCSI_WORK}/${scsi_label}.trace"
		((scsi_read_status == 0)) || fatal "SCSI fault input did not finish (${scsi_read_status}); inspect ${SCSI_WORK}"
		((scsi_witness)) || fatal "SCSI ${scsi_kind} injection lacks an independent target event; inspect ${SCSI_WORK}/${scsi_label}.trace"
		if [[ ${scsi_kind} == dispatch ]]; then
			assert_scsi_dispatch_host_busy "${SCSI_WORK}/${scsi_label}.trace"
		fi
		run_scsi_read "${scsi_label}-recovered"
		assert_scsi_read_succeeded "${scsi_label}-recovered" || fatal "owned SCSI disk did not recover after ${scsi_kind}"
		wait_until 10 0.2 assert_scsi_health_output "${scsi_label}" \
			|| fatal "confirmed SCSI ${scsi_kind} input lacks matching public Counter and saved event; inspect ${SCSI_WORK}"
		cp "${SCSI_EVENT}" "${SCSI_WORK}/${scsi_kind}.events.json"
		log_info "H03: SCSI ${scsi_kind} Counter and saved event match the independently observed target event"
	done
}

case ${TEST_IO_HEALTH_EVENT_CASE:-all} in
all) io_test_run_cases "$0" TEST_IO_HEALTH_EVENT_CASE block md scsi ;;
block) test_block_health_events ;;
md) test_md_health_events ;;
scsi) test_scsi_health_events ;;
*) fatal "TEST_IO_HEALTH_EVENT_CASE must be all, block, md or scsi" ;;
esac
