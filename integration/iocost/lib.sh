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

# This file provides shared IOCOST qualification setup, safety, and cleanup helpers.

set -euo pipefail

if [[ -n ${__HUATUO_IOCOST_LIB_LOADED:-} ]]; then
	return 0
fi
readonly __HUATUO_IOCOST_LIB_LOADED=1

readonly IOCOST_TEST_NAME="IOCOST OETEST"
readonly IOCOST_REQUIRED_VALUE="1"
readonly IOCOST_LOCK_DIR="/run/huatuo-iocost-oetest"
readonly IOCOST_LOCK_PATH="${IOCOST_LOCK_DIR}/runner.lock"
readonly IOCOST_CGROUP_ROOT="/sys/fs/cgroup"
readonly IOCOST_RUNTIME_TEST_REL="_output/oetest/bin/iocost-runtime.test"
readonly IOCOST_DAEMON_REL="_output/oetest/bin/huatuo-bamai"
readonly IOCOST_PRODUCTION_BPF_REL="bpf/iocost_tracing.o"
readonly IOCOST_TEST_BPF_REL="_output/test-bpf/iocost_tracing_test.o"
readonly IOCOST_MANIFEST_REL="_output/oetest/SHA256SUMS"
readonly IOCOST_HEAD_REL="_output/oetest/HEAD"
readonly IOCOST_RUNTIME_TEST_NAME="^TestIOCostStartAttachSmoke$"
readonly IOCOST_RUNTIME_TIMEOUT_SECONDS=180
readonly IOCOST_BACKING_BYTES=$((1024 * 1024 * 1024))
readonly IOCOST_IO_BLOCK_SIZE=4096
readonly IOCOST_WAIT_TIMEOUT_SECONDS=120
readonly IOCOST_STOP_TIMEOUT_SECONDS=10
readonly IOCOST_LOOP_CLEANUP_TIMEOUT_SECONDS=10

IOCOST_ROOT_DIR=""
IOCOST_TMP_DIR=""
IOCOST_BACKING_FILE=""
IOCOST_LOOP_MINOR=""
IOCOST_DEVICE_ID=""
IOCOST_DEVICE_MAJOR=""
IOCOST_DEVICE_FIRST_MINOR=""
IOCOST_CGROUP_PARENT=""
IOCOST_CGROUP_LEAF=""
IOCOST_CGROUP_PARKING=""
IOCOST_KERNEL_RELEASE=""
IOCOST_KERNEL_CONFIG=""
IOCOST_APPROVED_HEAD=""
IOCOST_APPROVED_HEAD_BEFORE=""
IOCOST_ARTIFACT_DIGESTS_BEFORE=""
IOCOST_OBSERVED_IOCG_PTR=""
IOCOST_OBSERVED_CSS_SERIAL=""
IOCOST_LAST_PID=""
IOCOST_LAST_RESULT=""
IOCOST_LAST_SCRAPE=""
IOCOST_LAST_AVERAGE=""
IOCOST_DAEMON_PID=""
IOCOST_DAEMON_PORT=""
IOCOST_DAEMON_REGION="iocost-oetest"
IOCOST_DAEMON_DIR=""
IOCOST_DAEMON_START_SEQUENCE=0
IOCOST_LOCK_OPEN=0
IOCOST_TMP_CREATED=0
IOCOST_CGROUP_CREATED=0
IOCOST_CLEANUP_RAN=0
IOCOST_SIGNAL_CRITICAL_DEPTH=0
IOCOST_PENDING_SIGNAL_STATUS=0

declare -a IOCOST_TRACKED_PIDS=()
declare -A IOCOST_PID_STARTTIME=()
declare -A IOCOST_PID_PGID=()
declare -A IOCOST_PID_LABEL=()
declare -A IOCOST_SCRAPED_WINDOWS=()

iocost_log() {
	printf '[%s] %s\n' "${IOCOST_TEST_NAME}" "$*"
}

iocost_warn() {
	printf '[%s][WARN] %s\n' "${IOCOST_TEST_NAME}" "$*" >&2
}

iocost_die() {
	printf '[%s][FAIL] %s\n' "${IOCOST_TEST_NAME}" "$*" >&2
	exit 1
}

# A signal may be delivered after a kernel/filesystem mutation or process
# spawn succeeds but before its cleanup identity is recorded. Every such
# ownership transition is enclosed by enter/leave: INT/TERM is remembered,
# registration completes, and the pending signal is then re-raised as an exit
# status so the normal cleanup trap sees the new resource. A foreground command
# which performs the mutation must also run through
# iocost_signal_critical_command: setsid isolates it before exec, so a terminal
# process-group signal either kills setsid before any mutation or is handled and
# deferred by this shell while the isolated mutation completes.
iocost_signal_critical_enter() {
	IOCOST_SIGNAL_CRITICAL_DEPTH=$((IOCOST_SIGNAL_CRITICAL_DEPTH + 1))
}

iocost_signal_critical_command() {
	((IOCOST_SIGNAL_CRITICAL_DEPTH > 0)) \
		|| iocost_die "signal-critical command used outside a critical section"
	# Callers execute this helper in a command-substitution or explicit
	# subshell. exec removes that pre-setsid wrapper: a group signal before
	# setsid means the mutation command never starts; afterwards it is isolated.
	exec setsid "$@"
}

iocost_signal_critical_leave() {
	local status
	((IOCOST_SIGNAL_CRITICAL_DEPTH > 0)) || iocost_die "unbalanced signal critical section"
	IOCOST_SIGNAL_CRITICAL_DEPTH=$((IOCOST_SIGNAL_CRITICAL_DEPTH - 1))
	if ((IOCOST_SIGNAL_CRITICAL_DEPTH == 0 && IOCOST_PENDING_SIGNAL_STATUS != 0)); then
		status=${IOCOST_PENDING_SIGNAL_STATUS}
		IOCOST_PENDING_SIGNAL_STATUS=0
		exit "${status}"
	fi
}

iocost_handle_signal() {
	local status=$1
	if ((IOCOST_SIGNAL_CRITICAL_DEPTH > 0)); then
		if ((IOCOST_PENDING_SIGNAL_STATUS == 0)); then
			IOCOST_PENDING_SIGNAL_STATUS=${status}
		fi
		return 0
	fi
	exit "${status}"
}

iocost_require_command() {
	local command_name=$1
	command -v "${command_name}" > /dev/null 2>&1 \
		|| iocost_die "required command is unavailable: ${command_name}"
}

iocost_file_value() {
	local path=$1
	[[ -r ${path} ]] || return 1
	tr -d '\n' < "${path}"
}

