#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
#
# Authors:
# Tonghao Zhang <tonghao@bamaicloud.com>
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

# Verify the iotracing CLI end-to-end: a normal run emits well-formed
# JSON with the expected top-level schema and the dd workload we drive
# shows up under per-file attribution. A private ext4 device keeps
# unrelated host IO out of the bounded capture and its top-N output.
# Real BPF contracts additionally cover accounting, metadata, capacity,
# device filters and complete object loading and attach.
# Path-validation rules are covered by internal/bpf TestValidateName.

set -exuo pipefail

source "${ROOT_DIR}/integration/lib.sh"

require_commands go
for fixture_tool in truncate losetup mkfs.ext4 mount umount mountpoint; do
	command -v "${fixture_tool}" > /dev/null 2>&1 \
		|| fatal "iotracing disk fixture requires ${fixture_tool}"
done
bpf_tool_setup iotracing
readonly DURATION=8
io_test_dir="${TOOL_WORK_DIR}/mnt"
loop_device=""
IO_PID=""
WORKLOAD_PID=""

cleanup() {
	local status=$?
	trap - EXIT
	[[ -n "${IO_PID}" ]] && stop_by_pid "${IO_PID}" 5 || true
	[[ -n "${WORKLOAD_PID}" ]] && stop_by_pid "${WORKLOAD_PID}" 5 || true
	if mountpoint -q "${io_test_dir}"; then
		umount "${io_test_dir}" || exit 1
	fi
	if [[ -n "${loop_device}" ]]; then
		losetup -d "${loop_device}" || status=1
	fi
	exit "${status}"
}
trap cleanup EXIT

# Device filtering happens before map insertion; filtering the host root
# device would still admit background package updates on that filesystem.
truncate -s 1G "${TOOL_WORK_DIR}/disk.img"
loop_device=$(losetup --find --show "${TOOL_WORK_DIR}/disk.img") \
	|| fatal "cannot create the iotracing loop device"
mkfs.ext4 -q -F "${loop_device}"
mkdir "${io_test_dir}"
mount "${loop_device}" "${io_test_dir}"
device=$(cat "/sys/class/block/${loop_device##*/}/dev")

log_info "iotracing: duration=${DURATION}s, device=${device}, io_test_dir=${io_test_dir}"
"${TOOL_BIN}" --bpf-path "${TOOL_BPF}" \
	--device "${device}" \
	--duration "${DURATION}" \
	--output json \
	> "${TOOL_OUT}" 2> "${TOOL_ERR}" &
IO_PID=$!

# Let probes attach before the workload starts.
sleep 1

# Reuse one inode and a bounded file so fast disks cannot fill the fixture.
timeout --kill-after=1s "$((DURATION - 2))" bash -c '
	while :; do
		dd if=/dev/zero of="$1" bs=1M count=16 oflag=dsync conv=notrunc status=none || exit
	done
' _ "${io_test_dir}/io" > "${TOOL_WORK_DIR}/dd.log" 2>&1 &
WORKLOAD_PID=$!

wait "${IO_PID}" || fatal "iotracing exited non-zero"
IO_PID=""
workload_status=0
wait "${WORKLOAD_PID}" || workload_status=$?
WORKLOAD_PID=""
[[ ${workload_status} -eq 124 ]] || fatal "dd workload exited with ${workload_status}, expected timeout (124)"
[[ -s ${TOOL_OUT} ]] || fatal "iotracing produced empty output"

# Envelope: parses as JSON, both top-level keys exist as arrays.
jq -e '
	(.process_file_io_stats | type == "array") and
	(.io_schedule_timeout_stacks | type == "array")
' "${TOOL_OUT}" > /dev/null \
	|| fatal "iotracing JSON schema invalid"

# Accuracy: a row attributed to dd holds the private file with non-zero
# write bps. The captured dentry path is relative to its filesystem root.
# comm may be "dd" (BPF capture) or "dd if=/dev/zero ..."
# (cmdline fallback when /proc still has the entry); match the leading
# token. Preserve the matched rows so a successful run shows which
# attribution data satisfied the accuracy check.
MATCHED_IO=$(jq -ce '
	[
		.process_file_io_stats[]
		| select(.comm | test("^dd( |$)")) as $process
		| $process.total_files[]?
		| (.fs_write_bps + .disk_write_bps) as $write_bps
		| select(.path == "/io")
		| select($write_bps > 0)
		| {
			pid: $process.pid,
			comm: $process.comm,
			path: .path,
			fs_write_bps: .fs_write_bps,
			disk_write_bps: .disk_write_bps,
			total_write_bps: $write_bps
		}
	]
	| select(length > 0)
' "${TOOL_OUT}") \
	|| fatal "no dd-attributed write to ${io_test_dir} found"

log_info "matched dd-attributed writes:"
echo "${MATCHED_IO}" | jq '.'

# Reuse this test's owned filesystem directory after the CLI capture exits.
TMPDIR="${io_test_dir}" TEST_IOTRACING_FILE_BPF="${TOOL_BPF}" \
	timeout --kill-after=5s 240s go test -mod=vendor -tags=integration ./cmd/iotracing \
	-run '^TestIOTracing' -count=1 -v -timeout=230s
