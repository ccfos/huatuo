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

# Exercise cgroup discovery and process-owned watch snapshots, including FD races.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_cgroup.sh"

readonly CGROUP_HELPER_ROOT="${HUATUO_BAMAI_TEST_TMPDIR}/cgroup helpers"
readonly CGROUP_HELPER_WATCH_ROOT="${CGROUP_HELPER_ROOT}/watch-targets"
readonly CGROUP_HELPER_CONTAINER_ID=$(printf '%064d' 1)
require_commands find python3 readlink awk stat mount umount

cgroup_helper_pid=""
cgroup_helper_mode=normal
cgroup_helper_watch_mounted=false

cleanup_cgroup_helper_fixture() {
	local status=$?
	if [[ -n "$cgroup_helper_pid" ]]; then
		kill "$cgroup_helper_pid" 2> /dev/null || true
		wait "$cgroup_helper_pid" 2> /dev/null || true
		cgroup_helper_pid=""
	fi
	if [[ "$cgroup_helper_watch_mounted" == true ]]; then
		umount "$CGROUP_HELPER_WATCH_ROOT" || status=1
	fi
	exit "$status"
}
trap cleanup_cgroup_helper_fixture EXIT

# Model mount availability without changing the host cgroup hierarchy.
findmnt() {
	case " $* " in
	*' -t cgroup '*)
		[[ -n "${cgroup_helper_v1}" ]] || return 1
		printf '%s\n' "${cgroup_helper_v1}"
		;;
	*' -t cgroup2 '*)
		[[ -n "${cgroup_helper_v2}" ]] || return 1
		printf '%s\n' "${cgroup_helper_v2}"
		;;
	*) return 1 ;;
	esac
}

cgroup_helper_v1="${CGROUP_HELPER_ROOT}/memory"
cgroup_helper_v2="${CGROUP_HELPER_ROOT}/unified"
mkdir -p "${cgroup_helper_v1}" "${cgroup_helper_v2}"
assert_eq "$(cgroup_memory_root)" "${cgroup_helper_v1}" "hybrid host prefers v1 memory controller"
assert_eq "$(cgroup_create fixture)" "${cgroup_helper_v1}/fixture" "create uses memory mount"
cgroup_delete "${cgroup_helper_v1}/fixture"
cgroup_helper_v1=""
assert_eq "$(cgroup_memory_root)" "${cgroup_helper_v2}" "v2 fallback"
cgroup_helper_v2=""
if cgroup_memory_root; then
	fatal "missing memory mount was accepted"
fi

for cgroup_helper_name in "${CGROUP_HELPER_CONTAINER_ID}" \
	"docker-${CGROUP_HELPER_CONTAINER_ID}.scope" \
	"cri-containerd-${CGROUP_HELPER_CONTAINER_ID}.scope" \
	"crio-${CGROUP_HELPER_CONTAINER_ID}.scope"; do
	mkdir "${CGROUP_HELPER_ROOT}/${cgroup_helper_name}"
	assert_eq "$(cgroup_find_container "${CGROUP_HELPER_ROOT}" "${CGROUP_HELPER_CONTAINER_ID}")" \
		"${CGROUP_HELPER_ROOT}/${cgroup_helper_name}" "container directory lookup"
	rmdir "${CGROUP_HELPER_ROOT}/${cgroup_helper_name}"
done

for cgroup_helper_id in "${CGROUP_HELPER_CONTAINER_ID}" '' '*'; do
	if cgroup_find_container "${CGROUP_HELPER_ROOT}" "${cgroup_helper_id}"; then
		fatal "missing or invalid container ID was accepted: ${cgroup_helper_id}"
	fi
done

log_info "memory mount selection and container cgroup lookup passed"

assert_cgroup_watch_device_encoding() (
	# Cgroup mount minor numbers can exceed the low eight bits of st_dev.
	stat() { printf '100 %d\n' "$((0x1200034))"; }
	assert_eq "$(cgroup_watch_identity unused)" 'ino:64 sdev:1234' "large device minor"
)
assert_cgroup_watch_device_encoding

# Btrfs subvolumes report an st_dev different from inotify's superblock device.
# Use a private tmpfs for watch targets; keep diagnostics outside the mount.
mkdir "$CGROUP_HELPER_WATCH_ROOT"
mount -t tmpfs -o size=1m,nosuid,nodev,noexec tmpfs "$CGROUP_HELPER_WATCH_ROOT"
cgroup_helper_watch_mounted=true

# Keep a real target watch while closing unrelated descriptors at scan boundaries.
python3 - "$CGROUP_HELPER_ROOT" "$CGROUP_HELPER_WATCH_ROOT" > "${CGROUP_HELPER_ROOT}/watch-process.log" 2>&1 << 'PY' &
import ctypes
import os
from pathlib import Path
import signal
import sys