iocost_proc_starttime() {
	local pid=$1 remainder
	[[ -r /proc/${pid}/stat ]] || return 1
	remainder=$(sed 's/^[^)]*) //' "/proc/${pid}/stat" 2> /dev/null) || return 1
	set -- ${remainder}
	[[ $# -ge 20 ]] || return 1
	printf '%s\n' "${20}"
}

iocost_proc_state() {
	local pid=$1 remainder
	[[ -r /proc/${pid}/stat ]] || return 1
	remainder=$(sed 's/^[^)]*) //' "/proc/${pid}/stat" 2> /dev/null) || return 1
	printf '%s\n' "${remainder%% *}"
}

iocost_record_pid() {
	local pid=$1 label=$2 starttime pgid
	# Record the raw direct-child PID first. If /proc identity capture fails,
	# cleanup retains the dependency rather than losing knowledge of a spawn.
	IOCOST_TRACKED_PIDS+=("${pid}")
	IOCOST_PID_LABEL[${pid}]=${label}
	starttime=$(iocost_proc_starttime "${pid}") || {
		iocost_warn "cannot record ${label} pid ${pid}: missing /proc identity"
		return 1
	}
	IOCOST_PID_STARTTIME[${pid}]=${starttime}
	pgid=$(ps -o pgid= -p "${pid}" | tr -d '[:space:]') || {
		iocost_warn "cannot record ${label} pid ${pid}: process-group lookup failed"
		return 1
	}
	[[ ${pgid} =~ ^[0-9]+$ ]] || {
		iocost_warn "cannot record ${label} pid ${pid}: invalid process group"
		return 1
	}
	IOCOST_PID_PGID[${pid}]=${pgid}
}

iocost_forget_pid() {
	local pid=$1 item
	local -a kept=()
	for item in "${IOCOST_TRACKED_PIDS[@]}"; do
		[[ ${item} == "${pid}" ]] || kept+=("${item}")
	done
	IOCOST_TRACKED_PIDS=("${kept[@]}")
	unset 'IOCOST_PID_STARTTIME['"${pid}"']'
	unset 'IOCOST_PID_PGID['"${pid}"']'
	unset 'IOCOST_PID_LABEL['"${pid}"']'
}

iocost_pid_is_original() {
	local pid=$1 current
	[[ -n ${IOCOST_PID_STARTTIME[${pid}]:-} ]] || return 1
	current=$(iocost_proc_starttime "${pid}") || return 1
	[[ ${current} == "${IOCOST_PID_STARTTIME[${pid}]}" ]]
}

iocost_pid_is_running_original() {
	local pid=$1 state
	iocost_pid_is_original "${pid}" || return 1
	state=$(iocost_proc_state "${pid}") || return 1
	[[ ${state} != Z && ${state} != X ]]
}

iocost_pid_is_reapable() {
	local pid=$1 state
	if iocost_pid_is_original "${pid}"; then
		state=$(iocost_proc_state "${pid}") || return 1
		if [[ ${state} == Z || ${state} == X ]]; then
			return 0
		fi
		return 1
	fi
	# A just-spawned direct child may exit before /proc identity capture. Z/X is
	# safe here because this path never signals it; wait only asks Bash for the
	# tracked child's already-completed status. Live unidentified PIDs remain
	# protected from signalling.
	if state=$(iocost_proc_state "${pid}") && [[ ${state} == Z || ${state} == X ]]; then
		return 0
	fi
	! kill -0 "${pid}" 2> /dev/null
}

iocost_wait_pid() {
	local pid=$1 status
	local label=${IOCOST_PID_LABEL[${pid}]:-process}
	local deadline=$((SECONDS + IOCOST_WAIT_TIMEOUT_SECONDS))
	while iocost_pid_is_running_original "${pid}"; do
		if ((SECONDS >= deadline)); then
			iocost_warn "timed out waiting for tracked ${label} pid ${pid}"
			iocost_stop_pid "${pid}" \
				|| iocost_warn "tracked ${label} pid ${pid} remained live after timeout cleanup"
			return 124
		fi
		sleep 0.05
	done
	if ! iocost_pid_is_reapable "${pid}"; then
		iocost_warn "tracked ${label} pid ${pid} lost its registered identity before exit"
		return 1
	fi
	if wait "${pid}"; then
		status=0
	else
		status=$?
	fi
	iocost_forget_pid "${pid}"
	if ((status != 0)); then
		iocost_warn "${label} exited with status ${status}"
		return "${status}"
	fi
	return 0
}

iocost_stop_pid() {
	iocost_stop_pids "$1"
}

iocost_stop_all_pids() {
	iocost_stop_pids "${IOCOST_TRACKED_PIDS[@]}"
}

iocost_stop_pids() {
	local pid label pgid target signal deadline index remaining failed=0
	local -a pids=("$@")
	# Each phase shares one grace period, including when pressure prepared many
	# stopped issuers. Resume only after TERM so they can handle pending exit.
	for signal in TERM KILL; do
		deadline=$((SECONDS + IOCOST_STOP_TIMEOUT_SECONDS))
		for ((index = ${#pids[@]} - 1; index >= 0; index--)); do
			pid=${pids[index]}
			iocost_pid_is_running_original "${pid}" || continue
			label=${IOCOST_PID_LABEL[${pid}]:-process}
			pgid=${IOCOST_PID_PGID[${pid}]:-}
			target=${pid}
			[[ ${pgid} != "${pid}" ]] || target=-${pgid}
			if [[ ${signal} == KILL ]]; then
				iocost_warn "forcing tracked ${label} pid ${pid} to stop"
			fi
			kill "-${signal}" -- "${target}" 2> /dev/null || true
			if [[ ${signal} == TERM ]] && iocost_pid_is_original "${pid}"; then
				kill -CONT -- "${target}" 2> /dev/null || true
			fi
		done
		while ((SECONDS < deadline)); do
			remaining=0
			for pid in "${pids[@]}"; do
				if iocost_pid_is_running_original "${pid}"; then
					remaining=1
					break
				fi
			done
			((remaining != 0)) || break
			sleep 0.1
		done
	done
	for ((index = ${#pids[@]} - 1; index >= 0; index--)); do
		pid=${pids[index]}
		label=${IOCOST_PID_LABEL[${pid}]:-process}
		if iocost_pid_is_running_original "${pid}"; then
			iocost_warn "tracked ${label} pid ${pid} survived bounded TERM/KILL cleanup"
			failed=1
			continue
		fi
		if ! iocost_pid_is_reapable "${pid}"; then
			iocost_warn "tracked ${label} pid ${pid} cannot be proved exited; refusing unbounded wait"
			failed=1
			continue
		fi
		wait "${pid}" 2> /dev/null || true
		iocost_forget_pid "${pid}"
	done
	((failed == 0))
}

iocost_wait_for_file() {
	local path=$1 pid=$2 description=$3
	local deadline=$((SECONDS + IOCOST_WAIT_TIMEOUT_SECONDS))
	while ((SECONDS < deadline)); do
		if [[ -f ${path} ]]; then
			return 0
		fi
		if ! iocost_pid_is_running_original "${pid}"; then
			return 1
		fi
		sleep 0.05
	done
	iocost_warn "timed out waiting for ${description}: ${path}"
	return 1
}

iocost_wait_for_state() {
	local pid=$1 wanted=$2 timeout=$3 state
	local deadline=$((SECONDS + timeout))
	while ((SECONDS < deadline)); do
		state=$(iocost_proc_state "${pid}") || return 1
		[[ ${state} == "${wanted}" ]] && return 0
		[[ ${state} != Z && ${state} != X ]] || return 1
		sleep 0.01
	done
	return 1
}

iocost_check_required_mode() {
	if [[ ${TEST_IOCOST_REQUIRED+x} != x || ${TEST_IOCOST_REQUIRED} != "${IOCOST_REQUIRED_VALUE}" ]]; then
		iocost_die "TEST_IOCOST_REQUIRED must be set to exactly 1"
	fi
}

iocost_check_platform() {
	local system machine
	system=$(uname -s)
	machine=$(uname -m)
	[[ ${system} == Linux ]] || iocost_die "requires Linux, got ${system}"
	[[ ${machine} == x86_64 ]] || iocost_die "requires x86_64, got ${machine}"
	((EUID == 0)) || iocost_die "requires root"
	IOCOST_KERNEL_RELEASE=$(uname -r)
	[[ -s /sys/kernel/btf/vmlinux && -r /sys/kernel/btf/vmlinux ]] \
		|| iocost_die "live kernel BTF is unavailable: /sys/kernel/btf/vmlinux"
	[[ -r /proc/kallsyms ]] || iocost_die "live kallsyms is unavailable"
}

iocost_find_kernel_config() {
	local candidate
	if [[ -r /proc/config.gz ]]; then
		IOCOST_KERNEL_CONFIG=/proc/config.gz
		return 0
	fi
	for candidate in "/boot/config-${IOCOST_KERNEL_RELEASE}" \
		"/lib/modules/${IOCOST_KERNEL_RELEASE}/build/.config"; do
		if [[ -r ${candidate} ]]; then
			IOCOST_KERNEL_CONFIG=${candidate}
			return 0
		fi
	done
	return 1
}

iocost_config_has() {
	local expected=$1
	if [[ ${IOCOST_KERNEL_CONFIG} == /proc/config.gz ]]; then
		gzip -cd -- "${IOCOST_KERNEL_CONFIG}" | grep -Fx -- "${expected}" > /dev/null
	else
		grep -Fqx -- "${expected}" "${IOCOST_KERNEL_CONFIG}"
	fi
}

iocost_check_kernel_config() {
	iocost_find_kernel_config || iocost_die "running-kernel config is unavailable"
	[[ ${IOCOST_KERNEL_CONFIG} != /proc/config.gz ]] || iocost_require_command gzip
	local option
	for option in CONFIG_BLK_CGROUP CONFIG_BLK_CGROUP_IOCOST CONFIG_BPF \
		CONFIG_BPF_SYSCALL CONFIG_BPF_EVENTS CONFIG_KPROBES CONFIG_KPROBE_EVENTS \
		CONFIG_DEBUG_INFO_BTF; do
		iocost_config_has "${option}=y" \
			|| iocost_die "running kernel lacks ${option}=y (${IOCOST_KERNEL_CONFIG})"
	done
}

iocost_check_cgroup_delegation() {
	[[ $(findmnt -rn -T "${IOCOST_CGROUP_ROOT}" -o FSTYPE) == cgroup2 ]] \
		|| iocost_die "${IOCOST_CGROUP_ROOT} is not a cgroup v2 mount"
	[[ -r ${IOCOST_CGROUP_ROOT}/cgroup.controllers ]] \
		|| iocost_die "cgroup v2 controllers file is unavailable"
	grep -qw io "${IOCOST_CGROUP_ROOT}/cgroup.controllers" \
		|| iocost_die "cgroup v2 io controller is unavailable"
	grep -qw io "${IOCOST_CGROUP_ROOT}/cgroup.subtree_control" \
		|| iocost_die "cgroup v2 io controller is not delegated at the root"
	[[ -w ${IOCOST_CGROUP_ROOT}/cgroup.procs ]] \
		|| iocost_die "cgroup v2 hierarchy is not writable"
	[[ -r ${IOCOST_CGROUP_ROOT}/io.cost.model && -w ${IOCOST_CGROUP_ROOT}/io.cost.model ]] \
		|| iocost_die "io.cost.model is unavailable (CONFIG_BLK_CGROUP_IOCOST unsupported)"
	[[ -r ${IOCOST_CGROUP_ROOT}/io.cost.qos && -w ${IOCOST_CGROUP_ROOT}/io.cost.qos ]] \
		|| iocost_die "io.cost.qos is unavailable (CONFIG_BLK_CGROUP_IOCOST unsupported)"
}

iocost_check_commands() {
	local command_name
	for command_name in awk bash chmod curl dd env findmnt flock git grep losetup \
		mkdir mktemp mknod ps python3 readlink realpath rm rmdir sed setsid sha256sum \
		udevadm sleep stat tr truncate uname; do
		iocost_require_command "${command_name}"
	done
}

iocost_acquire_lock() {
	local lock_dir_metadata
	# Reuse is expected; the metadata check below is authoritative whether
	# mkdir creates the directory or finds an existing path.
	mkdir -m 0700 -- "${IOCOST_LOCK_DIR}" 2> /dev/null || :
	lock_dir_metadata=$(LC_ALL=C stat -c '%F:%u:%g:%a' -- "${IOCOST_LOCK_DIR}") \
		|| iocost_die "cannot inspect IOCOST lock directory: ${IOCOST_LOCK_DIR}"
	[[ ${lock_dir_metadata} == "directory:0:0:700" ]] \
		|| iocost_die "unsafe IOCOST lock directory metadata: ${lock_dir_metadata}"
	exec 9>> "${IOCOST_LOCK_PATH}"
	IOCOST_LOCK_OPEN=1
	flock -n 9 || iocost_die "another IOCOST qualification runner holds ${IOCOST_LOCK_PATH}"
}

iocost_expected_artifacts() {
	printf '%s\n' \
		"${IOCOST_DAEMON_REL}" \
		"${IOCOST_RUNTIME_TEST_REL}" \
		"${IOCOST_PRODUCTION_BPF_REL}" \
		"${IOCOST_TEST_BPF_REL}"
}

iocost_validate_manifest_shape() {
	local manifest=${IOCOST_ROOT_DIR}/${IOCOST_MANIFEST_REL}
	local hash path extra count=0
	declare -A seen=()
	[[ -f ${manifest} && ! -L ${manifest} ]] \
		|| iocost_die "missing regular OETest manifest: ${manifest}"
	while read -r hash path extra; do
		[[ -n ${hash} && -n ${path} && -z ${extra:-} ]] \
			|| iocost_die "malformed OETest manifest line"
		path=${path#\*}
		[[ ${hash} =~ ^[[:xdigit:]]{64}$ ]] || iocost_die "malformed SHA-256 digest for ${path}"
		case "${path}" in
		"${IOCOST_DAEMON_REL}" | "${IOCOST_RUNTIME_TEST_REL}" | \
			"${IOCOST_PRODUCTION_BPF_REL}" | "${IOCOST_TEST_BPF_REL}") ;;
		*) iocost_die "unexpected artifact in manifest: ${path}" ;;
		esac
		[[ -z ${seen[${path}]:-} ]] || iocost_die "duplicate artifact in manifest: ${path}"
		seen[${path}]=1
		count=$((count + 1))
	done < "${manifest}"
	((count == 4)) || iocost_die "manifest must contain exactly four artifacts, got ${count}"
	while read -r path; do
		[[ -n ${seen[${path}]:-} ]] || iocost_die "manifest omits required artifact: ${path}"
	done < <(iocost_expected_artifacts)
}

iocost_current_artifact_digests() {
	local path
	while read -r path; do
		sha256sum "${IOCOST_ROOT_DIR}/${path}"
	done < <(iocost_expected_artifacts)
}

iocost_assert_clean_source() {
	local phase=$1 untracked
	git -C "${IOCOST_ROOT_DIR}" diff --quiet --no-ext-diff --ignore-submodules=all -- \
		|| iocost_die "${phase}: tracked source has unstaged changes"
	git -C "${IOCOST_ROOT_DIR}" diff --cached --quiet --no-ext-diff --ignore-submodules=all -- \
		|| iocost_die "${phase}: source index has staged changes"
	untracked=$(git -C "${IOCOST_ROOT_DIR}" ls-files --others --exclude-standard) \
		|| iocost_die "${phase}: cannot enumerate untracked source"
	[[ -z ${untracked} ]] || iocost_die "${phase}: source tree has untracked files: ${untracked}"
}

iocost_verify_artifacts() {
	local phase=$1 current_head path
	iocost_assert_clean_source "${phase}"
	[[ -f ${IOCOST_ROOT_DIR}/${IOCOST_HEAD_REL} && ! -L ${IOCOST_ROOT_DIR}/${IOCOST_HEAD_REL} ]] \
		|| iocost_die "missing regular OETest HEAD manifest"
	IOCOST_APPROVED_HEAD=$(iocost_file_value "${IOCOST_ROOT_DIR}/${IOCOST_HEAD_REL}")
	[[ ${IOCOST_APPROVED_HEAD} =~ ^[0-9a-f]{40}$ ]] || iocost_die "invalid approved HEAD"
	current_head=$(git -C "${IOCOST_ROOT_DIR}" rev-parse HEAD)
	[[ ${current_head} == "${IOCOST_APPROVED_HEAD}" ]] \
		|| iocost_die "${phase}: source HEAD ${current_head} differs from approved ${IOCOST_APPROVED_HEAD}"
	iocost_validate_manifest_shape
	while read -r path; do
		[[ -f ${IOCOST_ROOT_DIR}/${path} && ! -L ${IOCOST_ROOT_DIR}/${path} ]] \
			|| iocost_die "${phase}: missing regular artifact ${path}"
	done < <(iocost_expected_artifacts)
	[[ -x ${IOCOST_ROOT_DIR}/${IOCOST_DAEMON_REL} ]] || iocost_die "OETest daemon is not executable"
	[[ -x ${IOCOST_ROOT_DIR}/${IOCOST_RUNTIME_TEST_REL} ]] || iocost_die "runtime harness is not executable"
	(
		cd "${IOCOST_ROOT_DIR}"
		sha256sum -c "${IOCOST_MANIFEST_REL}"
	) || iocost_die "${phase}: OETest artifact digest verification failed"
}

iocost_snapshot_artifacts() {
	iocost_verify_artifacts before
	IOCOST_APPROVED_HEAD_BEFORE=${IOCOST_APPROVED_HEAD}
	IOCOST_ARTIFACT_DIGESTS_BEFORE=$(iocost_current_artifact_digests)
}

iocost_verify_artifacts_unchanged() {
	local after
	iocost_verify_artifacts after
	[[ ${IOCOST_APPROVED_HEAD} == "${IOCOST_APPROVED_HEAD_BEFORE}" ]] \
		|| iocost_die "approved OETest HEAD changed during qualification"
	after=$(iocost_current_artifact_digests)
	[[ ${after} == "${IOCOST_ARTIFACT_DIGESTS_BEFORE}" ]] \
		|| iocost_die "OETest artifacts changed during qualification"
}

# Block devices and cost configuration.

# Loop device lifecycle.

readonly IOCOST_DEFAULT_LOOP_NAME=default

declare -a IOCOST_REGISTERED_LOOP_ORDER=()
declare -A IOCOST_REGISTERED_LOOP=()
declare -A IOCOST_REGISTERED_LOOP_MINOR=()
declare -A IOCOST_REGISTERED_LOOP_DEVICE=()
declare -A IOCOST_REGISTERED_LOOP_WORKLOAD_DEVICE=()
declare -A IOCOST_REGISTERED_LOOP_DEVICE_ID=()
declare -A IOCOST_REGISTERED_LOOP_MAJOR=()
declare -A IOCOST_REGISTERED_LOOP_FIRST_MINOR=()
declare -A IOCOST_REGISTERED_LOOP_BACKING=()
declare -A IOCOST_REGISTERED_LOOP_ADDED=()
declare -A IOCOST_REGISTERED_LOOP_ATTACHED=()
declare -A IOCOST_REGISTERED_LOOP_COST_SAVED=()
declare -A IOCOST_REGISTERED_LOOP_COST_CHANGED=()
declare -A IOCOST_REGISTERED_LOOP_ORIGINAL_MODEL=()
declare -A IOCOST_REGISTERED_LOOP_ORIGINAL_QOS=()
declare -A IOCOST_REGISTERED_LOOP_NEXT_SEEK=()

iocost_validate_loop_name() {
	[[ $1 =~ ^[a-z][a-z0-9-]*$ ]] || iocost_die "invalid private loop name: $1"
}

iocost_create_loop_backing() {
	local backing=$1 private_root resolved
	[[ -n ${IOCOST_TMP_DIR} && -d ${IOCOST_TMP_DIR} && ! -L ${IOCOST_TMP_DIR} ]] \
		|| iocost_die "private temporary directory is unavailable"
	private_root=$(realpath -- "${IOCOST_TMP_DIR}") \
		|| iocost_die "cannot resolve private temporary directory"
	[[ ! -e ${backing} ]] || iocost_die "private loop backing already exists: ${backing}"
	[[ ${backing} == "${IOCOST_TMP_DIR}/"* ]] \
		|| iocost_die "loop backing must be inside the private temporary directory"
	truncate -s "${IOCOST_BACKING_BYTES}" "${backing}"
	chmod 0600 "${backing}"
	[[ -f ${backing} && ! -L ${backing} ]] \
		|| iocost_die "loop backing file is not a regular file"
	resolved=$(realpath -- "${backing}") || iocost_die "cannot resolve loop backing file"
	[[ ${resolved} == "${private_root}/"* ]] \
		|| iocost_die "resolved loop backing escaped the private temporary directory"
}

iocost_create_private_tmp() {
	local base=/var/tmp create_failed=0
	umask 077
	iocost_signal_critical_enter
	if IOCOST_TMP_DIR=$(iocost_signal_critical_command \
		mktemp -d "${base}/huatuo-iocost.XXXXXX"); then
		IOCOST_TMP_CREATED=1
	else
		create_failed=1
	fi
	iocost_signal_critical_leave
	((create_failed == 0)) || iocost_die "cannot create private temporary directory"
	chmod 0700 "${IOCOST_TMP_DIR}"
	[[ -d ${IOCOST_TMP_DIR} && ! -L ${IOCOST_TMP_DIR} ]] \
		|| iocost_die "private temporary directory is not a real directory"
	[[ $(stat -c '%u:%a' "${IOCOST_TMP_DIR}") == "0:700" ]] \
		|| iocost_die "private temporary directory must be root-owned mode 0700"
	IOCOST_BACKING_FILE=${IOCOST_TMP_DIR}/loop.backing
	iocost_create_loop_backing "${IOCOST_BACKING_FILE}"
}

iocost_sync_default_loop_globals() {
	[[ ${IOCOST_REGISTERED_LOOP[${IOCOST_DEFAULT_LOOP_NAME}]:-0} == 1 ]] || return 0
	IOCOST_BACKING_FILE=${IOCOST_REGISTERED_LOOP_BACKING[${IOCOST_DEFAULT_LOOP_NAME}]:-}
	IOCOST_LOOP_MINOR=${IOCOST_REGISTERED_LOOP_MINOR[${IOCOST_DEFAULT_LOOP_NAME}]:-}
	IOCOST_DEVICE_ID=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${IOCOST_DEFAULT_LOOP_NAME}]:-}
	IOCOST_DEVICE_MAJOR=${IOCOST_REGISTERED_LOOP_MAJOR[${IOCOST_DEFAULT_LOOP_NAME}]:-}
	IOCOST_DEVICE_FIRST_MINOR=${IOCOST_REGISTERED_LOOP_FIRST_MINOR[${IOCOST_DEFAULT_LOOP_NAME}]:-}
}

iocost_add_registered_loop_minor() {
	local name=$1 backing=$2 requested_minor=${3:-}
	local minor add_failed=0 device_id workload_device
	iocost_validate_loop_name "${name}"
	[[ -c /dev/loop-control ]] || iocost_die "/dev/loop-control is unavailable"
	[[ -f ${backing} && ! -L ${backing} && ${backing} == "${IOCOST_TMP_DIR}/"* ]] \
		|| iocost_die "registered loop backing is not a private regular file: ${backing}"
	[[ ${IOCOST_REGISTERED_LOOP_ADDED[${name}]:-0} == 0 ]] \
		|| iocost_die "registered loop is already added: ${name}"
	[[ -z ${requested_minor} || ${requested_minor} =~ ^[0-9]+$ ]] \
		|| iocost_die "invalid requested loop minor: ${requested_minor}"
	iocost_signal_critical_enter
	if ! minor=$(
		iocost_signal_critical_command python3 - "${requested_minor}" << 'PY'
import errno
import fcntl
import glob
import os
import re
import sys

LOOP_CTL_ADD = 0x4C80
requested = sys.argv[1]
if requested:
    candidates = [int(requested, 10)]
else:
    used = []
    for path in glob.glob("/sys/block/loop*"):
        match = re.fullmatch(r"loop([0-9]+)", os.path.basename(path))
        if match:
            used.append(int(match.group(1)))
    candidate = max(used + [255]) + 1
    candidates = range(candidate, candidate + 4096)
with open("/dev/loop-control", "rb+", buffering=0) as control:
    for number in candidates:
        try:
            result = fcntl.ioctl(control.fileno(), LOOP_CTL_ADD, number)
        except OSError as error:
            if error.errno in (errno.EEXIST, errno.EBUSY):
                continue
            raise
        if result != number:
            raise RuntimeError(f"LOOP_CTL_ADD returned {result}, expected {number}")
        print(number)
        break
    else:
        if requested:
            raise RuntimeError(f"requested loop minor {requested} is unavailable")
        raise RuntimeError("no unused loop minor could be added")
PY
	); then
		add_failed=1
	elif [[ ${minor} =~ ^[0-9]+$ ]]; then
		if [[ ${IOCOST_REGISTERED_LOOP[${name}]:-0} == 0 ]]; then
			IOCOST_REGISTERED_LOOP_ORDER+=("${name}")
			IOCOST_REGISTERED_LOOP[${name}]=1
		fi
		IOCOST_REGISTERED_LOOP_MINOR[${name}]=${minor}
		IOCOST_REGISTERED_LOOP_DEVICE[${name}]=/dev/loop${minor}
		workload_device=${IOCOST_TMP_DIR}/loop-${name}-${minor}
		IOCOST_REGISTERED_LOOP_BACKING[${name}]=${backing}
		IOCOST_REGISTERED_LOOP_ADDED[${name}]=1
		IOCOST_REGISTERED_LOOP_ATTACHED[${name}]=0
		IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]=""
		IOCOST_REGISTERED_LOOP_MAJOR[${name}]=""
		IOCOST_REGISTERED_LOOP_FIRST_MINOR[${name}]=""
		IOCOST_REGISTERED_LOOP_COST_SAVED[${name}]=0
		IOCOST_REGISTERED_LOOP_COST_CHANGED[${name}]=0
		IOCOST_REGISTERED_LOOP_ORIGINAL_MODEL[${name}]=""
		IOCOST_REGISTERED_LOOP_ORIGINAL_QOS[${name}]=""
		IOCOST_REGISTERED_LOOP_NEXT_SEEK[${name}]=0
		[[ ${name} != "${IOCOST_DEFAULT_LOOP_NAME}" ]] || iocost_sync_default_loop_globals
	else
		add_failed=1
	fi
	iocost_signal_critical_leave
	((add_failed == 0)) || iocost_die "LOOP_CTL_ADD failed or returned an invalid minor"
	local deadline=$((SECONDS + 10))
	while ((SECONDS < deadline)); do
		[[ -b ${IOCOST_REGISTERED_LOOP_DEVICE[${name}]} && -d /sys/block/loop${minor} ]] && break
		sleep 0.05
	done
	[[ -b ${IOCOST_REGISTERED_LOOP_DEVICE[${name}]} && -d /sys/block/loop${minor} ]] \
		|| iocost_die "new loop minor ${minor} did not appear in devtmpfs/sysfs"
	device_id=$(iocost_device_id_from_node "${IOCOST_REGISTERED_LOOP_DEVICE[${name}]}") \
		|| iocost_die "cannot register new loop major:minor"
	IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]=${device_id}
	IOCOST_REGISTERED_LOOP_MAJOR[${name}]=${device_id%%:*}
	IOCOST_REGISTERED_LOOP_FIRST_MINOR[${name}]=${device_id##*:}
	if [[ ! -e ${workload_device} ]]; then
		mknod -m 0600 -- "${workload_device}" b "${device_id%%:*}" "${device_id##*:}" \
			|| iocost_die "cannot create private workload node for loop ${minor}"
	fi
	IOCOST_REGISTERED_LOOP_WORKLOAD_DEVICE[${name}]=${workload_device}
	[[ -b ${workload_device} && ! -L ${workload_device} &&
		$(stat -c '%u:%a' "${workload_device}") == "0:600" &&
		$(iocost_device_id_from_node "${workload_device}") == "${device_id}" ]] \
		|| iocost_die "private workload node does not match loop ${minor}"
	[[ $(iocost_file_value "/sys/block/loop${minor}/dev") == "${device_id}" ]] \
		|| iocost_die "new loop node and sysfs identity disagree"
	[[ ${name} != "${IOCOST_DEFAULT_LOOP_NAME}" ]] || iocost_sync_default_loop_globals
}

iocost_add_new_loop_minor() {
	iocost_add_registered_loop_minor "${IOCOST_DEFAULT_LOOP_NAME}" "${IOCOST_BACKING_FILE}"
}

iocost_device_id_from_node() {
	local node=$1 major_hex minor_hex
	major_hex=$(stat -c '%t' "${node}") || return 1
	minor_hex=$(stat -c '%T' "${node}") || return 1
	printf '%d:%d\n' "$((16#${major_hex}))" "$((16#${minor_hex}))"
}

iocost_assert_registered_loop_topology_safe() {
	local name=$1
	local minor=${IOCOST_REGISTERED_LOOP_MINOR[${name}]:-}
	local device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]:-}
	local workload_device=${IOCOST_REGISTERED_LOOP_WORKLOAD_DEVICE[${name}]:-}
	local sysfs=/sys/block/loop${minor}
	local root_device mounted swap_path swap_device entry
	[[ ${IOCOST_REGISTERED_LOOP_ADDED[${name}]:-0} == 1 && -n ${minor} && -n ${device_id} ]] \
		|| iocost_die "registered loop lacks a complete live identity: ${name}"
	[[ $(readlink -f "/sys/dev/block/${device_id}") == "$(readlink -f "${sysfs}")" ]] \
		|| iocost_die "loop node and sysfs identity disagree"
	[[ ${workload_device} == "${IOCOST_TMP_DIR}/loop-${name}-${minor}" &&
		-b ${workload_device} && ! -L ${workload_device} &&
		$(iocost_device_id_from_node "${workload_device}") == "${device_id}" ]] \
		|| iocost_die "private workload node and loop identity disagree"
	root_device=$(findmnt -rn -T / -o MAJ:MIN)
	[[ ${device_id} != "${root_device}" ]] || iocost_die "fixture loop is the root device"
	while read -r mounted; do
		[[ ${mounted} != "${device_id}" ]] || iocost_die "fixture loop is mounted"
	done < <(findmnt -rn -o MAJ:MIN)
	while read -r swap_path _; do
		[[ ${swap_path} == Filename ]] && continue
		[[ -b ${swap_path} ]] || continue
		swap_device=$(iocost_device_id_from_node "${swap_path}") \
			|| iocost_die "cannot resolve swap device ${swap_path}"
		[[ ${swap_device} != "${device_id}" ]] || iocost_die "fixture loop is active swap"
	done < /proc/swaps
	for entry in "${sysfs}"/holders/* "${sysfs}"/slaves/*; do
		[[ ! -e ${entry} ]] || iocost_die "fixture loop participates in holder/slave topology: ${entry}"
	done
}

iocost_attach_registered_loop() {
	local name=$1
	local device=${IOCOST_REGISTERED_LOOP_DEVICE[${name}]:-}
	local workload_device=${IOCOST_REGISTERED_LOOP_WORKLOAD_DEVICE[${name}]:-}
	local backing=${IOCOST_REGISTERED_LOOP_BACKING[${name}]:-}
	local device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]:-}
	local minor=${IOCOST_REGISTERED_LOOP_MINOR[${name}]:-}
	local reported matched backing_echo attached_device attach_failed=0
	[[ ${IOCOST_REGISTERED_LOOP_ADDED[${name}]:-0} == 1 &&
		${IOCOST_REGISTERED_LOOP_ATTACHED[${name}]:-0} == 0 &&
		-n ${device} && -b ${workload_device} && -n ${backing} && -n ${device_id} ]] \
		|| iocost_die "loop identity was not registered before attach: ${name}"
	iocost_signal_critical_enter
	if (iocost_signal_critical_command losetup --direct-io=on \
		"${workload_device}" "${backing}"); then
		IOCOST_REGISTERED_LOOP_ATTACHED[${name}]=1
		[[ ${name} != "${IOCOST_DEFAULT_LOOP_NAME}" ]] || iocost_sync_default_loop_globals
	else
		attach_failed=1
		# LOOP_SET_FD can succeed before LOOP_SET_DIRECT_IO fails.  Preserve
		# that partial attachment in the registry so EXIT cleanup detaches it.
		if [[ -e /sys/block/loop${minor}/loop/backing_file ]] \
			&& backing_echo=$(iocost_file_value "/sys/block/loop${minor}/loop/backing_file") \
			&& [[ $(realpath -- "${backing_echo}") == "$(realpath -- "${backing}")" ]]; then
			IOCOST_REGISTERED_LOOP_ATTACHED[${name}]=1
			[[ ${name} != "${IOCOST_DEFAULT_LOOP_NAME}" ]] || iocost_sync_default_loop_globals
		fi
	fi
	iocost_signal_critical_leave
	((attach_failed == 0)) || iocost_die "failed to attach private loop backing file"
	attached_device=$(iocost_device_id_from_node "${device}") \
		|| iocost_die "cannot re-read attached loop major:minor"
	[[ ${attached_device} == "${device_id}" ]] \
		|| iocost_die "attached loop identity changed from ${device_id} to ${attached_device}"
	[[ $(iocost_file_value "/sys/block/loop${minor}/loop/dio") == 1 ]] \
		|| iocost_die "loop direct I/O was not enabled"
	reported=$(losetup --noheadings --output BACK-FILE "${device}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
	[[ $(realpath -- "${reported}") == "$(realpath -- "${backing}")" ]] \
		|| iocost_die "losetup backing-file echo does not match the private backing file"
	matched=$(losetup --noheadings --output NAME -j "${backing}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
	[[ ${matched} == "${device}" ]] \
		|| iocost_die "backing-file reverse lookup did not return exactly ${device}"
	backing_echo=$(iocost_file_value "/sys/block/loop${minor}/loop/backing_file")
	[[ $(realpath -- "${backing_echo}") == "$(realpath -- "${backing}")" ]] \
		|| iocost_die "sysfs backing-file echo does not match the private backing file"
	udevadm settle --timeout="${IOCOST_LOOP_CLEANUP_TIMEOUT_SECONDS}" \
		|| iocost_die "udev did not finish probing private loop ${device}"
	# systemd-udevd watches the canonical /dev node and retriggers blkid after
	# writers close it. Attach and workloads use the private node instead, then
	# this target-scoped event synchronizes the initial kernel change event.
	udevadm trigger \
		--action=change \
		--settle \
		"/sys/block/loop${minor}" \
		|| iocost_die "udev did not finish the private loop change event ${device}"
	udevadm info \
		--wait-for-initialization="${IOCOST_LOOP_CLEANUP_TIMEOUT_SECONDS}" \
		--name="${device}" > /dev/null \
		|| iocost_die "udev did not initialize private loop ${device}"
	iocost_assert_registered_loop_topology_safe "${name}"
}

iocost_attach_loop() {
	iocost_attach_registered_loop "${IOCOST_DEFAULT_LOOP_NAME}"
}

iocost_remove_loop_minor_exact() {
	local minor=$1 expected_device=$2
	(
		iocost_signal_critical_command python3 - "${minor}" "${expected_device}" \
			"${IOCOST_LOOP_CLEANUP_TIMEOUT_SECONDS}" << 'PY'
import errno
import fcntl
import os
import stat
import sys
import time

LOOP_CTL_REMOVE = 0x4C81
minor = int(sys.argv[1], 10)
expected_device = sys.argv[2]
timeout = int(sys.argv[3], 10)
deadline = time.monotonic() + timeout
node = f"/dev/loop{minor}"
sysfs_device = f"/sys/block/loop{minor}/dev"


def verify_identity():
    with open(sysfs_device, encoding="ascii") as source:
        actual_sysfs = source.read().strip()
    info = os.stat(node, follow_symlinks=False)
    if not stat.S_ISBLK(info.st_mode):
        raise RuntimeError(f"{node} is not a block device")
    actual_node = f"{os.major(info.st_rdev)}:{os.minor(info.st_rdev)}"
    if actual_node != actual_sysfs:
        raise RuntimeError(
            f"loop identity mismatch: node={actual_node}, sysfs={actual_sysfs}"
        )
    if expected_device and actual_sysfs != expected_device:
        raise RuntimeError(
            f"loop identity changed: expected={expected_device}, actual={actual_sysfs}"
        )


with open("/dev/loop-control", "rb+", buffering=0) as control:
    while True:
        verify_identity()
        try:
            result = fcntl.ioctl(control.fileno(), LOOP_CTL_REMOVE, minor)
        except OSError as error:
            if error.errno != errno.EBUSY:
                raise
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError(
                    f"LOOP_CTL_REMOVE minor {minor} remained EBUSY for {timeout}s"
                ) from error
            time.sleep(min(0.05, remaining))
            continue
        if result not in (0, minor):
            raise RuntimeError(f"LOOP_CTL_REMOVE returned {result}")
        break
PY
	)
}

iocost_registered_loop_mapping_is_absent() {
	local name=$1
	local device=${IOCOST_REGISTERED_LOOP_DEVICE[${name}]:-}
	local minor=${IOCOST_REGISTERED_LOOP_MINOR[${name}]:-}
	[[ -n ${device} && -n ${minor} ]] || return 1
	! losetup "${device}" > /dev/null 2>&1 \
		&& [[ ! -e /sys/block/loop${minor}/loop/backing_file ]]
}

iocost_wait_registered_loop_detached() {
	local name=$1
	local deadline=$((SECONDS + IOCOST_LOOP_CLEANUP_TIMEOUT_SECONDS))
	while ((SECONDS < deadline)); do
		iocost_registered_loop_mapping_is_absent "${name}" && return 0
		sleep 0.05
	done
	iocost_registered_loop_mapping_is_absent "${name}"
}

iocost_registered_loop_removal_is_visible() {
	local name=$1
	local minor=${IOCOST_REGISTERED_LOOP_MINOR[${name}]:-}
	local device=${IOCOST_REGISTERED_LOOP_DEVICE[${name}]:-}
	local device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]:-}
	[[ -n ${minor} && -n ${device} ]] || return 1
	[[ ! -e /sys/block/loop${minor} ]] || return 1
	[[ ! -e ${device} ]] || return 1
	if [[ -n ${device_id} ]]; then
		[[ ! -e /sys/dev/block/${device_id} ]] || return 1
		iocost_device_row_is_absent_for "${device_id}" \
			"${IOCOST_CGROUP_ROOT}/io.cost.model" || return 1
		iocost_device_row_is_absent_for "${device_id}" \
			"${IOCOST_CGROUP_ROOT}/io.cost.qos" || return 1
	fi
}

iocost_wait_registered_loop_removed() {
	local name=$1
	local deadline=$((SECONDS + IOCOST_LOOP_CLEANUP_TIMEOUT_SECONDS))
	while ((SECONDS < deadline)); do
		iocost_registered_loop_removal_is_visible "${name}" && return 0
		sleep 0.05
	done
	iocost_registered_loop_removal_is_visible "${name}"
}

iocost_try_detach_registered_loop() {
	local name=$1
	local device=${IOCOST_REGISTERED_LOOP_DEVICE[${name}]:-}
	local detach_failed=0
	[[ ${IOCOST_REGISTERED_LOOP_ATTACHED[${name}]:-0} == 1 ]] || return 0
	(iocost_assert_registered_loop_topology_safe "${name}") || return 1
	iocost_signal_critical_enter
	if (iocost_signal_critical_command losetup -d "${device}"); then
		IOCOST_REGISTERED_LOOP_ATTACHED[${name}]=0
		[[ ${name} != "${IOCOST_DEFAULT_LOOP_NAME}" ]] || iocost_sync_default_loop_globals
	else
		detach_failed=1
	fi
	iocost_signal_critical_leave
	((detach_failed == 0)) || return 1
	iocost_wait_registered_loop_detached "${name}"
}

iocost_clear_removed_loop_registration() {
	local name=$1
	IOCOST_REGISTERED_LOOP_DEVICE[${name}]=""
	IOCOST_REGISTERED_LOOP_WORKLOAD_DEVICE[${name}]=""
	IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]=""
	IOCOST_REGISTERED_LOOP_MAJOR[${name}]=""
	IOCOST_REGISTERED_LOOP_FIRST_MINOR[${name}]=""
	IOCOST_REGISTERED_LOOP_COST_SAVED[${name}]=0
	IOCOST_REGISTERED_LOOP_ORIGINAL_MODEL[${name}]=""
	IOCOST_REGISTERED_LOOP_ORIGINAL_QOS[${name}]=""
	[[ ${name} != "${IOCOST_DEFAULT_LOOP_NAME}" ]] || iocost_sync_default_loop_globals
}

iocost_try_remove_registered_loop() {
	local name=$1
	local minor=${IOCOST_REGISTERED_LOOP_MINOR[${name}]:-}
	local device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]:-}
	local remove_failed=0
	[[ ${IOCOST_REGISTERED_LOOP_ADDED[${name}]:-0} == 1 ]] || return 1
	iocost_try_detach_registered_loop "${name}" || return 1
	iocost_signal_critical_enter
	if iocost_remove_loop_minor_exact "${minor}" "${device_id}"; then
		# Every registered minor was created by this runner. Successful
		# LOOP_CTL_REMOVE therefore restores its pre-test absence and removes
		# any io.cost rows without first disabling IOCOST; this is required to
		# exercise ioc_rqos_exit at the actual disk lifetime boundary.
		IOCOST_REGISTERED_LOOP_ADDED[${name}]=0
		IOCOST_REGISTERED_LOOP_COST_CHANGED[${name}]=0
		[[ ${name} != "${IOCOST_DEFAULT_LOOP_NAME}" ]] || iocost_sync_default_loop_globals
	else
		remove_failed=1
	fi
	iocost_signal_critical_leave
	((remove_failed == 0)) || return 1
	iocost_wait_registered_loop_removed "${name}" || return 1
	iocost_clear_removed_loop_registration "${name}"
}

iocost_remove_registered_loop() {
	local name=$1
	local minor=${IOCOST_REGISTERED_LOOP_MINOR[${name}]:-}
	[[ ${IOCOST_REGISTERED_LOOP_ADDED[${name}]:-0} == 1 ]] \
		|| iocost_die "registered loop is not live: ${name}"
	iocost_try_remove_registered_loop "${name}" \
		|| iocost_die "failed to detach/remove registered loop ${name} (minor ${minor})"
}

iocost_cleanup_registered_loops() {
	local index name minor failed=0
	for ((index = ${#IOCOST_REGISTERED_LOOP_ORDER[@]} - 1; index >= 0; index--)); do
		name=${IOCOST_REGISTERED_LOOP_ORDER[index]}
		if [[ ${IOCOST_REGISTERED_LOOP_ADDED[${name}]:-0} == 0 &&
			-n ${IOCOST_REGISTERED_LOOP_DEVICE[${name}]:-} ]]; then
			if iocost_wait_registered_loop_removed "${name}"; then
				iocost_clear_removed_loop_registration "${name}"
			else
				iocost_warn "registered loop ${name} removal did not become visible"
				failed=1
			fi
			continue
		fi
		if [[ ${IOCOST_REGISTERED_LOOP_COST_CHANGED[${name}]:-0} == 1 ]]; then
			iocost_warn "preserving loop ${name}: io.cost restoration is incomplete"
			failed=1
			continue
		fi
		if [[ ${IOCOST_REGISTERED_LOOP_ADDED[${name}]:-0} == 1 ]]; then
			minor=${IOCOST_REGISTERED_LOOP_MINOR[${name}]:-}
			if ! iocost_try_remove_registered_loop "${name}"; then
				iocost_warn "failed to detach/remove registered loop ${name} (minor ${minor})"
				failed=1
			fi
		fi
	done
	iocost_sync_default_loop_globals
	return "${failed}"
}

iocost_registered_loops_are_clean() {
	local name
	for name in "${IOCOST_REGISTERED_LOOP_ORDER[@]}"; do
		[[ ${IOCOST_REGISTERED_LOOP_ADDED[${name}]:-0} == 0 &&
			${IOCOST_REGISTERED_LOOP_ATTACHED[${name}]:-0} == 0 &&
			${IOCOST_REGISTERED_LOOP_COST_CHANGED[${name}]:-0} == 0 &&
			-z ${IOCOST_REGISTERED_LOOP_DEVICE[${name}]:-} &&
			-z ${IOCOST_REGISTERED_LOOP_WORKLOAD_DEVICE[${name}]:-} ]] || return 1
	done
}

iocost_device_line_for() {
	local device_id=$1 file=$2 output
	[[ -r ${file} ]] || return 2
	if output=$(awk -v device="${device_id}" '
		$1 == device {
			if (NF < 2)
				invalid = 1
			rows++
			line = $0
		}
		END {
			if (invalid || rows > 1)
				print "INVALID:"
			else if (rows == 0)
				print "ABSENT:"
			else
				print "FOUND:" line
		}
	' "${file}"); then
		:
	else
		# awk read/runtime errors must never be confused with a missing row.
		return 2
	fi
	case ${output} in
	ABSENT:)
		return 1
		;;
	INVALID:)
		return 2
		;;
	FOUND:*)
		printf '%s\n' "${output#FOUND:}"
		;;
	*)
		return 2
		;;
	esac
}

iocost_device_row_is_absent_for() {
	local device_id=$1 file=$2 status
	if iocost_device_line_for "${device_id}" "${file}" > /dev/null 2>&1; then
		return 1
	else
		status=$?
	fi
	((status == 1))
}

# Cost configuration and restoration.

iocost_save_registered_cost_state() {
	local name=$1
	local device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]:-}
	local status line
	[[ ${IOCOST_REGISTERED_LOOP_COST_SAVED[${name}]:-0} == 0 ]] || return 0
	[[ ${IOCOST_REGISTERED_LOOP_ATTACHED[${name}]:-0} == 1 && -n ${device_id} ]] \
		|| iocost_die "cannot save io.cost state for inactive loop ${name}"
	if line=$(iocost_device_line_for "${device_id}" "${IOCOST_CGROUP_ROOT}/io.cost.model"); then
		IOCOST_REGISTERED_LOOP_ORIGINAL_MODEL[${name}]=${line}
	else
		status=$?
		((status == 1)) || iocost_die "cannot read unique io.cost.model state for ${device_id}"
		IOCOST_REGISTERED_LOOP_ORIGINAL_MODEL[${name}]=""
	fi
	if line=$(iocost_device_line_for "${device_id}" "${IOCOST_CGROUP_ROOT}/io.cost.qos"); then
		IOCOST_REGISTERED_LOOP_ORIGINAL_QOS[${name}]=${line}
	else
		status=$?
		((status == 1)) || iocost_die "cannot read unique io.cost.qos state for ${device_id}"
		IOCOST_REGISTERED_LOOP_ORIGINAL_QOS[${name}]=""
	fi
	IOCOST_REGISTERED_LOOP_COST_SAVED[${name}]=1
}

iocost_assert_device_cost_tokens() {
	local device_id=$1 file=$2
	shift 2
	local line token
	line=$(iocost_device_line_for "${device_id}" "${file}") \
		|| iocost_die "${file} has no ${device_id} row after write"
	for token in "$@"; do
		grep -Eq "(^|[[:space:]])${token}([[:space:]]|$)" <<< "${line}" \
			|| iocost_die "${file} did not echo expected token ${token}: ${line}"
	done
}

iocost_assert_registered_cost_tokens() {
	local name=$1 file=$2
	local device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]:-}
	shift 2
	iocost_assert_device_cost_tokens "${device_id}" "${file}" "$@"
}

iocost_set_registered_cost_rates() {
	local name=$1 rbps=$2 riops=$3 wbps=$4 wiops=$5
	local device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]:-}
	[[ ${rbps} =~ ^[1-9][0-9]*$ && ${riops} =~ ^[1-9][0-9]*$ &&
		${wbps} =~ ^[1-9][0-9]*$ && ${wiops} =~ ^[1-9][0-9]*$ ]] \
		|| iocost_die "IOCOST model rates must be positive integers"
	iocost_save_registered_cost_state "${name}"
	# The first write mutates global kernel state. Keep this flag set until the
	# complete saved model and QoS state has been read back successfully.
	IOCOST_REGISTERED_LOOP_COST_CHANGED[${name}]=1
	printf '%s ctrl=user model=linear rbps=%s rseqiops=%s rrandiops=%s wbps=%s wseqiops=%s wrandiops=%s\n' \
		"${device_id}" "${rbps}" "${riops}" "${riops}" "${wbps}" "${wiops}" "${wiops}" \
		> "${IOCOST_CGROUP_ROOT}/io.cost.model"
	printf '%s enable=1 ctrl=user rpct=0.00 rlat=0 wpct=0.00 wlat=0 min=100.00 max=100.00\n' \
		"${device_id}" > "${IOCOST_CGROUP_ROOT}/io.cost.qos"
	iocost_assert_registered_cost_tokens "${name}" "${IOCOST_CGROUP_ROOT}/io.cost.model" \
		'ctrl=user' 'model=linear' "rbps=${rbps}" "rseqiops=${riops}" \
		"rrandiops=${riops}" "wbps=${wbps}" "wseqiops=${wiops}" "wrandiops=${wiops}"
	iocost_assert_registered_cost_tokens "${name}" "${IOCOST_CGROUP_ROOT}/io.cost.qos" \
		'enable=1' 'ctrl=user' 'rpct=0\.00' 'rlat=0' 'wpct=0\.00' 'wlat=0' \
		'min=100\.00' 'max=100\.00'
}

iocost_set_cost_rates() {
	iocost_set_registered_cost_rates "${IOCOST_DEFAULT_LOOP_NAME}" "$@"
}

iocost_set_registered_cost_profile() {
	local name=$1 profile=$2
	case ${profile} in
	high)
		iocost_set_registered_cost_rates "${name}" 1073741824 262144 1073741824 262144
		;;
	medium)
		iocost_set_registered_cost_rates "${name}" 65536 16 65536 16
		;;
	long)
		iocost_set_registered_cost_rates "${name}" 4096 1 4096 1
		;;
	cross)
		iocost_set_registered_cost_rates "${name}" 512 1 512 1
		;;
	diagnostic-long)
		iocost_set_registered_cost_rates "${name}" 262144 64 262144 64
		;;
	*) iocost_die "unknown IOCOST cost profile: ${profile}" ;;
	esac
}

iocost_set_cost_profile() {
	iocost_set_registered_cost_profile "${IOCOST_DEFAULT_LOOP_NAME}" "$1"
}

iocost_write_saved_cost_line() {
	local file=$1 line=$2 token ctrl="" output=""
	local -a tokens=()
	read -r -a tokens <<< "${line}"
	((${#tokens[@]} >= 2)) || return 1
	output=${tokens[0]}
	for token in "${tokens[@]:1}"; do
		if [[ ${token} == ctrl=* ]]; then
			ctrl=${token}
		else
			output+=" ${token}"
		fi
	done
	[[ -n ${ctrl} ]] || return 1
	printf '%s %s\n' "${output}" "${ctrl}" > "${file}"
}

iocost_cost_line_has_tokens() {
	local line=$1 token
	shift
	for token in "$@"; do
		grep -Eq "(^|[[:space:]])${token}([[:space:]]|$)" <<< "${line}" || return 1
	done
}

iocost_restore_registered_cost_state() {
	local name=$1
	local device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]:-}
	local original_model=${IOCOST_REGISTERED_LOOP_ORIGINAL_MODEL[${name}]:-}
	local original_qos=${IOCOST_REGISTERED_LOOP_ORIGINAL_QOS[${name}]:-}
	local failed=0 line status
	[[ ${IOCOST_REGISTERED_LOOP_COST_CHANGED[${name}]:-0} == 1 ]] || return 0
	if [[ -n ${original_model} ]]; then
		iocost_write_saved_cost_line "${IOCOST_CGROUP_ROOT}/io.cost.model" \
			"${original_model}" || failed=1
	else
		printf '%s ctrl=auto\n' "${device_id}" > "${IOCOST_CGROUP_ROOT}/io.cost.model" || failed=1
	fi
	if [[ -n ${original_qos} ]]; then
		iocost_write_saved_cost_line "${IOCOST_CGROUP_ROOT}/io.cost.qos" \
			"${original_qos}" || failed=1
	else
		printf '%s enable=0 ctrl=auto\n' "${device_id}" > "${IOCOST_CGROUP_ROOT}/io.cost.qos" || failed=1
	fi
	if [[ -n ${original_model} ]]; then
		[[ $(iocost_device_line_for "${device_id}" "${IOCOST_CGROUP_ROOT}/io.cost.model" 2> /dev/null) == "${original_model}" ]] || failed=1
	else
		if line=$(iocost_device_line_for "${device_id}" "${IOCOST_CGROUP_ROOT}/io.cost.model" 2> /dev/null); then
			iocost_cost_line_has_tokens "${line}" 'ctrl=auto' || failed=1
		else
			status=$?
			((status == 1)) || failed=1
		fi
	fi
	if [[ -n ${original_qos} ]]; then
		[[ $(iocost_device_line_for "${device_id}" "${IOCOST_CGROUP_ROOT}/io.cost.qos" 2> /dev/null) == "${original_qos}" ]] || failed=1
	else
		if line=$(iocost_device_line_for "${device_id}" "${IOCOST_CGROUP_ROOT}/io.cost.qos" 2> /dev/null); then
			iocost_cost_line_has_tokens "${line}" 'enable=0' 'ctrl=auto' || failed=1
		else
			status=$?
			((status == 1)) || failed=1
		fi
	fi
	if ((failed != 0)); then
		iocost_warn "failed to restore io.cost state for ${name} (${device_id})"
		return 1
	fi
	IOCOST_REGISTERED_LOOP_COST_CHANGED[${name}]=0
}

iocost_restore_all_cost_states() {
	local index name failed=0
	for ((index = ${#IOCOST_REGISTERED_LOOP_ORDER[@]} - 1; index >= 0; index--)); do
		name=${IOCOST_REGISTERED_LOOP_ORDER[index]}
		iocost_restore_registered_cost_state "${name}" || failed=1
	done
	return "${failed}"
}

# Cgroups and IO workloads.

# Cgroup lifecycle.

declare -a IOCOST_REGISTERED_CGROUP_ORDER=()
declare -A IOCOST_REGISTERED_CGROUP_PATH=()
declare -A IOCOST_REGISTERED_CGROUP_ACTIVE=()

iocost_validate_named_leaf() {
	local name=$1
	[[ ${name} =~ ^[a-z][a-z0-9-]*$ ]] \
		|| iocost_die "invalid private cgroup leaf name: ${name}"
	[[ ${name} != leaf && ${name} != parking ]] \
		|| iocost_die "private cgroup leaf name is reserved: ${name}"
}

iocost_cgroup_is_empty() {
	local cgroup=$1
	[[ -r ${cgroup}/cgroup.procs ]] || return 1
	! grep -Eq '^[0-9]+$' "${cgroup}/cgroup.procs"
}

iocost_create_cgroups() {
	local name="huatuo-iocost-${BASHPID}-${IOCOST_LOOP_MINOR}"
	local create_failed=0
	IOCOST_CGROUP_PARENT=${IOCOST_CGROUP_ROOT}/${name}
	IOCOST_CGROUP_LEAF=${IOCOST_CGROUP_PARENT}/leaf
	IOCOST_CGROUP_PARKING=${IOCOST_CGROUP_PARENT}/parking
	[[ ! -e ${IOCOST_CGROUP_PARENT} ]] || iocost_die "fixture cgroup already exists"
	iocost_signal_critical_enter
	if (iocost_signal_critical_command mkdir "${IOCOST_CGROUP_PARENT}"); then
		IOCOST_CGROUP_CREATED=1
	else
		create_failed=1
	fi
	iocost_signal_critical_leave
	((create_failed == 0)) || iocost_die "cannot create private cgroup parent"
	iocost_cgroup_is_empty "${IOCOST_CGROUP_PARENT}" \
		|| iocost_die "new fixture cgroup is not empty or cannot be inspected"
	printf '+io\n' > "${IOCOST_CGROUP_PARENT}/cgroup.subtree_control"
	grep -qw io "${IOCOST_CGROUP_PARENT}/cgroup.subtree_control" \
		|| iocost_die "failed to enable io in the private cgroup parent"
	mkdir "${IOCOST_CGROUP_LEAF}" "${IOCOST_CGROUP_PARKING}"
	[[ -w ${IOCOST_CGROUP_LEAF}/cgroup.procs && -r ${IOCOST_CGROUP_LEAF}/io.stat ]] \
		|| iocost_die "private leaf lacks delegated io controller files"
	[[ -w ${IOCOST_CGROUP_PARKING}/cgroup.procs ]] \
		|| iocost_die "private parking cgroup is unusable"
	printf 'default 100\n' > "${IOCOST_CGROUP_LEAF}/io.weight"
	printf 'default 100\n' > "${IOCOST_CGROUP_PARKING}/io.weight"
}

iocost_create_named_leaf() {
	local name=$1 path create_failed=0
	iocost_validate_named_leaf "${name}"
	((IOCOST_CGROUP_CREATED == 1)) \
		|| iocost_die "cannot create a named leaf before the private cgroup parent"
	path=${IOCOST_CGROUP_PARENT}/${name}
	[[ ${IOCOST_REGISTERED_CGROUP_ACTIVE[${name}]:-0} == 0 && ! -e ${path} ]] \
		|| iocost_die "private cgroup leaf already exists: ${name}"

	iocost_signal_critical_enter
	if (iocost_signal_critical_command mkdir "${path}"); then
		if [[ -z ${IOCOST_REGISTERED_CGROUP_PATH[${name}]:-} ]]; then
			IOCOST_REGISTERED_CGROUP_ORDER+=("${name}")
		fi
		IOCOST_REGISTERED_CGROUP_PATH[${name}]=${path}
		IOCOST_REGISTERED_CGROUP_ACTIVE[${name}]=1
	else
		create_failed=1
	fi
	iocost_signal_critical_leave
	((create_failed == 0)) || iocost_die "cannot create private cgroup leaf: ${name}"

	[[ -w ${path}/cgroup.procs && -r ${path}/io.stat ]] \
		|| iocost_die "private cgroup leaf is unusable: ${name}"
	printf 'default 100\n' > "${path}/io.weight"
	IOCOST_LAST_CGROUP=${path}
}

iocost_remove_named_leaf() {
	local name=$1 path remove_failed=0
	iocost_validate_named_leaf "${name}"
	path=${IOCOST_REGISTERED_CGROUP_PATH[${name}]:-}
	[[ ${IOCOST_REGISTERED_CGROUP_ACTIVE[${name}]:-0} == 1 && -n ${path} ]] \
		|| iocost_die "private cgroup leaf is not active: ${name}"
	iocost_cgroup_is_empty "${path}" \
		|| iocost_die "refusing to remove non-empty private cgroup leaf: ${name}"

	iocost_signal_critical_enter
	if (iocost_signal_critical_command rmdir "${path}"); then
		IOCOST_REGISTERED_CGROUP_ACTIVE[${name}]=0
	else
		remove_failed=1
	fi
	iocost_signal_critical_leave
	((remove_failed == 0)) || iocost_die "cannot remove private cgroup leaf: ${name}"
	[[ ! -e ${path} ]] || iocost_die "removed private cgroup leaf remains visible: ${name}"
}

iocost_cgroup_stat_value() {
	local cgroup=$1 field=$2 output
	[[ -r ${cgroup}/cgroup.stat ]] || return 1
	output=$(awk -v key="${field}" '
		$1 == key {
			if (NF != 2 || $2 !~ /^[0-9]+$/)
				invalid = 1
			rows++
			value = $2
		}
		END {
			if (invalid || rows != 1)
				exit 1
			print value
		}
	' "${cgroup}/cgroup.stat") || return 1
	[[ ${output} =~ ^[0-9]+$ ]] || return 1
	printf '%s\n' "${output}"
}

iocost_wait_nr_dying_descendants() {
	local cgroup=$1 expected=$2 value
	[[ ${expected} =~ ^[0-9]+$ ]] || iocost_die "invalid nr_dying_descendants baseline"
	local deadline=$((SECONDS + IOCOST_WAIT_TIMEOUT_SECONDS))
	while ((SECONDS < deadline)); do
		value=$(iocost_cgroup_stat_value "${cgroup}" nr_dying_descendants) \
			|| iocost_die "cannot read nr_dying_descendants from ${cgroup}"
		[[ ${value} != "${expected}" ]] || return 0
		sleep 0.05
	done
	value=$(iocost_cgroup_stat_value "${cgroup}" nr_dying_descendants) || return 1
	[[ ${value} == "${expected}" ]]
}

iocost_recreate_leaf() {
	[[ ! -e ${IOCOST_CGROUP_LEAF} ]] || iocost_die "cannot recreate an existing leaf"
	mkdir "${IOCOST_CGROUP_LEAF}"
	printf 'default 100\n' > "${IOCOST_CGROUP_LEAF}/io.weight"
	[[ -w ${IOCOST_CGROUP_LEAF}/cgroup.procs ]] || iocost_die "recreated leaf is unusable"
}

iocost_cleanup_named_leaves() {
	local index name path failed=0
	for ((index = ${#IOCOST_REGISTERED_CGROUP_ORDER[@]} - 1; index >= 0; index--)); do
		name=${IOCOST_REGISTERED_CGROUP_ORDER[index]}
		[[ ${IOCOST_REGISTERED_CGROUP_ACTIVE[${name}]:-0} == 1 ]] || continue
		path=${IOCOST_REGISTERED_CGROUP_PATH[${name}]:-}
		if [[ ! -e ${path} ]]; then
			IOCOST_REGISTERED_CGROUP_ACTIVE[${name}]=0
			continue
		fi
		if [[ ! -d ${path} || ! -r ${path}/cgroup.procs ]]; then
			iocost_warn "cannot inspect registered private cgroup leaf ${name}"
			failed=1
			continue
		fi
		if ! iocost_cgroup_is_empty "${path}"; then
			iocost_warn "refusing to remove non-empty registered private cgroup leaf ${name}"
			failed=1
			continue
		fi
		if rmdir "${path}"; then
			IOCOST_REGISTERED_CGROUP_ACTIVE[${name}]=0
		else
			failed=1
		fi
	done
	return "${failed}"
}

iocost_remove_cgroups() {
	((IOCOST_CGROUP_CREATED == 1)) || return 0
	local failed=0 cgroup pid
	iocost_cleanup_named_leaves || failed=1
	for cgroup in "${IOCOST_CGROUP_LEAF}" "${IOCOST_CGROUP_PARKING}"; do
		[[ -d ${cgroup} ]] || continue
		if [[ ! -r ${cgroup}/cgroup.procs ]]; then
			iocost_warn "cannot inspect processes in ${cgroup}"
			failed=1
			continue
		fi
		if ! iocost_cgroup_is_empty "${cgroup}"; then
			iocost_warn "refusing to move or kill untracked processes left in ${cgroup}"
			while read -r pid; do
				iocost_warn "remaining cgroup pid: ${pid}"
			done < "${cgroup}/cgroup.procs"
			failed=1
			continue
		fi
		rmdir "${cgroup}" || failed=1
	done
	if [[ -d ${IOCOST_CGROUP_PARENT} ]]; then
		if [[ ! -r ${IOCOST_CGROUP_PARENT}/cgroup.procs ]]; then
			iocost_warn "cannot inspect processes in private cgroup parent"
			failed=1
		elif ! iocost_cgroup_is_empty "${IOCOST_CGROUP_PARENT}"; then
			iocost_warn "private cgroup parent unexpectedly contains processes"
			failed=1
		else
			rmdir "${IOCOST_CGROUP_PARENT}" || failed=1
		fi
	fi
	((failed == 0)) || return 1
	IOCOST_CGROUP_CREATED=0
}

# IO workloads and observation.

declare -a IOCOST_WRITE_BATCH_PIDS=()
IOCOST_WRITE_BATCH_OPEN=0
IOCOST_WRITE_BATCH_RELEASED=0
IOCOST_LAST_RELEASE_NS=""

iocost_io_stat_value_for() {
	local name=$1 cgroup=$2 field=$3
	local device_id=${IOCOST_REGISTERED_LOOP_DEVICE_ID[${name}]:-}
	awk -v device="${device_id}" -v key="${field}" '
		$1 == device {
			for (i = 2; i <= NF; i++) {
				split($i, pair, "=")
				if (pair[1] == key) {
					print pair[2]
					exit
				}
			}
		}
		END { if (NR == 0) print 0 }
	' "${cgroup}/io.stat" | awk 'NF { value=$1 } END { print value + 0 }'
}

iocost_io_stat_value() {
	iocost_io_stat_value_for "${IOCOST_DEFAULT_LOOP_NAME}" "$@"
}

iocost_prepare_registered_write() {
	local name=$1 cgroup=$2 blocks=$3 block_size=${4:-${IOCOST_IO_BLOCK_SIZE}}
	local device=${IOCOST_REGISTERED_LOOP_WORKLOAD_DEVICE[${name}]:-}
	local capacity seek next_seek log_file pid record_failed=0 private_parent resolved_cgroup
	[[ ${IOCOST_REGISTERED_LOOP_ATTACHED[${name}]:-0} == 1 && -b ${device} ]] \
		|| iocost_die "cannot write through inactive registered loop ${name}"
	private_parent=$(realpath -- "${IOCOST_CGROUP_PARENT}") \
		|| iocost_die "cannot resolve private cgroup parent"
	resolved_cgroup=$(realpath -- "${cgroup}") \
		|| iocost_die "cannot resolve workload cgroup: ${cgroup}"
	[[ ${resolved_cgroup} == "${private_parent}/"* && -w ${cgroup}/cgroup.procs ]] \
		|| iocost_die "workload cgroup is outside the private subtree: ${cgroup}"
	[[ ${blocks} =~ ^[1-9][0-9]*$ && ${block_size} =~ ^[1-9][0-9]*$ ]] \
		|| iocost_die "direct write size must be positive"
	capacity=$((IOCOST_BACKING_BYTES / block_size))
	((blocks < capacity)) || iocost_die "direct write exceeds the private backing device"
	next_seek=${IOCOST_REGISTERED_LOOP_NEXT_SEEK[${name}]:-0}
	seek=$((next_seek % (capacity - blocks)))
	IOCOST_REGISTERED_LOOP_NEXT_SEEK[${name}]=$(((seek + blocks + 31) % capacity))
	log_file=${IOCOST_TMP_DIR}/write-${name}-${BASHPID}-${RANDOM}.log
	iocost_signal_critical_enter
	setsid bash -c '
		kill -STOP "$$"
		exec dd if=/dev/zero of="$1" bs="$2" count="$3" seek="$4" \
			oflag=direct conv=notrunc status=none
	' iocost-write "${device}" "${block_size}" "${blocks}" "${seek}" \
		> "${log_file}" 2>&1 &
	pid=$!
	iocost_record_pid "${pid}" "direct-write-${name}" || record_failed=1
	IOCOST_LAST_PID=${pid}
	iocost_signal_critical_leave
	((record_failed == 0)) || iocost_die "cannot register spawned direct-write pid ${pid}"
	if ! iocost_wait_for_state "${pid}" T 5; then
		iocost_warn "direct-write failed to stop before cgroup placement"
		[[ ! -s ${log_file} ]] || sed 's/^/[direct-write] /' "${log_file}" >&2
		iocost_die "cannot place direct write in private cgroup"
	fi
	printf '%s\n' "${pid}" > "${cgroup}/cgroup.procs"
	[[ $(iocost_file_value "/proc/${pid}/cgroup") == *"${cgroup#${IOCOST_CGROUP_ROOT}}"* ]] \
		|| iocost_die "direct-write pid ${pid} was not placed in ${cgroup}"
}

iocost_release_prepared_write() {
	local pid=$1
	iocost_wait_for_state "${pid}" T 5 \
		|| iocost_die "prepared direct-write pid ${pid} is not stopped at the release barrier"
	IOCOST_LAST_RELEASE_NS=$(
		python3 - "${pid}" << 'PY'
import os
import signal
import sys
import time

pid = int(sys.argv[1], 10)
start_ns = time.monotonic_ns()
os.kill(pid, signal.SIGCONT)
print(start_ns)
PY
	) || iocost_die "cannot release prepared direct-write pid ${pid}"
	[[ ${IOCOST_LAST_RELEASE_NS} =~ ^[0-9]+$ ]] \
		|| iocost_die "prepared direct-write release timestamp is invalid"
}

iocost_start_registered_write() {
	iocost_prepare_registered_write "$@"
	iocost_release_prepared_write "${IOCOST_LAST_PID}"
}

iocost_start_leaf_write() {
	iocost_start_registered_write "${IOCOST_DEFAULT_LOOP_NAME}" \
		"${IOCOST_CGROUP_LEAF}" "$@"
}

iocost_run_registered_write() {
	local name=$1 cgroup=$2 blocks=$3 block_size=${4:-${IOCOST_IO_BLOCK_SIZE}}
	local pid log_glob log_file
	iocost_start_registered_write "${name}" "${cgroup}" "${blocks}" "${block_size}"
	pid=${IOCOST_LAST_PID}
	if ! iocost_wait_pid "${pid}"; then
		log_glob=${IOCOST_TMP_DIR}/write-*.log
		for log_file in ${log_glob}; do
			[[ -f ${log_file} ]] && sed 's/^/[direct-write] /' "${log_file}" >&2
		done
		iocost_die "direct write failed"
	fi
}

iocost_run_leaf_write() {
	iocost_run_registered_write "${IOCOST_DEFAULT_LOOP_NAME}" \
		"${IOCOST_CGROUP_LEAF}" "$@"
}

iocost_run_registered_discard() {
	local name=$1 cgroup=$2
	local device=${IOCOST_REGISTERED_LOOP_WORKLOAD_DEVICE[${name}]:-}
	local private_parent resolved_cgroup log_file pid record_failed=0
	[[ ${IOCOST_REGISTERED_LOOP_ATTACHED[${name}]:-0} == 1 && -b ${device} ]] \
		|| iocost_die "cannot discard through inactive registered loop ${name}"
	private_parent=$(realpath -- "${IOCOST_CGROUP_PARENT}") \
		|| iocost_die "cannot resolve private cgroup parent"
	resolved_cgroup=$(realpath -- "${cgroup}") \
		|| iocost_die "cannot resolve discard cgroup: ${cgroup}"
	[[ ${resolved_cgroup} == "${private_parent}/"* && -w ${cgroup}/cgroup.procs ]] \
		|| iocost_die "discard cgroup is outside the private subtree: ${cgroup}"

	log_file=${IOCOST_TMP_DIR}/discard-${name}-${BASHPID}-${RANDOM}.log
	iocost_signal_critical_enter
	setsid bash -c '
		kill -STOP "$$"
		exec "$@"
	' iocost-discard \
		python3 -c '
import fcntl
import os
import struct
import sys

BLKDISCARD = 0x1277
with os.fdopen(os.open(sys.argv[1], os.O_RDWR | os.O_CLOEXEC), "rb+", buffering=0) as device:
    fcntl.ioctl(device.fileno(), BLKDISCARD, struct.pack("QQ", 0, 4096))
' "${device}" > "${log_file}" 2>&1 &
	pid=$!
	iocost_record_pid "${pid}" "direct-discard-${name}" || record_failed=1
	IOCOST_LAST_PID=${pid}
	iocost_signal_critical_leave
	((record_failed == 0)) || iocost_die "cannot register spawned direct-discard pid ${pid}"
	if ! iocost_wait_for_state "${pid}" T 5; then
		iocost_warn "direct-discard failed to stop before cgroup placement"
		[[ ! -s ${log_file} ]] || sed 's/^/[direct-discard] /' "${log_file}" >&2
		iocost_die "cannot place direct discard in private cgroup"
	fi
	printf '%s\n' "${pid}" > "${cgroup}/cgroup.procs"
	[[ $(iocost_file_value "/proc/${pid}/cgroup") == *"${cgroup#${IOCOST_CGROUP_ROOT}}"* ]] \
		|| iocost_die "direct-discard pid ${pid} was not placed in ${cgroup}"
	iocost_release_prepared_write "${pid}"
	if ! iocost_wait_pid "${pid}"; then
		[[ ! -s ${log_file} ]] || sed 's/^/[direct-discard] /' "${log_file}" >&2
		iocost_die "direct discard failed"
	fi
}

iocost_begin_write_batch() {
	((IOCOST_WRITE_BATCH_OPEN == 0 && ${#IOCOST_WRITE_BATCH_PIDS[@]} == 0)) \
		|| iocost_die "a prepared direct-write batch is already active"
	IOCOST_WRITE_BATCH_OPEN=1
	IOCOST_WRITE_BATCH_RELEASED=0
}

iocost_prepare_batch_write() {
	((IOCOST_WRITE_BATCH_OPEN == 1 && IOCOST_WRITE_BATCH_RELEASED == 0)) \
		|| iocost_die "direct-write batch is not accepting prepared issuers"
	iocost_prepare_registered_write "$@"
	IOCOST_WRITE_BATCH_PIDS+=("${IOCOST_LAST_PID}")
}

iocost_release_write_batch() {
	local pid
	((IOCOST_WRITE_BATCH_OPEN == 1 && IOCOST_WRITE_BATCH_RELEASED == 0 && \
	${#IOCOST_WRITE_BATCH_PIDS[@]} > 0)) \
		|| iocost_die "direct-write batch is not ready for release"
	# Verify the complete cohort before releasing any issuer. This makes the
	# prepared STOP state a real barrier instead of a staggered start loop.
	for pid in "${IOCOST_WRITE_BATCH_PIDS[@]}"; do
		iocost_wait_for_state "${pid}" T 5 \
			|| iocost_die "prepared direct-write pid ${pid} left the release barrier"
	done
	for pid in "${IOCOST_WRITE_BATCH_PIDS[@]}"; do
		kill -CONT "${pid}" || iocost_die "cannot release direct-write pid ${pid}"
	done
	IOCOST_WRITE_BATCH_RELEASED=1
}

iocost_wait_write_batch_blocked() {
	local pid
	((IOCOST_WRITE_BATCH_OPEN == 1 && IOCOST_WRITE_BATCH_RELEASED == 1)) \
		|| iocost_die "direct-write batch has not been released"
	for pid in "${IOCOST_WRITE_BATCH_PIDS[@]}"; do
		iocost_wait_for_blocked_write "${pid}"
	done
}

iocost_wait_write_batch() {
	local pid failed=0
	((IOCOST_WRITE_BATCH_OPEN == 1 && IOCOST_WRITE_BATCH_RELEASED == 1)) \
		|| iocost_die "direct-write batch has not been released"
	for pid in "${IOCOST_WRITE_BATCH_PIDS[@]}"; do
		iocost_wait_pid "${pid}" || failed=1
	done
	IOCOST_WRITE_BATCH_PIDS=()
	IOCOST_WRITE_BATCH_OPEN=0
	IOCOST_WRITE_BATCH_RELEASED=0
	((failed == 0)) || iocost_die "one or more direct-write batch issuers failed"
}

iocost_wait_for_blocked_write() {
	local pid=$1
	if ! iocost_wait_for_state "${pid}" D 10; then
		iocost_die "direct-write pid ${pid} did not enter uninterruptible IOCOST wait"
	fi
}

iocost_monotonic_ns() {
	python3 - << 'PY'
import time
print(time.monotonic_ns())
PY
}

# SCSI device lifecycle.

IOCOST_SCSI_DEBUG_LOADED=0
IOCOST_SCSI_DEBUG_BOUND=0
IOCOST_SCSI_DEBUG_TRANSITIONING=0
IOCOST_SCSI_BLOCK_NAME=""
IOCOST_SCSI_DEVICE=""
IOCOST_SCSI_WORKLOAD_DEVICE=""
IOCOST_SCSI_DEVICE_ID=""
IOCOST_SCSI_PREVIOUS_DEVICE_ID=""
IOCOST_SCSI_ADDRESS=""
IOCOST_SCSI_DEVICE_PATH=""
IOCOST_SCSI_NEXT_SEEK_BLOCK=0
declare -a IOCOST_SCSI_DEVICE_IDS=()

iocost_scsi_debug_block_names() {
	local sysfs model
	for sysfs in /sys/class/block/*; do
		[[ -e ${sysfs} && ! -e ${sysfs}/partition && -r ${sysfs}/device/model ]] \
			|| continue
		model=$(sed 's/^[[:space:]]*//;s/[[:space:]]*$//' "${sysfs}/device/model") \
			|| return 1
		[[ ${model} == scsi_debug ]] || continue
		printf '%s\n' "${sysfs##*/}"
	done
}

iocost_wait_scsi_debug_block_count() {
	local expected=$1 deadline=$((SECONDS + IOCOST_LOOP_CLEANUP_TIMEOUT_SECONDS))
	local output
	local -a names=()
	[[ ${expected} == 0 || ${expected} == 1 ]] || return 1
	while :; do
		output=$(iocost_scsi_debug_block_names) || return 1
		names=()
		[[ -z ${output} ]] || mapfile -t names <<< "${output}"
		if ((${#names[@]} == expected)); then
			if ((expected == 1)) && [[ ! -b /dev/${names[0]} ]]; then
				:
			elif ((expected == 0)) && [[ -n ${IOCOST_SCSI_BLOCK_NAME} &&
				-b /dev/${IOCOST_SCSI_BLOCK_NAME} ]]; then
				:
			else
				return 0
			fi
		fi
		((SECONDS < deadline)) || return 1
		sleep 0.05
	done
}

iocost_record_scsi_debug_device_id() {
	local device_id=$1 known
	for known in "${IOCOST_SCSI_DEVICE_IDS[@]}"; do
		[[ ${known} != "${device_id}" ]] || return 0
	done
	IOCOST_SCSI_DEVICE_IDS+=("${device_id}")
}

iocost_register_scsi_debug_disk() {
	local -a names=()
	local output name sysfs device device_id node_id device_path address workload_device
	output=$(iocost_scsi_debug_block_names) \
		|| iocost_die "cannot enumerate scsi_debug block devices"
	[[ -z ${output} ]] || mapfile -t names <<< "${output}"
	((${#names[@]} == 1)) \
		|| iocost_die "scsi_debug must expose exactly one whole-disk block device"
	name=${names[0]}
	sysfs=$(readlink -f "/sys/class/block/${name}") \
		|| iocost_die "cannot resolve scsi_debug block sysfs path"
	device=/dev/${name}
	[[ -b ${device} ]] || iocost_die "scsi_debug block node is unavailable: ${device}"
	device_id=$(iocost_file_value "${sysfs}/dev") \
		|| iocost_die "cannot read scsi_debug major:minor"
	node_id=$(iocost_device_id_from_node "${device}") \
		|| iocost_die "cannot read scsi_debug block-node identity"
	[[ ${node_id} == "${device_id}" ]] \
		|| iocost_die "scsi_debug block node and sysfs identity disagree"
	device_path=$(readlink -f "${sysfs}/device") \
		|| iocost_die "cannot resolve scsi_debug SCSI device"
	address=${device_path##*/}
	[[ ${address} =~ ^[0-9]+:[0-9]+:[0-9]+:[0-9]+$ ]] \
		|| iocost_die "invalid scsi_debug SCSI address: ${address}"
	[[ -L /sys/bus/scsi/drivers/sd/${address} &&
		$(readlink -f "/sys/bus/scsi/drivers/sd/${address}") == "${device_path}" ]] \
		|| iocost_die "scsi_debug disk is not bound to the sd driver"
	if [[ -n ${IOCOST_SCSI_ADDRESS} ]]; then
		[[ ${address} == "${IOCOST_SCSI_ADDRESS}" &&
			${device_path} == "${IOCOST_SCSI_DEVICE_PATH}" ]] \
			|| iocost_die "scsi_debug rebind changed the underlying SCSI device"
		IOCOST_SCSI_PREVIOUS_DEVICE_ID=${IOCOST_SCSI_DEVICE_ID}
	fi
	IOCOST_SCSI_BLOCK_NAME=${name}
	IOCOST_SCSI_DEVICE=${device}
	IOCOST_SCSI_DEVICE_ID=${device_id}
	IOCOST_SCSI_ADDRESS=${address}
	IOCOST_SCSI_DEVICE_PATH=${device_path}
	workload_device=${IOCOST_TMP_DIR}/scsi-${device_id//:/-}
	if [[ ! -e ${workload_device} ]]; then
		mknod -m 0600 -- "${workload_device}" b "${device_id%%:*}" "${device_id##*:}" \
			|| iocost_die "cannot create private scsi_debug workload node"
	fi
	IOCOST_SCSI_WORKLOAD_DEVICE=${workload_device}
	[[ -b ${workload_device} && ! -L ${workload_device} &&
		$(stat -c '%u:%a' "${workload_device}") == "0:600" &&
		$(iocost_device_id_from_node "${workload_device}") == "${device_id}" ]] \
		|| iocost_die "private scsi_debug workload node does not match the disk"
	udevadm info \
		--wait-for-initialization="${IOCOST_LOOP_CLEANUP_TIMEOUT_SECONDS}" \
		--name="${device}" > /dev/null \
		|| iocost_die "udev did not initialize the scsi_debug disk"
	IOCOST_SCSI_DEBUG_BOUND=1
	iocost_record_scsi_debug_device_id "${device_id}"
	iocost_assert_scsi_debug_topology_safe
	IOCOST_SCSI_DEBUG_TRANSITIONING=0
}

iocost_assert_scsi_debug_block_population() {
	local output
	local -a names=()
	output=$(iocost_scsi_debug_block_names) \
		|| iocost_die "cannot enumerate the owned scsi_debug block population"
	[[ -z ${output} ]] || mapfile -t names <<< "${output}"
	if ((IOCOST_SCSI_DEBUG_BOUND == 1)); then
		((${#names[@]} == 1)) \
			&& [[ ${names[0]} == "${IOCOST_SCSI_BLOCK_NAME}" ]] \
			|| iocost_die "scsi_debug block population changed during qualification"
	elif ((IOCOST_SCSI_DEBUG_TRANSITIONING == 1)); then
		((${#names[@]} <= 1)) \
			|| iocost_die "scsi_debug registration found multiple block devices"
	else
		((${#names[@]} == 0)) \
			|| iocost_die "an unowned scsi_debug block device appeared"
	fi
}

iocost_assert_scsi_debug_topology_safe() {
	local sysfs root_device mounted swap_path swap_device entry
	[[ ${IOCOST_SCSI_DEBUG_LOADED} == 1 && ${IOCOST_SCSI_DEBUG_BOUND} == 1 &&
		-b ${IOCOST_SCSI_DEVICE} && -b ${IOCOST_SCSI_WORKLOAD_DEVICE} &&
		-n ${IOCOST_SCSI_DEVICE_ID} ]] \
		|| iocost_die "scsi_debug fixture lacks a complete live identity"
	iocost_assert_scsi_debug_block_population
	sysfs=/sys/class/block/${IOCOST_SCSI_BLOCK_NAME}
	[[ $(readlink -f "/sys/dev/block/${IOCOST_SCSI_DEVICE_ID}") == $(readlink -f "${sysfs}") ]] \
		|| iocost_die "scsi_debug node and sysfs identity disagree"
	[[ $(readlink -f "${sysfs}/device") == "${IOCOST_SCSI_DEVICE_PATH}" ]] \
		|| iocost_die "scsi_debug SCSI device identity changed"
	[[ ${IOCOST_SCSI_WORKLOAD_DEVICE} == "${IOCOST_TMP_DIR}/scsi-${IOCOST_SCSI_DEVICE_ID//:/-}" &&
		! -L ${IOCOST_SCSI_WORKLOAD_DEVICE} &&
		$(iocost_device_id_from_node "${IOCOST_SCSI_WORKLOAD_DEVICE}") == "${IOCOST_SCSI_DEVICE_ID}" ]] \
		|| iocost_die "private scsi_debug workload node identity changed"
	root_device=$(findmnt -rn -T / -o MAJ:MIN)
	[[ ${IOCOST_SCSI_DEVICE_ID} != "${root_device}" ]] \
		|| iocost_die "scsi_debug fixture is the root device"
	while read -r mounted; do
		[[ ${mounted} != "${IOCOST_SCSI_DEVICE_ID}" ]] \
			|| iocost_die "scsi_debug fixture is mounted"
	done < <(findmnt -rn -o MAJ:MIN)
	while read -r swap_path _; do
		[[ ${swap_path} == Filename ]] && continue
		[[ -b ${swap_path} ]] || continue
		swap_device=$(iocost_device_id_from_node "${swap_path}") \
			|| iocost_die "cannot resolve swap device ${swap_path}"
		[[ ${swap_device} != "${IOCOST_SCSI_DEVICE_ID}" ]] \
			|| iocost_die "scsi_debug fixture is active swap"
	done < /proc/swaps
	for entry in "${sysfs}"/holders/* "${sysfs}"/slaves/*; do
		[[ ! -e ${entry} ]] \
			|| iocost_die "scsi_debug fixture participates in block topology: ${entry}"
	done
}

iocost_create_scsi_debug_device() {
	local load_failed=0
	iocost_require_command modprobe
	iocost_config_has "CONFIG_SCSI_DEBUG=m" \
		|| iocost_die "retained-queue qualification requires CONFIG_SCSI_DEBUG=m"
	[[ ! -e /sys/module/scsi_debug ]] \
		|| iocost_die "refusing to reuse a pre-existing scsi_debug module"
	iocost_wait_scsi_debug_block_count 0 \
		|| iocost_die "pre-existing scsi_debug block device is present"
	iocost_signal_critical_enter
	if (iocost_signal_critical_command modprobe --first-time scsi_debug add_host=1 \
		dev_size_mb=64 max_luns=1 num_tgts=1 sector_size=512); then
		IOCOST_SCSI_DEBUG_LOADED=1
		IOCOST_SCSI_DEBUG_TRANSITIONING=1
		iocost_wait_scsi_debug_block_count 1 \
			|| iocost_die "scsi_debug did not expose exactly one block device"
		iocost_register_scsi_debug_disk
	else
		load_failed=1
	fi
	iocost_signal_critical_leave
	((load_failed == 0)) || iocost_die "cannot load the isolated scsi_debug module"
}

iocost_unbind_scsi_debug_disk() {
	local unbind_failed=0
	iocost_assert_scsi_debug_topology_safe
	iocost_signal_critical_enter
	if (iocost_signal_critical_command bash -c '
		printf "%s\n" "$1" > /sys/bus/scsi/drivers/sd/unbind
	' iocost-scsi-unbind "${IOCOST_SCSI_ADDRESS}"); then
		IOCOST_SCSI_DEBUG_TRANSITIONING=1
		IOCOST_SCSI_DEBUG_BOUND=0
		iocost_wait_scsi_debug_block_count 0 \
			|| iocost_die "scsi_debug block device remained after sd unbind"
		IOCOST_SCSI_DEBUG_TRANSITIONING=0
	else
		unbind_failed=1
	fi
	iocost_signal_critical_leave
	((unbind_failed == 0)) || iocost_die "cannot unbind the owned scsi_debug disk"
	[[ -d ${IOCOST_SCSI_DEVICE_PATH} ]] \
		|| iocost_die "sd unbind removed the underlying scsi_debug SCSI device"
}

iocost_rebind_scsi_debug_disk() {
	local previous_address=${IOCOST_SCSI_ADDRESS} bind_failed=0
	[[ ${IOCOST_SCSI_DEBUG_LOADED} == 1 && ${IOCOST_SCSI_DEBUG_BOUND} == 0 &&
		-d ${IOCOST_SCSI_DEVICE_PATH} ]] \
		|| iocost_die "scsi_debug fixture is not ready for sd rebind"
	iocost_signal_critical_enter
	if (iocost_signal_critical_command bash -c '
		printf "%s\n" "$1" > /sys/bus/scsi/drivers/sd/bind
	' iocost-scsi-bind "${previous_address}"); then
		IOCOST_SCSI_DEBUG_TRANSITIONING=1
		iocost_wait_scsi_debug_block_count 1 \
			|| iocost_die "scsi_debug block device did not return after sd rebind"
		iocost_register_scsi_debug_disk
	else
		bind_failed=1
	fi
	iocost_signal_critical_leave
	((bind_failed == 0)) || iocost_die "cannot rebind the owned scsi_debug disk"
	[[ ${IOCOST_SCSI_ADDRESS} == "${previous_address}" ]] \
		|| iocost_die "sd rebind selected a different SCSI device"
}

iocost_set_scsi_debug_cost_profile() {
	local profile=$1 device_id=${IOCOST_SCSI_DEVICE_ID}
	local rbps riops wbps wiops
	[[ ${IOCOST_SCSI_DEBUG_BOUND} == 1 ]] \
		|| iocost_die "cannot configure an unbound scsi_debug disk"
	case ${profile} in
	high)
		rbps=1073741824
		riops=262144
		wbps=1073741824
		wiops=262144
		;;
	long)
		rbps=4096
		riops=1
		wbps=4096
		wiops=1
		;;
	*) iocost_die "unsupported scsi_debug IOCOST profile: ${profile}" ;;
	esac
	printf '%s ctrl=user model=linear rbps=%s rseqiops=%s rrandiops=%s wbps=%s wseqiops=%s wrandiops=%s\n' \
		"${device_id}" "${rbps}" "${riops}" "${riops}" "${wbps}" "${wiops}" "${wiops}" \
		> "${IOCOST_CGROUP_ROOT}/io.cost.model"
	printf '%s enable=1 ctrl=user rpct=0.00 rlat=0 wpct=0.00 wlat=0 min=100.00 max=100.00\n' \
		"${device_id}" > "${IOCOST_CGROUP_ROOT}/io.cost.qos"
	iocost_assert_device_cost_tokens "${device_id}" "${IOCOST_CGROUP_ROOT}/io.cost.model" \
		'ctrl=user' 'model=linear' "rbps=${rbps}" "rseqiops=${riops}" \
		"rrandiops=${riops}" "wbps=${wbps}" "wseqiops=${wiops}" "wrandiops=${wiops}"
	iocost_assert_device_cost_tokens "${device_id}" "${IOCOST_CGROUP_ROOT}/io.cost.qos" \
		'enable=1' 'ctrl=user' 'rpct=0\.00' 'rlat=0' 'wpct=0\.00' 'wlat=0' \
		'min=100\.00' 'max=100\.00'
}

iocost_prepare_scsi_debug_write() {
	local cgroup=$1 block_size=${2:-${IOCOST_IO_BLOCK_SIZE}}
	local sectors capacity seek log_file pid record_failed=0 private_parent resolved_cgroup
	iocost_assert_scsi_debug_topology_safe
	private_parent=$(realpath -- "${IOCOST_CGROUP_PARENT}") \
		|| iocost_die "cannot resolve private cgroup parent"
	resolved_cgroup=$(realpath -- "${cgroup}") \
		|| iocost_die "cannot resolve scsi_debug workload cgroup"
	[[ ${resolved_cgroup} == "${private_parent}/"* && -w ${cgroup}/cgroup.procs ]] \
		|| iocost_die "scsi_debug workload cgroup is outside the private subtree"
	[[ ${block_size} =~ ^[1-9][0-9]*$ ]] \
		|| iocost_die "scsi_debug direct-write block size must be positive"
	sectors=$(iocost_file_value "/sys/class/block/${IOCOST_SCSI_BLOCK_NAME}/size") \
		|| iocost_die "cannot read scsi_debug capacity"
	[[ ${sectors} =~ ^[1-9][0-9]*$ ]] || iocost_die "invalid scsi_debug capacity"
	capacity=$((sectors * 512 / block_size))
	((capacity > 1)) || iocost_die "scsi_debug device is too small"
	seek=$((IOCOST_SCSI_NEXT_SEEK_BLOCK % (capacity - 1)))
	IOCOST_SCSI_NEXT_SEEK_BLOCK=$(((seek + 32) % capacity))
	log_file=${IOCOST_TMP_DIR}/write-scsi-${BASHPID}-${RANDOM}.log
	iocost_signal_critical_enter
	setsid bash -c '
		kill -STOP "$$"
		exec dd if=/dev/zero of="$1" bs="$2" count=1 seek="$3" \
			oflag=direct conv=notrunc status=none
	' iocost-scsi-write "${IOCOST_SCSI_WORKLOAD_DEVICE}" "${block_size}" "${seek}" \
		> "${log_file}" 2>&1 &
	pid=$!
	iocost_record_pid "${pid}" "direct-write-scsi-debug" || record_failed=1
	IOCOST_LAST_PID=${pid}
	iocost_signal_critical_leave
	((record_failed == 0)) \
		|| iocost_die "cannot register spawned scsi_debug direct-write pid ${pid}"
	if ! iocost_wait_for_state "${pid}" T 5; then
		[[ ! -s ${log_file} ]] || sed 's/^/[scsi-write] /' "${log_file}" >&2
		iocost_die "scsi_debug direct write did not reach the ownership barrier"
	fi
	printf '%s\n' "${pid}" > "${cgroup}/cgroup.procs"
	[[ $(iocost_file_value "/proc/${pid}/cgroup") == *"${cgroup#${IOCOST_CGROUP_ROOT}}"* ]] \
		|| iocost_die "scsi_debug direct write was not placed in its private cgroup"
	IOCOST_LAST_PID=${pid}
}

iocost_start_scsi_debug_write() {
	iocost_prepare_scsi_debug_write "$@"
	iocost_release_prepared_write "${IOCOST_LAST_PID}"
}

iocost_prepare_scsi_debug_batch_write() {
	((IOCOST_WRITE_BATCH_OPEN == 1 && IOCOST_WRITE_BATCH_RELEASED == 0)) \
		|| iocost_die "direct-write batch is not accepting scsi_debug issuers"
	iocost_prepare_scsi_debug_write "$@"
	IOCOST_WRITE_BATCH_PIDS+=("${IOCOST_LAST_PID}")
}

iocost_run_scsi_debug_unload() {
	local deadline=$1 pid status=0
	(iocost_signal_critical_command modprobe -r scsi_debug < /dev/null > /dev/null 2>&1) &
	pid=$!
	if ! iocost_record_pid "${pid}" "scsi_debug module removal"; then
		if ! iocost_pid_is_reapable "${pid}"; then
			iocost_warn "cannot supervise scsi_debug module removal pid ${pid}"
			iocost_stop_pid "${pid}" \
				|| iocost_warn "scsi_debug module removal pid ${pid} remains live"
			return 124
		fi
	fi
	while iocost_pid_is_running_original "${pid}" && ((SECONDS < deadline)); do
		sleep 0.05
	done
	if iocost_pid_is_running_original "${pid}"; then
		iocost_warn "timed out removing the scsi_debug module"
		iocost_stop_pid "${pid}" \
			|| iocost_warn "scsi_debug module removal survived bounded cleanup"
		return 124
	fi
	if ! iocost_pid_is_reapable "${pid}"; then
		iocost_warn "scsi_debug module removal lost its registered identity"
		iocost_stop_pid "${pid}" \
			|| iocost_warn "scsi_debug module removal cannot be proved stopped"
		return 124
	fi
	wait "${pid}" || status=$?
	iocost_forget_pid "${pid}"
	return "${status}"
}

iocost_cleanup_scsi_debug() {
	local deadline failed=0 unload_status device_id
	((IOCOST_SCSI_DEBUG_LOADED == 1)) || return 0
	(iocost_assert_scsi_debug_block_population) || return 1
	if ((IOCOST_SCSI_DEBUG_BOUND == 1)); then
		(iocost_assert_scsi_debug_topology_safe) || return 1
	fi
	deadline=$((SECONDS + IOCOST_LOOP_CLEANUP_TIMEOUT_SECONDS))
	iocost_signal_critical_enter
	while :; do
		if iocost_run_scsi_debug_unload "${deadline}"; then
			break
		else
			unload_status=$?
		fi
		if ((unload_status == 124 || SECONDS >= deadline)); then
			failed=1
			break
		fi
		sleep 0.05
	done
	if ((failed == 0)); then
		IOCOST_SCSI_DEBUG_LOADED=0
		IOCOST_SCSI_DEBUG_BOUND=0
		IOCOST_SCSI_DEBUG_TRANSITIONING=0
	fi
	iocost_signal_critical_leave
	((failed == 0)) || return 1
	[[ ! -e /sys/module/scsi_debug ]] || return 1
	iocost_wait_scsi_debug_block_count 0 || return 1
	for device_id in "${IOCOST_SCSI_DEVICE_IDS[@]}"; do
		iocost_device_row_is_absent_for \
			"${device_id}" "${IOCOST_CGROUP_ROOT}/io.cost.model" || return 1
		iocost_device_row_is_absent_for \
			"${device_id}" "${IOCOST_CGROUP_ROOT}/io.cost.qos" || return 1
	done
	IOCOST_SCSI_BLOCK_NAME=""
	IOCOST_SCSI_DEVICE=""
	IOCOST_SCSI_WORKLOAD_DEVICE=""
	IOCOST_SCSI_DEVICE_ID=""
	IOCOST_SCSI_PREVIOUS_DEVICE_ID=""
	IOCOST_SCSI_ADDRESS=""
	IOCOST_SCSI_DEVICE_PATH=""
	IOCOST_SCSI_NEXT_SEEK_BLOCK=0
	IOCOST_SCSI_DEVICE_IDS=()
}

iocost_scsi_debug_is_clean() {
	((IOCOST_SCSI_DEBUG_LOADED == 0 && IOCOST_SCSI_DEBUG_BOUND == 0)) \
		&& [[ -z ${IOCOST_SCSI_BLOCK_NAME} && -z ${IOCOST_SCSI_DEVICE} &&
			-z ${IOCOST_SCSI_WORKLOAD_DEVICE} &&
			-z ${IOCOST_SCSI_DEVICE_ID} && -z ${IOCOST_SCSI_PREVIOUS_DEVICE_ID} &&
			-z ${IOCOST_SCSI_ADDRESS} && -z ${IOCOST_SCSI_DEVICE_PATH} &&
			${IOCOST_SCSI_NEXT_SEEK_BLOCK} == 0 &&
			${#IOCOST_SCSI_DEVICE_IDS[@]} == 0 ]]
}

# Prebuilt runtime and fault qualification.

IOCOST_QUALIFICATION_PID=""
IOCOST_QUALIFICATION_RESULT=""
IOCOST_QUALIFICATION_DONE_FILE=""
IOCOST_QUALIFICATION_LOG_FILE=""
IOCOST_LAST_QUALIFICATION_RESULT=""

iocost_start_runtime() {
	local mode=$1 tag=$2
	local protocol_dir=${IOCOST_TMP_DIR}/runtime-${tag}-${RANDOM}
	local ready=${protocol_dir}/ready done=${protocol_dir}/done
	local result=${protocol_dir}/result.json log_file=${protocol_dir}/runtime.log pid
	local record_failed=0
	shift 2
	mkdir -m 0700 "${protocol_dir}"
	iocost_signal_critical_enter
	setsid bash -c '
		root_dir=$1
		shift
		cd "${root_dir}"
		exec "$@"
	' iocost-runtime "${IOCOST_ROOT_DIR}" \
		env \
		TEST_IOCOST_REQUIRED=1 \
		TEST_IOCOST_OBJECT="${mode}" \
		TEST_IOCOST_READY_FILE="${ready}" \
		TEST_IOCOST_DONE_FILE="${done}" \
		TEST_IOCOST_RESULT_FILE="${result}" \
		TEST_IOCOST_TIMEOUT_SECONDS="${IOCOST_RUNTIME_TIMEOUT_SECONDS}" \
		"$@" \
		"${IOCOST_ROOT_DIR}/${IOCOST_RUNTIME_TEST_REL}" \
		-test.run "${IOCOST_RUNTIME_TEST_NAME}" -test.v \
		> "${log_file}" 2>&1 &
	pid=$!
	iocost_record_pid "${pid}" "runtime-${mode}-${tag}" || record_failed=1
	IOCOST_LAST_PID=${pid}
	IOCOST_LAST_RESULT=${result}
	IOCOST_RUNTIME_DONE_FILE=${done}
	IOCOST_RUNTIME_LOG_FILE=${log_file}
	iocost_signal_critical_leave
	((record_failed == 0)) || iocost_die "cannot register spawned ${mode} runtime pid ${pid}"
	if ! iocost_wait_for_file "${ready}" "${pid}" "${mode} runtime ready file"; then
		[[ ! -f ${log_file} ]] || sed 's/^/[runtime] /' "${log_file}" >&2
		iocost_die "${mode} runtime did not become ready"
	fi
}

iocost_finish_runtime() {
	local pid=$1 result=$2 done=$3 log_file=$4
	[[ ! -e ${done} ]] || iocost_die "runtime done path already exists: ${done}"
	printf 'done\n' > "${done}"
	if ! iocost_wait_pid "${pid}"; then
		[[ ! -f ${log_file} ]] || sed 's/^/[runtime] /' "${log_file}" >&2
		iocost_die "runtime harness failed"
	fi
	[[ -s ${result} && ! -L ${result} ]] || iocost_die "runtime result is missing: ${result}"
}

iocost_start_qualification() {
	local tag=$1 timeout_seconds=$2
	shift 2
	[[ ${timeout_seconds} =~ ^[1-9][0-9]*$ && ${timeout_seconds} -le 300 ]] \
		|| iocost_die "qualification timeout must be a positive integer no greater than 300"
	[[ -z ${IOCOST_QUALIFICATION_PID} ]] \
		|| iocost_die "an IOCOST qualification runtime is already active"
	iocost_start_runtime qualification "${tag}" \
		TEST_IOCOST_TIMEOUT_SECONDS="${timeout_seconds}" "$@"
	IOCOST_QUALIFICATION_PID=${IOCOST_LAST_PID}
	IOCOST_QUALIFICATION_RESULT=${IOCOST_LAST_RESULT}
	IOCOST_QUALIFICATION_DONE_FILE=${IOCOST_RUNTIME_DONE_FILE}
	IOCOST_QUALIFICATION_LOG_FILE=${IOCOST_RUNTIME_LOG_FILE}
}

iocost_checkpoint_qualification() {
	[[ -n ${IOCOST_QUALIFICATION_PID} ]] || iocost_die "no qualification runtime is active"
	local request=${IOCOST_QUALIFICATION_DONE_FILE}.checkpoint
	[[ ! -e ${request} ]] || iocost_die "qualification checkpoint already requested"
	printf 'checkpoint\n' > "${request}"
	iocost_wait_for_file "${IOCOST_QUALIFICATION_RESULT}.checkpoint" \
		"${IOCOST_QUALIFICATION_PID}" "qualification checkpoint" \
		|| iocost_die "qualification checkpoint failed"
}

iocost_finish_qualification() {
	[[ -n ${IOCOST_QUALIFICATION_PID} ]] \
		|| iocost_die "no IOCOST qualification runtime is active"
	iocost_finish_runtime \
		"${IOCOST_QUALIFICATION_PID}" \
		"${IOCOST_QUALIFICATION_RESULT}" \
		"${IOCOST_QUALIFICATION_DONE_FILE}" \
		"${IOCOST_QUALIFICATION_LOG_FILE}"
	IOCOST_LAST_QUALIFICATION_RESULT=${IOCOST_QUALIFICATION_RESULT}
	IOCOST_QUALIFICATION_PID=""
	IOCOST_QUALIFICATION_RESULT=""
	IOCOST_QUALIFICATION_DONE_FILE=""
	IOCOST_QUALIFICATION_LOG_FILE=""
}

iocost_assert_runtime_common() {
	local result=$1 require_observation=$2 status_policy=${3:-healthy}
	# Optional counts are the exact IOC, owner, and aggregate rows after drain.
	python3 - "${result}" "${require_observation}" "${status_policy}" "${@:4}" << 'PY'
import json
import re
import sys

path, require_observation, status_policy = sys.argv[1:4]
expected_rows = sys.argv[4:]
with open(path, encoding="utf-8") as source:
    result = json.load(source)
if result.get("error") != "":
    raise SystemExit(f"runtime error: {result.get('error')!r}")
status = result.get("status")
expected_status = {"reason", "errno"}
if not isinstance(status, dict) or set(status) != expected_status:
    raise SystemExit(f"invalid runtime status object: {status!r}")
if type(status["reason"]) is not int or status["reason"] < 0:
    raise SystemExit(f"invalid runtime stop reason: {status!r}")
if type(status["errno"]) is not int or status["errno"] > 0:
    raise SystemExit(f"invalid runtime helper errno: {status!r}")
if status_policy == "healthy" and any(status[name] != 0 for name in expected_status):
    raise SystemExit(f"runtime status is not healthy: {status!r}")
if status_policy not in {"healthy", "fault"}:
    raise SystemExit(f"invalid runtime status policy: {status_policy!r}")
drain = result.get("drain")
if not isinstance(drain, dict) or set(drain) != {
    "pending_rows", "active_wake_frames", "drained"
}:
    raise SystemExit(f"invalid drain object: {drain!r}")
if drain != {"pending_rows": 0, "active_wake_frames": 0, "drained": True}:
    raise SystemExit(f"runtime did not drain: {drain!r}")
observation = result.get("observation")
if expected_rows:
    if len(expected_rows) != 3 or any(not re.fullmatch(r"[0-9]+", value) for value in expected_rows):
        raise SystemExit(f"invalid expected map row counts: {expected_rows!r}")
    for name, expected in zip(("ioc_rows", "owner_rows", "aggregate_rows"), expected_rows):
        value = observation.get(name) if isinstance(observation, dict) else None
        if type(value) is not int or value != int(expected, 10):
            raise SystemExit(f"unexpected observation {name}: {value!r}, want {expected}")
if require_observation == "1":
    expected = {
        "major", "first_minor", "ioc_ptr", "ioc_id", "iocg_ptr", "css",
        "css_serial", "ioc_rows", "owner_rows", "aggregate_rows",
    }
    if not isinstance(observation, dict) or set(observation) != expected:
        raise SystemExit(f"invalid observation object: {observation!r}")
    for name in ("ioc_ptr", "ioc_id", "iocg_ptr", "css", "css_serial"):
        value = observation[name]
        if not isinstance(value, str) or not re.fullmatch(r"0x[0-9a-f]+", value) or int(value, 16) == 0:
            raise SystemExit(f"invalid observation {name}: {value!r}")
    for name in ("major", "first_minor", "ioc_rows", "owner_rows", "aggregate_rows"):
        value = observation[name]
        if type(value) is not int or value < 0:
            raise SystemExit(f"invalid observation {name}: {value!r}")
PY
}

iocost_assert_qualification_result() {
	local result=$1
	shift
	(($# > 0)) || iocost_die "qualification assertion requires a device expectation"
	iocost_assert_runtime_common "${result}" 0 \
		|| iocost_die "qualification runtime result failed common validation"
	python3 - "${result}" "$@" << 'PY'
import json
import math
import re
import sys

path = sys.argv[1]
raw_expectations = sys.argv[2:]
expected = {}
device_re = re.compile(r"^[0-9]+:[0-9]+$")
for raw in raw_expectations:
    fields = raw.split(",")
    if len(fields) != 4:
        raise SystemExit(f"invalid device expectation {raw!r}")
    device, count_raw, minimum_raw, maximum_raw = fields
    if not device_re.fullmatch(device) or device in expected:
        raise SystemExit(f"invalid or duplicate expected device {device!r}")
    try:
        count = int(count_raw, 10)
        minimum = float(minimum_raw)
        maximum = float(maximum_raw)
    except ValueError as error:
        raise SystemExit(f"invalid device expectation {raw!r}: {error}")
    if count < 0 or not all(map(math.isfinite, (minimum, maximum))):
        raise SystemExit(f"invalid device expectation {raw!r}")
    if minimum < 0 or maximum < minimum:
        raise SystemExit(f"invalid average envelope {raw!r}")
    expected[device] = (count, minimum, maximum)

with open(path, encoding="utf-8") as source:
    result = json.load(source)
business = result.get("business_intervals")
expected_interval_names = {"first", "second"}
if not isinstance(business, dict) or set(business) != expected_interval_names:
    raise SystemExit(f"invalid business_intervals object: {business!r}")
lane_observation = result.get("lane_observation")
if not isinstance(lane_observation, dict) or set(lane_observation) != {
    "count_lanes", "wait_lanes", "raw_series",
}:
    raise SystemExit(f"invalid lane_observation object: {lane_observation!r}")


def parse_lanes(name):
    lanes = lane_observation[name]
    if not isinstance(lanes, list) or any(
        isinstance(lane, bool) or not isinstance(lane, int) or lane < 0
        for lane in lanes
    ):
        raise SystemExit(f"invalid {name}: {lanes!r}")
    if lanes != sorted(set(lanes)):
        raise SystemExit(f"unordered or duplicate {name}: {lanes!r}")
    return lanes


count_lanes = parse_lanes("count_lanes")
wait_lanes = parse_lanes("wait_lanes")
if count_lanes != wait_lanes:
    raise SystemExit(
        f"IOCOST count/wait per-CPU lane coverage differs: "
        f"count={count_lanes!r}, wait={wait_lanes!r}"
    )

raw_series = lane_observation["raw_series"]
if not isinstance(raw_series, list):
    raise SystemExit(f"invalid raw_series: {raw_series!r}")
raw_series_keys = {"device", "operation", "io_count", "wait_10us"}
decimal_u64_re = re.compile(r"0|[1-9][0-9]*")
u64_max = (1 << 64) - 1
raw_values = {}
raw_order = []
for index, series in enumerate(raw_series):
    if not isinstance(series, dict) or set(series) != raw_series_keys:
        raise SystemExit(f"invalid raw series {index}: {series!r}")
    device = series["device"]
    operation = series["operation"]
    if not isinstance(device, str) or not device_re.fullmatch(device):
        raise SystemExit(f"invalid raw device {device!r}")
    if device not in expected or operation != "write":
        raise SystemExit(
            f"unexpected raw IOCOST identity {(device, operation)!r}"
        )
    key = (device, operation)
    if key in raw_values:
        raise SystemExit(f"duplicate raw IOCOST series: {key!r}")
    values = []
    for name in ("io_count", "wait_10us"):
        value = series[name]
        if not isinstance(value, str) or not decimal_u64_re.fullmatch(value):
            raise SystemExit(f"invalid raw {name}: {value!r}")
        decoded = int(value, 10)
        if decoded > u64_max:
            raise SystemExit(f"raw {name} exceeds u64: {value!r}")
        values.append(decoded)
    io_count, wait_10us = values
    if io_count == 0 and wait_10us != 0:
        raise SystemExit(
            f"raw wait_10us is nonzero with no IO for {key!r}: {wait_10us}"
        )
    raw_values[key] = (io_count, wait_10us)
    raw_order.append(key)
if raw_order != sorted(raw_order):
    raise SystemExit(f"raw IOCOST series are not sorted: {raw_order!r}")

expected_positive = any(count > 0 for count, _, _ in expected.values())
raw_positive = any(io_count > 0 for io_count, _ in raw_values.values())
if bool(count_lanes) != raw_positive or raw_positive != expected_positive:
    raise SystemExit(
        f"IOCOST lane/raw presence does not match expected positive count: "
        f"lanes={count_lanes!r}, raw={raw_values!r}, expected={expected!r}"
    )

count_name = "waitq_io_count"
average_name = "average_wait_milliseconds"
container_names = {
    "container_waitq_io_count",
    "container_average_wait_milliseconds",
}
expected_metric_keys = {"name", "value", "labels"}
expected_label_keys = {"name", "value"}
expected_label_names = ["region", "host", "device", "operation", "scope"]


def parse_interval(interval_name):
    metrics = business[interval_name]
    if not isinstance(metrics, list):
        raise SystemExit(f"{interval_name} business interval is not a list")
    parsed = []
    seen = set()
    for index, metric in enumerate(metrics):
        if not isinstance(metric, dict) or set(metric) != expected_metric_keys:
            raise SystemExit(
                f"invalid {interval_name} metric {index}: {metric!r}"
            )
        name = metric["name"]
        if name in container_names:
            raise SystemExit(
                f"container IOCOST row in empty-catalog qualification: {metric!r}"
            )
        if name not in {count_name, average_name}:
            raise SystemExit(f"foreign IOCOST metric name: {name!r}")
        value = metric["value"]
        if isinstance(value, bool) or type(value) not in (int, float):
            raise SystemExit(f"non-numeric IOCOST value: {value!r}")
        value = float(value)
        if not math.isfinite(value) or value < 0:
            raise SystemExit(f"invalid IOCOST value: {value!r}")
        labels = metric["labels"]
        if not isinstance(labels, list) or len(labels) != len(expected_label_names):
            raise SystemExit(f"invalid IOCOST labels: {labels!r}")
        label_names = []
        label_values = []
        for label in labels:
            if not isinstance(label, dict) or set(label) != expected_label_keys:
                raise SystemExit(f"invalid IOCOST label: {label!r}")
            if not isinstance(label["name"], str) or not isinstance(label["value"], str):
                raise SystemExit(f"non-string IOCOST label: {label!r}")
            label_names.append(label["name"])
            label_values.append(label["value"])
        if label_names != expected_label_names:
            raise SystemExit(
                f"IOCOST label order changed: {label_names!r}"
            )
        _, host, device, operation, scope = label_values
        if not host or not device_re.fullmatch(device):
            raise SystemExit(f"invalid IOCOST host/device labels: {labels!r}")
        if device not in expected or operation != "write" or scope not in {"host", "other"}:
            raise SystemExit(f"unexpected IOCOST label tuple: {labels!r}")
        key = (scope, device, name)
        if key in seen:
            raise SystemExit(f"duplicate IOCOST business row: {key!r}")
        seen.add(key)
        parsed.append((key, value, labels))
    return parsed


first = parse_interval("first")
first_devices = {key[1] for key, _, _ in first}
raw_devices = {device for device, operation in raw_values if operation == "write"}
if first_devices != raw_devices:
    raise SystemExit(
        f"raw/public IOCOST device sets differ: "
        f"raw={raw_devices!r}, public={first_devices!r}"
    )
for device, (count, _, _) in expected.items():
    if count != 0 and device not in first_devices:
        raise SystemExit(f"expected IOCOST device is absent: {device}")

expected_keys = {
    (scope, device, name)
    for scope in ("host", "other")
    for device in first_devices
    for name in (count_name, average_name)
}
actual_keys = {key for key, _, _ in first}
if actual_keys != expected_keys:
    raise SystemExit(
        f"IOCOST business series changed: {actual_keys!r}"
    )

first_values = {key: value for key, value, _ in first}
for device in expected:
    if device not in first_devices:
        continue
    host_count = first_values[("host", device, count_name)]
    other_count = first_values[("other", device, count_name)]
    host_average = first_values[("host", device, average_name)]
    other_average = first_values[("other", device, average_name)]
    count, minimum, maximum = expected[device]
    raw_count, raw_wait_10us = raw_values[(device, "write")]
    if raw_count != count:
        raise SystemExit(
            f"raw IOCOST count for {device} is {raw_count}, expected={count}"
        )
    if host_count != count or other_count != count:
        raise SystemExit(
            f"IOCOST count for {device} is host={host_count}, "
            f"other={other_count}, expected={count}"
        )
    if not math.isclose(host_average, other_average, rel_tol=1e-12, abs_tol=1e-9):
        raise SystemExit(f"IOCOST Host != Other average for {device}")
    if not minimum <= host_average <= maximum:
        raise SystemExit(
            f"IOCOST average for {device} is {host_average}, "
            f"expected range=[{minimum}, {maximum}]"
        )
    raw_average = 0.0
    if raw_count != 0:
        raw_average = float(raw_wait_10us) * 0.01 / float(raw_count)
    if not math.isclose(host_average, raw_average, rel_tol=1e-12, abs_tol=1e-9):
        raise SystemExit(
            f"raw/public IOCOST average differs for {device}: "
            f"raw={raw_average}, public={host_average}"
        )

second = parse_interval("second")
if {key: labels for key, _, labels in second} != {
    key: labels for key, _, labels in first
}:
    raise SystemExit("second IOCOST interval changed business row labels")
for key, value, _ in second:
    if value != 0:
        raise SystemExit(f"second IOCOST interval is not drained: {key!r}={value}")
PY
}

iocost_assert_empty_qualification_result() {
	local result=$1 device=$2
	iocost_assert_qualification_result "${result}" "${device},0,0,0" || return
	python3 - "${result}" << 'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    result = json.load(source)
expected_lanes = dict(
    count_lanes=[],
    wait_lanes=[],
    raw_series=[],
)
if result.get("lane_observation") != expected_lanes:
    raise SystemExit(
        f"empty qualification created IOCOST raw state: "
        f"{result.get('lane_observation')!r}"
    )
expected_intervals = dict(
    first=[],
    second=[],
)
if result.get("business_intervals") != expected_intervals:
    raise SystemExit(
        f"empty qualification published IOCOST series: "
        f"{result.get('business_intervals')!r}"
    )
PY
}

iocost_observe_identity() {
	local pid result done log_file
	iocost_start_runtime observe identity \
		TEST_IOCOST_OBSERVE_MAJOR="${IOCOST_DEVICE_MAJOR}" \
		TEST_IOCOST_OBSERVE_FIRST_MINOR="${IOCOST_DEVICE_FIRST_MINOR}"
	pid=${IOCOST_LAST_PID}
	result=${IOCOST_LAST_RESULT}
	done=${IOCOST_RUNTIME_DONE_FILE}
	log_file=${IOCOST_RUNTIME_LOG_FILE}
	iocost_run_leaf_write 2
	iocost_finish_runtime "${pid}" "${result}" "${done}" "${log_file}"
	iocost_assert_runtime_common "${result}" 1 || iocost_die "observe runtime result failed validation"
	read -r IOCOST_OBSERVED_IOCG_PTR IOCOST_OBSERVED_CSS_SERIAL < <(
		python3 - "${result}" "${IOCOST_DEVICE_MAJOR}" "${IOCOST_DEVICE_FIRST_MINOR}" << 'PY'
import json
import sys

path, major, first_minor = sys.argv[1:]
with open(path, encoding="utf-8") as source:
    observation = json.load(source)["observation"]
if observation["major"] != int(major) or observation["first_minor"] != int(first_minor):
    raise SystemExit(f"observed device mismatch: {observation!r}")
if observation["ioc_rows"] < 1 or observation["owner_rows"] < 1 or observation["aggregate_rows"] < 1:
    raise SystemExit(f"observation omitted live fixture rows: {observation!r}")
print(observation["iocg_ptr"], observation["css_serial"])
PY
	) || iocost_die "cannot decode observed IOCG identity"
	[[ -n ${IOCOST_OBSERVED_IOCG_PTR} && -n ${IOCOST_OBSERVED_CSS_SERIAL} ]] \
		|| iocost_die "observe runtime returned an empty IOCG identity"
	iocost_log "observed fixture identity for ${IOCOST_DEVICE_ID}"
}

iocost_start_diagnostic() {
	local tag=$1 fault_mask=${2:-0}
	[[ -n ${IOCOST_OBSERVED_IOCG_PTR} && -n ${IOCOST_OBSERVED_CSS_SERIAL} ]] \
		|| iocost_die "diagnostic runtime requires an observed fixture identity"
	iocost_start_runtime diagnostic "${tag}" \
		TEST_IOCOST_DIAG_FAULT_MASK="${fault_mask}" \
		TEST_IOCOST_DIAG_MAJOR="${IOCOST_DEVICE_MAJOR}" \
		TEST_IOCOST_DIAG_FIRST_MINOR="${IOCOST_DEVICE_FIRST_MINOR}" \
		TEST_IOCOST_DIAG_CSS_SERIAL="${IOCOST_OBSERVED_CSS_SERIAL}" \
		TEST_IOCOST_DIAG_IOCG_PTR="${IOCOST_OBSERVED_IOCG_PTR}"
}

iocost_assert_diagnostic() {
	local result=$1 mode=$2
	iocost_assert_runtime_common "${result}" 0 || iocost_die "diagnostic runtime result failed validation"
	python3 - "${result}" "${mode}" << 'PY'
import json
import sys

path, mode = sys.argv[1:]
with open(path, encoding="utf-8") as source:
    result = json.load(source)
counters = result.get("diagnostic_counters")
expected = {
    "start_guard_passes", "wake_entry_hits",
    "wake_return_zero", "wake_return_minus_one", "settled_count",
    "map_full_injections", "collision_injections",
    "delete_failure_injections",
}
if not isinstance(counters, dict) or set(counters) != expected:
    raise SystemExit(f"invalid diagnostic counters: {counters!r}")
if any(type(value) is not int or value < 0 for value in counters.values()):
    raise SystemExit(f"non-u64 diagnostic counter: {counters!r}")
for name in (
    "map_full_injections", "collision_injections",
    "delete_failure_injections",
):
    if counters[name] != 0:
        raise SystemExit(f"unexpected fault counter {name}: {counters!r}")
zero = counters["wake_return_zero"]
minus_one = counters["wake_return_minus_one"]
settled = counters["settled_count"]
guards = counters["start_guard_passes"]
entries = counters["wake_entry_hits"]
if zero < 1 or settled != zero or guards != settled:
    raise SystemExit(f"wait completion pairing is invalid: {counters!r}")
if entries != zero + minus_one:
    raise SystemExit(f"wake entry/return pairing is invalid: {counters!r}")
if mode == "repeated" and minus_one < 2:
    raise SystemExit(f"repeated -1 wake returns were not observed: {counters!r}")
if mode == "offline" and zero < 1:
    raise SystemExit(f"offline waiter did not settle: {counters!r}")
PY
}

iocost_assert_fault_diagnostic() {
	local result=$1 fault=$2
	iocost_assert_runtime_common "${result}" 0 fault \
		|| iocost_die "fault diagnostic runtime result failed validation"
	python3 - "${result}" "${fault}" << 'PY'
import json
import sys

path, fault = sys.argv[1:]
with open(path, encoding="utf-8") as source:
    result = json.load(source)

status_changes = {
    "map-full": {"reason": 3, "errno": -7},
    "collision": {},
    "delete-failure": {"reason": 4, "errno": -2},
}
fault_counters = {
    "map-full": "map_full_injections",
    "collision": "collision_injections",
    "delete-failure": "delete_failure_injections",
}
if fault not in status_changes:
    raise SystemExit(f"unknown IOCOST diagnostic fault: {fault!r}")

status = result["status"]
expected_status = {name: 0 for name in status}
expected_status.update(status_changes[fault])
if status != expected_status:
    raise SystemExit(
        f"{fault} status mismatch: {status!r}"
    )

counters = result.get("diagnostic_counters")
expected_counter_names = {
    "start_guard_passes", "wake_entry_hits",
    "wake_return_zero", "wake_return_minus_one", "settled_count",
    "map_full_injections", "collision_injections",
    "delete_failure_injections",
}
if not isinstance(counters, dict) or set(counters) != expected_counter_names:
    raise SystemExit(f"invalid diagnostic counters: {counters!r}")
if any(type(value) is not int or value < 0 for value in counters.values()):
    raise SystemExit(f"non-u64 diagnostic counter: {counters!r}")
if counters["start_guard_passes"] != 1:
    raise SystemExit(f"{fault} did not consume exactly one admission: {counters!r}")
for name in (
    "map_full_injections", "collision_injections", "delete_failure_injections"
):
    expected = int(name == fault_counters[fault])
    if counters[name] != expected:
        raise SystemExit(f"{fault} injection counter mismatch: {counters!r}")
if counters["settled_count"] != int(fault == "collision"):
    raise SystemExit(f"{fault} settlement count mismatch: {counters!r}")

if fault == "map-full":
    for name in (
        "wake_entry_hits", "wake_return_zero", "wake_return_minus_one"
    ):
        if counters[name] != 0:
            raise SystemExit(f"{fault} unexpectedly reached wake accounting: {counters!r}")
elif fault == "collision":
    if counters["wake_return_zero"] != 1:
        raise SystemExit(f"replacement did not reach one final wake: {counters!r}")
    if counters["wake_entry_hits"] != (
        counters["wake_return_zero"] + counters["wake_return_minus_one"]
    ):
        raise SystemExit(f"replacement wake accounting is inconsistent: {counters!r}")
elif (counters["wake_entry_hits"] != 1 or counters["wake_return_zero"] != 0
      or counters["wake_return_minus_one"] != 0):
    raise SystemExit(f"delete failure did not stop at ownership transfer: {counters!r}")
PY
}

# Daemon configuration and exported metrics.

iocost_blacklist_for_daemon() {
	printf '%s\n' \
		arp ascend_npu cpuidle cpusys cpu_stat cpu_util diskio dload dropwatch ethtool \
		fastfork hungtask io_health blk_throtl iolatency iotracing loadavg \
		irqtracing \
		memburst memory_bandwidth memory_buddyinfo memory_events memory_free memory_others \
		memory_oom_kill memory_threshold_snapshot \
		memory_priority_reclaim memory_reclaim memory_reclaim_events memory_vmstat \
		metax_gpu mountpoint_perm mthreads_gpu mthreads_xid netdev netdev_bonding_lacp netdev_dcb netdev_events \
		netdev_hw netdev_qdisc netdev_rdma_link netdev_txqueue_timeout net_rx_latency \
		netstat oom priority_reclaim ras runqlat sched-blame sched_tick sockstat softirq \
		softirq_tracing softlockup tcp_memory tcp_retransmit tcpshark tracing_status xfs
}

iocost_write_daemon_config() {
	local config_path=$1 port=$2 name first=1
	{
		printf '# IOCOST OETest: every tracing/metric module except iocost is disabled.\n'
		printf 'BlackList = ['
		while read -r name; do
			[[ -n ${name} ]] || continue
			if ((first == 0)); then
				printf ', '
			fi
			printf '"%s"' "${name}"
			first=0
		done < <(iocost_blacklist_for_daemon)
		printf ']\n\n'
		printf '[HTTPServer]\n'
		printf '    ListenAddress = "127.0.0.1:%s"\n\n' "${port}"
		printf '[HTTPServer.Auth]\n'
		printf '    BearerToken = "iocost-oetest-local-token"\n\n'
		printf '[Log]\n'
		printf '    Level = "Info"\n\n'
		printf '[Runtime]\n'
		printf '    StartupCPULimitCores = 1.0\n'
		printf '    CPULimitCores = 2.0\n'
		printf '    MemoryLimitMiB = 2048\n'
	} > "${config_path}"
	chmod 0600 "${config_path}"
}

iocost_allocate_port() {
	python3 - << 'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
    listener.bind(("127.0.0.1", 0))
    print(listener.getsockname()[1])
PY
}

iocost_tcp_ready() {
	local port=$1
	python3 - "${port}" << 'PY'
import socket
import sys

with socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=0.2):
    pass
PY
}

iocost_assert_daemon_only_iocost() {
	python3 - "${IOCOST_DAEMON_LOG}" << 'PY'
import re
import sys

started = set()
with open(sys.argv[1], encoding="utf-8") as source:
    for line in source:
        if 'msg="tracer started"' not in line:
            continue
        names = re.findall(r'(?:^|\s)tracer="([^"]*)"(?:\s|$)', line)
        if len(names) != 1 or not names[0]:
            raise SystemExit(f"invalid tracer started log: {line!r}")
        started.add(names[0])
if started != {"iocost"}:
    raise SystemExit(f"daemon started tracers other than iocost: {sorted(started)!r}")
PY
}

iocost_assert_iocost_collector_success() {
	local response=$1
	[[ -s ${response} ]] || return 1
	python3 - "${response}" "${IOCOST_DAEMON_REGION}" << 'PY'
import json
import math
import re
import sys

path, region = sys.argv[1:]
target = "huatuo_bamai_scrape_collector_success"
duration = "huatuo_bamai_scrape_collector_duration_seconds"
collectors = {target: set(), duration: set()}
metric_re = re.compile(
    r"^([A-Za-z_:][A-Za-z0-9_:]*)(?:\{(.*)\})?\s+([^\s]+)(?:\s+[0-9]+)?$"
)
label_re = re.compile(r'([A-Za-z_][A-Za-z0-9_]*)="((?:\\.|[^"\\])*)"')


def parse_labels(raw):
    if raw is None or raw == "":
        return {}
    labels = {}
    offset = 0
    while offset < len(raw):
        match = label_re.match(raw, offset)
        if not match:
            raise SystemExit(f"invalid Prometheus labels: {raw!r}")
        name, escaped = match.groups()
        if name in labels:
            raise SystemExit(f"duplicate Prometheus label {name!r}")
        labels[name] = json.loads('"' + escaped + '"')
        offset = match.end()
        if offset == len(raw):
            break
        if raw[offset] != ",":
            raise SystemExit(f"invalid Prometheus label separator: {raw!r}")
        offset += 1
    return labels


matches = []
with open(path, encoding="utf-8") as source:
    for number, raw_line in enumerate(source, 1):
        line = raw_line.rstrip("\n")
        if not line or line.startswith("#"):
            continue
        metric = metric_re.match(line)
        if not metric:
            raise SystemExit(f"invalid Prometheus sample at line {number}: {line!r}")
        name, raw_labels, raw_value = metric.groups()
        if name not in collectors:
            continue
        labels = parse_labels(raw_labels)
        if not labels.get("collector"):
            raise SystemExit(f"missing collector label: {labels!r}")
        collectors[name].add(labels["collector"])
        if name != target:
            continue
        if labels.get("collector") != "iocost" or labels.get("region") != region:
            continue
        if set(labels) != {"host", "region", "collector"} or not labels["host"]:
            raise SystemExit(f"invalid IOCOST collector_success labels: {labels!r}")
        try:
            value = float(raw_value)
        except ValueError as error:
            raise SystemExit(f"invalid collector_success value: {error}")
        if not math.isfinite(value):
            raise SystemExit("non-finite IOCOST collector_success")
        matches.append(value)

for name, enabled in collectors.items():
    if enabled != {"iocost"}:
        raise SystemExit(f"daemon enabled collectors other than iocost in {name}: {sorted(enabled)!r}")
if matches != [1.0]:
    raise SystemExit(f"expected one IOCOST collector_success=1 sample, got {matches!r}")
PY
}

iocost_wait_daemon_collector_ready() {
	local response=${IOCOST_DAEMON_DIR}/readiness.prom
	local curl_error=${IOCOST_DAEMON_DIR}/readiness-curl.log
	local validation_error=${IOCOST_DAEMON_DIR}/readiness-validation.log
	local deadline=$((SECONDS + IOCOST_WAIT_TIMEOUT_SECONDS))
	while ((SECONDS < deadline)); do
		if ! iocost_pid_is_running_original "${IOCOST_DAEMON_PID}"; then
			return 1
		fi
		if curl --fail --silent --show-error --connect-timeout 2 --max-time 15 \
			"http://127.0.0.1:${IOCOST_DAEMON_PORT}/metrics" \
			> "${response}" 2> "${curl_error}" \
			&& iocost_assert_iocost_collector_success "${response}" 2> "${validation_error}"; then
			return 0
		fi
		sleep 0.2
	done
	[[ ! -s ${curl_error} ]] || sed 's/^/[readiness-curl] /' "${curl_error}" >&2
	[[ ! -s ${validation_error} ]] || sed 's/^/[readiness-metrics] /' "${validation_error}" >&2
	return 1
}

iocost_start_daemon() {
	local config_dir config_path log_file pid deadline
	local record_failed=0
	[[ -z ${IOCOST_DAEMON_PID} ]] || iocost_die "huatuo-bamai is already running"
	IOCOST_DAEMON_START_SEQUENCE=$((IOCOST_DAEMON_START_SEQUENCE + 1))
	config_dir=${IOCOST_TMP_DIR}/daemon-${IOCOST_DAEMON_START_SEQUENCE}
	mkdir -m 0700 "${config_dir}"
	IOCOST_DAEMON_DIR=${config_dir}
	IOCOST_DAEMON_PORT=$(iocost_allocate_port) || iocost_die "cannot allocate a private daemon port"
	[[ ${IOCOST_DAEMON_PORT} =~ ^[0-9]+$ ]] || iocost_die "invalid private daemon port"
	config_path=${config_dir}/bamai.conf
	log_file=${config_dir}/daemon.log
	iocost_write_daemon_config "${config_path}" "${IOCOST_DAEMON_PORT}"
	iocost_signal_critical_enter
	setsid "${IOCOST_ROOT_DIR}/${IOCOST_DAEMON_REL}" \
		--config-dir "${config_dir}" \
		--config bamai.conf \
		--bpf-dir "${IOCOST_ROOT_DIR}/bpf" \
		--tools-bin-dir "${IOCOST_ROOT_DIR}/_output/oetest/bin" \
		--region "${IOCOST_DAEMON_REGION}" \
		--disable-storage \
		--disable-kubelet \
		> "${log_file}" 2>&1 &
	pid=$!
	iocost_record_pid "${pid}" "huatuo-bamai" || record_failed=1
	IOCOST_DAEMON_PID=${pid}
	IOCOST_DAEMON_LOG=${log_file}
	iocost_signal_critical_leave
	((record_failed == 0)) || iocost_die "cannot register spawned huatuo-bamai pid ${pid}"
	deadline=$((SECONDS + 120))
	while ((SECONDS < deadline)); do
		if ! iocost_pid_is_running_original "${pid}"; then
			[[ ! -f ${log_file} ]] || sed 's/^/[huatuo-bamai] /' "${log_file}" >&2
			iocost_die "huatuo-bamai exited before becoming ready"
		fi
		if grep -Fq 'huatuo-bamai started successfully' "${log_file}" \
			&& iocost_tcp_ready "${IOCOST_DAEMON_PORT}" 2> /dev/null; then
			# Start() publishes the IOCOST session asynchronously. Readiness is
			# the first successful collector boundary, not a fixed wall-clock nap.
			iocost_wait_daemon_collector_ready || {
				[[ ! -f ${log_file} ]] || sed 's/^/[huatuo-bamai] /' "${log_file}" >&2
				iocost_die "IOCOST collector did not become ready before business traffic"
			}
			iocost_assert_daemon_only_iocost
			return 0
		fi
		sleep 0.1
	done
	[[ ! -f ${log_file} ]] || sed 's/^/[huatuo-bamai] /' "${log_file}" >&2
	iocost_die "huatuo-bamai did not listen on its private port"
}

iocost_stop_daemon() {
	[[ -n ${IOCOST_DAEMON_PID} ]] || return 0
	iocost_stop_pid "${IOCOST_DAEMON_PID}" \
		|| iocost_die "huatuo-bamai did not stop within the hard cleanup bound"
	IOCOST_DAEMON_PID=""
	if grep -Eqi 'panic|(^|[^[:alpha:]])error([^[:alpha:]]|$)' "${IOCOST_DAEMON_LOG}"; then
		sed 's/^/[huatuo-bamai] /' "${IOCOST_DAEMON_LOG}" >&2
		iocost_die "huatuo-bamai logged an error"
	fi
}

iocost_scrape_once() {
	local window=$1 output
	[[ -n ${IOCOST_DAEMON_PID} ]] || iocost_die "cannot scrape before daemon start"
	[[ -z ${IOCOST_SCRAPED_WINDOWS[${window}]:-} ]] \
		|| iocost_die "business window ${window} was scraped more than once"
	output=${IOCOST_TMP_DIR}/metrics-${window}.prom
	IOCOST_SCRAPED_WINDOWS[${window}]=1
	curl --fail --silent --show-error --connect-timeout 2 --max-time 35 \
		"http://127.0.0.1:${IOCOST_DAEMON_PORT}/metrics" > "${output}" \
		|| iocost_die "single scrape failed for business window ${window}"
	[[ -s ${output} ]] || iocost_die "empty metrics response for business window ${window}"
	IOCOST_LAST_SCRAPE=${output}
}

iocost_assert_metric_window() {
	local file=$1 mode=$2 expected_count=${3:-0} min_ms=${4:-0} max_ms=${5:-0}
	iocost_assert_iocost_collector_success "${file}" \
		|| iocost_die "business metrics response lacks one healthy IOCOST collector boundary"
	python3 - "${file}" "${IOCOST_DEVICE_ID}" "${IOCOST_DAEMON_REGION}" \
		"${mode}" "${expected_count}" "${min_ms}" "${max_ms}" << 'PY'
import json
import math
import re
import sys

path, device, region, mode, expected_raw, minimum_raw, maximum_raw = sys.argv[1:]
expected = float(expected_raw)
minimum = float(minimum_raw)
maximum = float(maximum_raw)
host_count_name = "huatuo_bamai_iocost_waitq_io_count"
host_average_name = "huatuo_bamai_iocost_average_wait_milliseconds"
container_names = {
    "huatuo_bamai_iocost_container_waitq_io_count",
    "huatuo_bamai_iocost_container_average_wait_milliseconds",
}
metric_re = re.compile(r"^([A-Za-z_:][A-Za-z0-9_:]*)(?:\{(.*)\})?\s+([^\s]+)(?:\s+[0-9]+)?$")
label_re = re.compile(r'([A-Za-z_][A-Za-z0-9_]*)="((?:\\.|[^"\\])*)"')

def parse_labels(raw):
    if raw is None or raw == "":
        return {}
    labels = {}
    offset = 0
    while offset < len(raw):
        match = label_re.match(raw, offset)
        if not match:
            raise SystemExit(f"invalid Prometheus labels: {raw!r}")
        name, escaped = match.groups()
        if name in labels:
            raise SystemExit(f"duplicate Prometheus label {name!r}")
        labels[name] = json.loads('"' + escaped + '"')
        offset = match.end()
        if offset == len(raw):
            break
        if raw[offset] != ",":
            raise SystemExit(f"invalid Prometheus label separator: {raw!r}")
        offset += 1
    return labels

samples = []
with open(path, encoding="utf-8") as source:
    for number, raw_line in enumerate(source, 1):
        line = raw_line.rstrip("\n")
        if not line or line.startswith("#"):
            continue
        match = metric_re.match(line)
        if not match:
            raise SystemExit(f"invalid Prometheus sample at line {number}: {line!r}")
        name, raw_labels, raw_value = match.groups()
        if name not in {host_count_name, host_average_name} | container_names:
            continue
        labels = parse_labels(raw_labels)
        try:
            value = float(raw_value)
        except ValueError as error:
            raise SystemExit(f"invalid metric value at line {number}: {error}")
        if not math.isfinite(value):
            raise SystemExit(f"non-finite IOCOST metric at line {number}")
        if labels.get("device") == device and labels.get("operation") == "write":
            samples.append((name, labels, value))

if any(name in container_names for name, _, _ in samples):
    raise SystemExit("container IOCOST samples exist with --disable-kubelet")
host_samples = [(name, labels, value) for name, labels, value in samples if name not in container_names]
if not host_samples:
    if mode in {"absent-or-zero", "any"}:
        print("0")
        raise SystemExit(0)
    raise SystemExit("target IOCOST host/other samples are absent")

expected_labels = {"region", "host", "device", "operation", "scope"}
by_key = {}
for name, labels, value in host_samples:
    if set(labels) != expected_labels:
        raise SystemExit(f"incomplete or extra IOCOST labels: {labels!r}")
    if labels["region"] != region or labels["device"] != device or labels["operation"] != "write":
        raise SystemExit(f"IOCOST label mismatch: {labels!r}")
    if not labels["host"]:
        raise SystemExit("IOCOST host label is empty")
    scope = labels["scope"]
    if scope not in {"host", "other"}:
        raise SystemExit(f"invalid IOCOST scope: {scope!r}")
    key = (name, scope)
    if key in by_key:
        raise SystemExit(f"duplicate IOCOST sample: {key!r}")
    by_key[key] = value
required = {
    (host_count_name, "host"), (host_count_name, "other"),
    (host_average_name, "host"), (host_average_name, "other"),
}
if set(by_key) != required:
    raise SystemExit(f"missing or extra target IOCOST samples: {by_key!r}")
host_count = by_key[(host_count_name, "host")]
other_count = by_key[(host_count_name, "other")]
host_average = by_key[(host_average_name, "host")]
other_average = by_key[(host_average_name, "other")]
if host_count != other_count or not math.isclose(host_average, other_average, rel_tol=1e-12, abs_tol=1e-9):
    raise SystemExit("Host != Other with an empty container catalog")
if host_count < 0 or host_average < 0:
    raise SystemExit("negative IOCOST business metric")
if mode in {"wellformed", "any"}:
    if host_count == 0 and host_average != 0:
        raise SystemExit("zero count has a nonzero average")
elif mode == "absent-or-zero":
    if host_count != 0 or host_average != 0:
        raise SystemExit(f"under-budget window was counted: count={host_count}, average={host_average}")
elif mode == "exact":
    if host_count != expected:
        raise SystemExit(f"IOCOST count={host_count}, expected={expected}")
    if not minimum <= host_average <= maximum:
        raise SystemExit(f"IOCOST average={host_average}, expected range=[{minimum}, {maximum}]")
else:
    raise SystemExit(f"unknown metric assertion mode: {mode}")
print(f"{host_average:.9f}")
PY
}

iocost_elapsed_bounds() {
	local start_ns=$1 end_ns=$2
	python3 - "${start_ns}" "${end_ns}" << 'PY'
import sys

start, end = map(int, sys.argv[1:])
if end <= start:
    raise SystemExit("non-positive workload elapsed time")
elapsed_ms = (end - start) / 1_000_000
lower = max(1.0, elapsed_ms - 200.0)
upper = elapsed_ms + 200.0
print(f"{lower:.9f} {upper:.9f}")
PY
}

iocost_assert_float_greater() {
	local greater=$1 lesser=$2 description=$3
	python3 - "${greater}" "${lesser}" "${description}" << 'PY'
import sys

greater, lesser = map(float, sys.argv[1:3])
if not greater > lesser:
    raise SystemExit(f"{sys.argv[3]}: {greater} is not greater than {lesser}")
PY
}

iocost_cleanup_fixture() {
	local failed=0 process_cleanup_failed=0 cgroup_cleanup_failed=0
	iocost_stop_all_pids || {
		process_cleanup_failed=1
		failed=1
	}
	iocost_cleanup_scsi_debug || failed=1
	iocost_remove_cgroups || {
		cgroup_cleanup_failed=1
		failed=1
	}
	iocost_restore_all_cost_states || {
		failed=1
	}
	if ((process_cleanup_failed == 1 || cgroup_cleanup_failed == 1)); then
		iocost_warn "preserving registered loops after incomplete dependency cleanup"
	else
		# Cost restoration is isolated per registered loop.  The registry
		# cleanup skips only an entry whose COST_CHANGED flag remains set, so a
		# failure on one private loop must not leak every unrelated loop minor.
		iocost_cleanup_registered_loops || failed=1
	fi
	if ((IOCOST_TMP_CREATED == 1)); then
		if ((failed != 0)) || ! iocost_registered_loops_are_clean \
			|| ! iocost_scsi_debug_is_clean; then
			iocost_warn "preserving private fixture for recovery: ${IOCOST_TMP_DIR}"
		elif [[ ${IOCOST_TMP_DIR} =~ ^/var/tmp/huatuo-iocost\.[A-Za-z0-9]+$ &&
			-d ${IOCOST_TMP_DIR} && ! -L ${IOCOST_TMP_DIR} ]]; then
			if rm -rf -- "${IOCOST_TMP_DIR}"; then
				IOCOST_TMP_CREATED=0
			else
				iocost_warn "failed to remove private temporary directory: ${IOCOST_TMP_DIR}"
				failed=1
			fi
		else
			iocost_warn "refusing unsafe temporary-directory cleanup: ${IOCOST_TMP_DIR}"
			failed=1
		fi
	fi
	return "${failed}"
}

iocost_cleanup() {
	((IOCOST_CLEANUP_RAN == 0)) || return 0
	IOCOST_CLEANUP_RAN=1
	local failed=0
	iocost_cleanup_fixture || failed=1
	if [[ -n ${IOCOST_ARTIFACT_DIGESTS_BEFORE} ]]; then
		(iocost_verify_artifacts_unchanged) || failed=1
	fi
	if ((IOCOST_LOCK_OPEN == 1)); then
		flock -u 9 || failed=1
		exec 9>&-
		IOCOST_LOCK_OPEN=0
	fi
	return "${failed}"
}

iocost_exit_trap() {
	local original_status=$1 cleanup_status=0
	# Cleanup owns external resources whose kernel/filesystem mutation and
	# registry update must stay atomic.  A second INT/TERM must not interrupt
	# that recovery window after the first signal has already selected the
	# final exit status.
	trap '' INT TERM
	trap - EXIT
	set +e
	iocost_cleanup
	cleanup_status=$?
	if ((cleanup_status != 0)); then
		iocost_warn "fixture or artifact cleanup failed"
		original_status=1
	fi
	exit "${original_status}"
}

iocost_install_traps() {
	trap 'iocost_exit_trap $?' EXIT
	trap 'iocost_handle_signal 130' INT
	trap 'iocost_handle_signal 143' TERM
}

iocost_runner_init() {
	local root_dir=$1
	IOCOST_ROOT_DIR=$(realpath -- "${root_dir}")
	iocost_check_required_mode
	iocost_check_commands
	iocost_check_platform
	iocost_check_kernel_config
	iocost_check_cgroup_delegation
	iocost_acquire_lock
	iocost_snapshot_artifacts
	iocost_create_private_tmp
	iocost_add_new_loop_minor
	iocost_attach_loop
	iocost_create_cgroups
	iocost_log "fixture ready: kernel=${IOCOST_KERNEL_RELEASE}, device=${IOCOST_DEVICE_ID}"
}
