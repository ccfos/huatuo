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

# Check IO fixture ownership and qualification process cleanup.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
require_commands unshare mount mknod

unshare --mount --propagation private bash -s << 'FIXTURE'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
assert_io_disk_selection() {
    [[ ${io_test_disks[*]} == "$1" && ${io_test_disk_writable} == "$2" ]] \
        || fatal "unexpected disk selection: disks=${io_test_disks[*]} writable=${io_test_disk_writable}, expected $1/$2"
}

assert_io_disk_nonexclusive() {
    [[ ${TEST_IOLATENCY_IO_EXCLUSIVE} == 0 ]] || fatal "existing disk fallback was marked exclusive"
}

assert_io_disk_not_free() {
    ! io_test_disk_is_free "$1" || fatal "ineligible disk accepted as free: $1"
}

assert_io_fallback_cleanup() {
    [[ -b /dev/vda && -b /dev/vdb && -b /dev/sda ]] || fatal "cleanup removed an existing device"
    grep -q 'stage=released' "${HUATUO_BAMAI_TEST_TMPDIR}/io-disks.resources" || fatal "resource cleanup was not recorded"
}

fixture="${HUATUO_BAMAI_TEST_TMPDIR}/io-selection"
mkdir -p "${fixture}/bin"
cat > "${fixture}/bin/modinfo" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
cat > "${fixture}/bin/modprobe" <<'EOF'
#!/usr/bin/env bash
printf 'unexpected module mutation\n' >&2
exit 1
EOF
cat > "${fixture}/bin/lsblk" <<'EOF'
#!/usr/bin/env bash
device=${!#}
cat "/sys/block/${device##*/}/mounted"
EOF
chmod +x "${fixture}/bin/"*
export PATH="${fixture}/bin:${PATH}"
mount -t tmpfs tmpfs /sys
mount -t tmpfs tmpfs /dev
mknod /dev/null c 1 3
for name in vda vdb sda; do
    mkdir -p "/sys/block/${name}/queue" "/sys/block/${name}/holders"
    printf '1\n' > "/sys/block/${name}/queue/iostats"
    printf '262144\n' > "/sys/block/${name}/size"
    printf '0 0\n' > "/sys/block/${name}/inflight"
    : > "/sys/block/${name}/mounted"
    mknod "/dev/${name}" b 240 0
done
printf '/\n' > /sys/block/vda/mounted
# The default-policy fixture must not inherit the caller's write permission.
unset TEST_IO_ALLOW_FREE_DISK_WRITES
io_test_prepare_disks 1
assert_io_disk_selection vdb 0
TEST_IO_ALLOW_FREE_DISK_WRITES=1 io_test_prepare_disks 1
assert_io_disk_selection vdb 1
assert_io_disk_nonexclusive
printf '1 0\n' > /sys/block/vdb/inflight
assert_io_disk_not_free vdb
printf '0 0\n' > /sys/block/vdb/inflight
mkdir /sys/block/vdb/holders/dm-0
assert_io_disk_not_free vdb
TEST_IO_ALLOW_FREE_DISK_WRITES=1 io_test_prepare_disks 1
assert_io_disk_selection sda 0
printf '/data\n' > /sys/block/sda/mounted
TEST_IO_ALLOW_FREE_DISK_WRITES=1 io_test_prepare_disks 1
assert_io_disk_selection vda 0
io_test_cleanup_disks
assert_io_fallback_cleanup
FIXTURE
log_info "IO disk selection, explicit write authorization and fallback cleanup passed"

# Exercise the real component entry without loading BPF or creating devices.
require_commands python3 setsid ps
kernel_control_root="${HUATUO_BAMAI_TEST_TMPDIR}/kernel-control"
mkdir -p "${kernel_control_root}/bin"
cat > "${kernel_control_root}/bin/go" << 'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ $1 == test && " $* " == *' -c '* ]] || exit 90
[[ ${IO_KERNEL_CONTROL_MODE} != build-fail ]] || exit 17
while (($#)); do
    if [[ $1 == -o ]]; then
        cp "${IO_KERNEL_CONTROL_ROOT}/process.py" "$2"
        chmod +x "$2"
        exit
    fi
    shift
done
exit 91
EOF
cat > "${kernel_control_root}/process.py" << 'EOF'
#!/usr/bin/env python3
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

root = Path(os.environ["IO_KERNEL_CONTROL_DIR"])
mode = os.environ["IO_KERNEL_CONTROL_MODE"]
stopped = False

def interrupt(_signum, _frame):
    global stopped
    stopped = True

for signum in (signal.SIGINT, signal.SIGTERM):
    signal.signal(signum, signal.SIG_IGN if mode == "unresponsive" else interrupt)
child = subprocess.Popen(["sleep", "30"], preexec_fn=os.setpgrp)
with (root / "calls").open("a") as output:
    output.write("started\n")
(root / "pids").write_text(str(os.getpid()) + " " + str(child.pid))
(root / "ready").touch()
if mode == "descendant":
    sys.exit(0)
if mode in ("int", "term", "timeout", "unresponsive"):
    while not stopped:
        time.sleep(0.02)
time.sleep(0.2 if mode == "normal" else 2)
child.terminate()
child.wait()
(root / "cleaned").touch()
print("cleanup completed", flush=True)
if mode == "cleanup-fail":
    print("injected cleanup failure", file=sys.stderr, flush=True)
    sys.exit(9)
EOF
chmod +x "${kernel_control_root}/bin/go"

assert_io_kernel_control() {
	local mode=$1 expected=$2 actual=$3 directory=$4 pid
	assert_eq "${actual}" "${expected}" "IO qualification ${mode} exit status" \
		|| fatal "IO qualification ${mode} returned before the expected exit"
	if [[ ${mode} == build-fail ]]; then
		[[ ! -e ${directory}/ready ]] || fatal "failed compilation started an IO qualification"
		return
	fi
	[[ $(wc -l < "${directory}/calls") == 1 ]] || fatal "IO qualification continued after ${mode}"
	for pid in $(< "${directory}/pids"); do
		process_exited "${pid}" || fatal "IO qualification ${mode} retained live pid=${pid}"
	done
	case ${mode} in
	normal | int | term | timeout | cleanup-fail)
		[[ -f ${directory}/cleaned ]] || fatal "IO qualification ${mode} returned before cleanup completed"
		grep -q 'cleanup completed' "${directory}/entry.log" || fatal "IO qualification lost final cleanup output"
		;;
	esac
	if [[ ${mode} == cleanup-fail ]]; then
		grep -q 'injected cleanup failure' "${directory}/entry.log" || fatal "IO qualification hid the cleanup failure"
	fi
}

run_io_kernel_control() (
	local mode=$1 expected=$2 directory="${kernel_control_root}/$1" entry_pid="" status=0 budget=10 cleanup=4 selection=md
	mkdir -p "${directory}"
	trap '
		if [[ -n "${entry_pid}" ]]; then
			kill -TERM "${entry_pid}" 2>/dev/null || true
			wait "${entry_pid}" || true
		fi
		if [[ -s "${directory}/pids" ]]; then
			for control_pid in $(< "${directory}/pids"); do
				process_exited "${control_pid}" || kill -KILL "${control_pid}" 2>/dev/null || true
			done
		fi
	' EXIT
	case ${mode} in timeout | unresponsive) budget=1 ;; esac
	[[ ${mode} != unresponsive ]] || cleanup=2
	[[ ${mode} != cleanup-fail ]] || selection=collectors
	# Background Bash inherits ignored SIGINT. Reset it before exec, as a
	# foreground runner invocation would, so the INT control tests a real signal.
	IO_KERNEL_CONTROL_ROOT="${kernel_control_root}" IO_KERNEL_CONTROL_DIR="${directory}" \
		IO_KERNEL_CONTROL_MODE="${mode}" PATH="${kernel_control_root}/bin:${PATH}" \
		HUATUO_BAMAI_TEST_TMPDIR="${directory}" TEST_IO_KERNEL_MODE="${selection}" \
		TEST_IO_KERNEL_TIMEOUT_SECONDS="${budget}" TEST_IO_KERNEL_CLEANUP_TIMEOUT_SECONDS="${cleanup}" \
		python3 -c 'import os, signal, sys; signal.signal(signal.SIGINT, signal.SIG_DFL); os.execvp("bash", ["bash", sys.argv[1]])' \
		"${ROOT_DIR}/integration/test_io_kernel.sh" > "${directory}/entry.log" 2>&1 &
	entry_pid=$!
	case ${mode} in
	int | term)
		wait_until 5 0.1 test -f "${directory}/ready" || fatal "IO ${mode} control did not start"
		if [[ ${mode} == int ]]; then kill -INT "${entry_pid}"; else kill -TERM "${entry_pid}"; fi
		;;
	esac
	wait "${entry_pid}" || status=$?
	entry_pid=""
	assert_io_kernel_control "${mode}" "${expected}" "${status}" "${directory}"
	log_info "IO qualification control ${mode}: exit=${status}, process cleanup verified"
)

run_io_kernel_control normal 0
run_io_kernel_control int 130
run_io_kernel_control term 143
run_io_kernel_control timeout 124
run_io_kernel_control cleanup-fail 9
run_io_kernel_control descendant 1
run_io_kernel_control unresponsive 1
run_io_kernel_control build-fail 17
log_info "IO qualification waits for tests and workloads, preserves cleanup errors and stops after interruption"