root = Path(sys.argv[1])
target = Path(sys.argv[2]) / "memory.max"
target.touch()
libc = ctypes.CDLL(None, use_errno=True)
regular_fd = os.open("/dev/null", os.O_RDONLY)
unrelated_fd = libc.inotify_init1(os.O_CLOEXEC)
target_fd = libc.inotify_init1(os.O_CLOEXEC)
if unrelated_fd < 0 or target_fd < 0:
    raise OSError(ctypes.get_errno(), "inotify_init1")
if libc.inotify_add_watch(target_fd, os.fsencode(target), 2) < 0:
    raise OSError(ctypes.get_errno(), "inotify_add_watch")

def close_regular(signum, frame):
    os.close(regular_fd)
    (root / "regular.closed").touch()

def close_inotify(signum, frame):
    os.close(unrelated_fd)
    (root / "inotify.closed").touch()

signal.signal(signal.SIGUSR1, close_regular)
signal.signal(signal.SIGUSR2, close_inotify)
(root / "watch-fds").write_text(f"{regular_fd} {unrelated_fd} {target_fd}\n")
while True:
    signal.pause()
PY
cgroup_helper_pid=$!
wait_until 10 0.1 test -s "${CGROUP_HELPER_ROOT}/watch-fds" \
	|| fatal "watch workload did not initialize; see watch-process.log"
read -r cgroup_helper_regular_fd cgroup_helper_inotify_fd cgroup_helper_target_fd \
	< "${CGROUP_HELPER_ROOT}/watch-fds"
cgroup_helper_identity=$(cgroup_watch_identity "${CGROUP_HELPER_WATCH_ROOT}/memory.max")

readlink() {
	if [[ "$cgroup_helper_mode" == close_regular && "$1" == "/proc/${cgroup_helper_pid}/fd/${cgroup_helper_regular_fd}" ]]; then
		kill -USR1 "$cgroup_helper_pid"
		wait_until 5 0.01 test -f "${CGROUP_HELPER_ROOT}/regular.closed" > /dev/null || return 1
	fi
	if [[ "$cgroup_helper_mode" == readlink_failure && "$1" == "/proc/${cgroup_helper_pid}/fd/${cgroup_helper_target_fd}" ]]; then
		return 1
	fi
	command readlink "$@"
}

awk() {
	local file=${!#}
	if [[ "$cgroup_helper_mode" == close_inotify && "$file" == "/proc/${cgroup_helper_pid}/fdinfo/${cgroup_helper_inotify_fd}" ]]; then
		kill -USR2 "$cgroup_helper_pid"
		wait_until 5 0.01 test -f "${CGROUP_HELPER_ROOT}/inotify.closed" > /dev/null || return 1
	fi
	if [[ "$cgroup_helper_mode" == fdinfo_failure && "$file" == "/proc/${cgroup_helper_pid}/fdinfo/${cgroup_helper_target_fd}" ]]; then
		return 1
	fi
	command awk "$@"
}

assert_cgroup_watch_snapshot() {
	local mode=$1 expected_status=$2 status=0
	local output="${CGROUP_HELPER_ROOT}/${mode}.watches"
	cgroup_helper_mode=$mode
	[[ "$mode" != write_failure ]] || output=/dev/full
	cgroup_read_inotify_watches "$cgroup_helper_pid" "$output" \
		2> "${CGROUP_HELPER_ROOT}/${mode}.err" || status=$?
	assert_eq "$status" "$expected_status" "${mode}: snapshot exit status"
	if ((expected_status == 0)); then
		grep -qFx "$cgroup_helper_identity" "$output" \
			|| fatal "${mode}: live target watch missing from snapshot"
	fi
	log_info "PASS: ${mode} watch snapshot"
}

assert_cgroup_watch_snapshot normal 0
assert_cgroup_watch_snapshot close_regular 0
assert_cgroup_watch_snapshot close_inotify 0
assert_cgroup_watch_snapshot readlink_failure 1
assert_cgroup_watch_snapshot fdinfo_failure 1
assert_cgroup_watch_snapshot write_failure 1

# A valid empty watch set and an exited process must have different outcomes.
rm "${CGROUP_HELPER_WATCH_ROOT}/memory.max"
cgroup_helper_mode=normal
assert_cgroup_no_watches() {
	local output="${CGROUP_HELPER_ROOT}/removed.watches"
	cgroup_read_inotify_watches "$cgroup_helper_pid" "$output" \
		|| fatal "could not read watches after target removal"
	assert_eq "$(< "$output")" "" "removed target has no remaining watch"
}
assert_cgroup_no_watches
kill "$cgroup_helper_pid"
wait "$cgroup_helper_pid" 2> /dev/null || true
assert_cgroup_watch_snapshot exited_process 1
cgroup_helper_pid=""
