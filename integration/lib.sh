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

set -euo pipefail

# --------------------------------- log --------------------------------------

TEST_LOG_TAG=${TEST_LOG_TAG:-"INTEGRATION TEST"}

log_info() { echo "[${TEST_LOG_TAG}] $*"; }
log_warn() { echo "[${TEST_LOG_TAG}][WARN] $*" >&2; }
log_error() { echo "[${TEST_LOG_TAG}][ERROR] $*" >&2; }
fatal() {
	echo "[${TEST_LOG_TAG}][FAIL] $*" >&2
	exit 1
}

# skip exits 0 so the harness treats it as success without false confidence.
skip() {
	echo "[${TEST_LOG_TAG}][SKIP] $*"
	exit 0
}

# --------------------------------- utils ------------------------------------

assert_eq() {
	local actual=$1 expect=$2 msg=${3:-""}
	[[ "$actual" == "$expect" ]] && return 0
	log_info "assert_eq: ${msg} actual=${actual}, expect=${expect}"
	return 1
}

# kernel_version_le <major> <minor>
# Returns 0 when the running kernel version is less than or equal to major.minor.
kernel_version_le() {
	local want_major=$1 want_minor=$2
	local version major minor

	version=$(uname -r)
	major=${version%%.*}
	version=${version#*.}
	minor=${version%%.*}

	[[ "${major}" =~ ^[0-9]+$ ]] || return 1
	[[ "${minor}" =~ ^[0-9]+$ ]] || return 1

	((major < want_major || (major == want_major && minor <= want_minor)))
}

# wait_until <timeout> <interval> <desc> <func> [args...]
# Returns 0 on success, 1 on timeout.
wait_until() {
	local timeout=$1 interval=$2 desc=$3
	shift 3
	local func=$1
	shift

	if ! type -t "$func" > /dev/null 2>&1; then
		log_error "wait_until expects function or command: %q" "$func"
		return 1
	fi

	local end=$(($(date +%s) + timeout))
	local attempt=0

	while [ "$(date +%s)" -lt "$end" ]; do
		attempt=$((attempt + 1))
		log_info "wait attempt #${attempt}: ${desc}, func/cmd: [${func} ${*}]"
		if "$func" "$@"; then
			return 0
		fi
		sleep "$interval"
	done

	log_error "wait_until timeout: ${desc}, func/cmd: [${func} ${*}]"
	return 1
}

profiler_ready() {
	local stdout=$1
	[[ -f "${stdout}" ]] && grep -q "data reading loop started" "${stdout}"
}

kprobe_available() {
	local symbol=$1
	local file candidate
	local files=(
		"/sys/kernel/tracing/available_filter_functions"
		"/sys/kernel/debug/tracing/available_filter_functions"
	)

	for file in "${files[@]}"; do
		[[ -r "${file}" ]] || continue
		for candidate in "${symbol}" "__x64_${symbol}"; do
			awk -v sym="${candidate}" '$1 == sym { found = 1; exit } END { exit !found }' "${file}" && return 0
		done
	done

	return 1
}

# ------------------------- bpf tool test scaffolding -------------------------

bpf_tool_setup() {
	local name=$1
	TOOL_BIN="${ROOT_DIR}/_output/bin/${name}"
	TOOL_BPF="${ROOT_DIR}/_output/bpf/${name}.o"
	TOOL_OUT="${HUATUO_BAMAI_TEST_TMPDIR}/${name}.out"
	TOOL_ERR="${HUATUO_BAMAI_TEST_TMPDIR}/${name}.err"

	[[ $EUID -eq 0 ]] || fatal "requires root (BPF requires CAP_BPF/CAP_SYS_ADMIN)"
	[[ -x ${TOOL_BIN} ]] || fatal "missing ${name} binary: ${TOOL_BIN}"
	[[ -r ${TOOL_BPF} ]] || fatal "missing ${name} bpf object: ${TOOL_BPF}"
}

# Print file to stderr with a label header; silent if file is absent.
dump_file() {
	local label=$1 path=$2
	[[ -f "${path}" ]] || return 0
	log_error "----- ${label} (${path}) -----"
	cat "${path}" >&2
}

dump_tool_logs_and_fail() {
	dump_file "OUT" "${TOOL_OUT}"
	dump_file "ERR" "${TOOL_ERR}"
	fatal "$*"
}

# SIGTERM with graceful polling, then SIGKILL as fallback.
# $1=pid  $2=timeout_seconds (default 10).
stop_by_pid() {
	local pid=$1 timeout=${2:-10}
	kill -0 "${pid}" 2> /dev/null || return 0
	kill -TERM "${pid}" 2> /dev/null || true
	local waited=0
	while kill -0 "${pid}" 2> /dev/null && [[ ${waited} -lt ${timeout} ]]; do
		sleep 1
		waited=$((waited + 1))
	done
	kill -KILL "${pid}" 2> /dev/null || true
}

# --------------------------- container detection ----------------------------

# Returns 0 when running inside a container.
# Method 1: overlay/btrfs rootfs — container runtimes mount an overlay or
# btrfs snapshot as /; bare-metal hosts use ext4/xfs/zfs.
# Method 2: systemd-detect-virt -c — explicitly checks for container
# virtualization (docker, lxc, podman, etc.).
is_container() {
	local fstype
	fstype=$(findmnt -n -o FSTYPE / 2> /dev/null || true)
	case "${fstype}" in
	overlay | btrfs) return 0 ;;
	esac

	if command -v systemd-detect-virt > /dev/null 2>&1; then
		[[ "$(systemd-detect-virt -c 2> /dev/null)" != "none" ]] && return 0
	fi

	return 1
}

# ----------------------------- huatuo-bamai ----------------------------------

huatuo_bamai_start() {
	[[ -x "${HUATUO_BAMAI_BIN}" ]] || fatal "huatuo-bamai binary not found: ${HUATUO_BAMAI_BIN}"

	log_info "starting huatuo-bamai: $*"
	"${HUATUO_BAMAI_BIN}" "$@" > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" 2>&1 &
	local pid=$!
	echo "$pid" > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	log_info "huatuo-bamai pid: ${pid}"

	sleep 0.5
	wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" "${WAIT_HUATUO_BAMAI_INTERVAL}" \
		"huatuo-bamai ready" huatuo_bamai_ready
}

huatuo_bamai_ready() {
	local pid
	pid=$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" 2> /dev/null || echo "")
	[[ -n "$pid" ]] || return 1

	if ! kill -0 "${pid}" 2> /dev/null; then
		log_error "huatuo-bamai pid=${pid} exited, last log:"
		tail -20 "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" >&2 || echo "empty"
		return 1
	fi

	curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_METRICS_API}" > /dev/null
}

huatuo_bamai_stop() {
	local exit_code=${1:-0}

	local pid
	pid=$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" 2> /dev/null || echo "")
	[[ -n "$pid" ]] && stop_by_pid "${pid}"
	rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"

	if [ "${exit_code}" -ne 0 ]; then
		log_info "the exit code: ${exit_code}"
		log_info "
========== HUATUO INTEGRATION TEST FAILED ================

Summary:
  - One or more expected metrics are missing.

Temporary artifacts preserved at:
  ${HUATUO_BAMAI_TEST_TMPDIR}

Key files:
  - metrics.txt
  - huatuo.log
  - bamai.conf

=========================================================
"
	fi
}

huatuo_bamai_metrics() {
	curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_METRICS_API}"
}

# highlight and reject error/panic keywords in the log
huatuo_bamai_log_check() {
	sed -E "s/(${HUATUO_BAMAI_MATCH_KEYWORDS})/\x1b[31m\1\x1b[0m/gI" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	! grep -qE "${HUATUO_BAMAI_MATCH_KEYWORDS}" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
}

huatuo_bamai_pod_count() {
	local regex=$1
	curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_PODS_API}" \
		| jq --arg re "$regex" '
      [ .data[]
        | select(.hostname != null)
        | select(.hostname | test($re))
      ] | length
    ' 2> /dev/null || echo 0
}

# ----------------------------- metrics helpers --------------------------------

# integration_test_start <config_writer_func>
# Writes config via the named function, sets EXIT trap, and starts huatuo-bamai.
integration_huatuo_bamai_start() {
	local config_writer=${1:-write_default_config}

	[[ -z "${HUATUO_BAMAI_ARGS_INTEGRATION:-}" ]] && eval "HUATUO_BAMAI_ARGS_INTEGRATION=(${HUATUO_BAMAI_INTEGRATION_ARGS_STR})"
	trap 'huatuo_bamai_stop $? || true' EXIT

	"$config_writer"
	huatuo_bamai_start "${HUATUO_BAMAI_ARGS_INTEGRATION[@]}"
}

# huatuo_bamai_collect_metrics saves /metrics output to the temp metrics file.
huatuo_bamai_collect_metrics() {
	huatuo_bamai_metrics > "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}

# huatuo_bamai_await_metrics waits until the metrics endpoint responds, then saves.
huatuo_bamai_await_metrics() {
	wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" \
		"${WAIT_HUATUO_BAMAI_INTERVAL}" \
		"metrics endpoint ready" \
		huatuo_bamai_collect_metrics
}

# check_metrics <desc> <present_pattern>... [-- <absent_pattern>...]
# Single-pass metric assertion: verifies present patterns exist and absent
# patterns do not, using at most 2 grep invocations regardless of pattern count.
check_metrics() {
	local desc=$1
	shift
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	local prefix="huatuo_bamai_"

	local present=() absent=()
	while [[ $# -gt 0 && "$1" != "--" ]]; do
		present+=("$1")
		shift
	done
	shift 2> /dev/null || true
	absent=("$@")

	if [[ ${#absent[@]} -gt 0 ]]; then
		local absent_re
		absent_re=$(
			IFS='|'
			echo "${absent[*]}"
		)
		local found
		found=$(grep -oE "${prefix}(${absent_re})" "$metrics_file" || true)
		[[ -z "$found" ]] || fatal "${desc}: expected absent but found: ${found}"
	fi

	if [[ ${#present[@]} -gt 0 ]]; then
		local present_re
		present_re=$(
			IFS='|'
			echo "${present[*]}"
		)
		local matches
		matches=$(grep -oE "${prefix}(${present_re})" "$metrics_file" || true)
		local pat
		for pat in "${present[@]}"; do
			echo "$matches" | grep -q "$pat" || fatal "${desc}: expected present but not found: ${pat}"
		done
	fi
}
