#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

readonly MEMORY_BUNDLE="${PROFILER_TOOL_DIR}/memray"
readonly MEMORY_FIXTURE="${ROOT_DIR}/integration/testdata/test_profiler_python_memory.py"
readonly MEMORY_FIXTURE_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/memory-fixture.log"
readonly MEMORY_FIXTURE_ERR="${HUATUO_BAMAI_TEST_TMPDIR}/memory-fixture.err"
readonly MEMORY_LIBRARY="${HUATUO_BAMAI_TEST_TMPDIR}/memory-fixture.so"

memory_require_runtime() {
	local version=$1 bundle=${2:-${MEMORY_BUNDLE}}
	# Only unsupported versions may skip; a missing supported runtime is a
	# packaging failure, not an unavailable external prerequisite.
	case "${version}" in
	3.7 | 3.8 | 3.9 | 3.10 | 3.11 | 3.12 | 3.13 | 3.14) ;;
	*) skip "Python ${version} is not supported by the pinned Memray bundle" ;;
	esac
	local runtime="${bundle}/runtimes/py${version}/python/memray"
	compgen -G "${runtime}/_memray*.so" > /dev/null \
		|| fatal "Memray runtime for Python ${version} missing: ${runtime}; run make prep-memray-bundle"
	compgen -G "${runtime}/_inject*.so" > /dev/null \
		|| fatal "Memray injector for Python ${version} missing: ${runtime}; run make prep-memray-bundle"
}

memory_require_gdb_relr_support() {
	local pid=$1 libraries library status=0
	local log_file="${HUATUO_BAMAI_TEST_TMPDIR}/memory-gdb-elf.log"
	local -a files=(-ex "file /proc/${pid}/exe")
	libraries=$(awk '$6 ~ /\/(libc[.-]|libpython[0-9]|ld-linux)/ {print $6}' "/proc/${pid}/maps" | sort -u) \
		|| fatal "cannot read Python fixture mappings for PID ${pid}"
	while IFS= read -r library; do
		[[ -n "${library}" ]] || continue
		files+=(-ex "file /proc/${pid}/root${library}")
	done <<< "${libraries}"
	# Inspect ELF files without attaching: old BFD may misread modern glibc
	# before an inferior call crashes. Do not use a distro or GDB version cutoff.
	LC_ALL=C DEBUGINFOD_URLS= timeout --kill-after=1 5 gdb -nx -nh -batch \
		-iex 'set auto-load off' -ex "set sysroot /proc/${pid}/root" \
		"${files[@]}" > "${log_file}" 2>&1 || status=$?
	if [[ "${status}" -le 1 ]] \
		&& grep -Eq 'unknown type \[0x13\] section .*\.relr\.dyn' "${log_file}"; then
		skip "host GDB/BFD cannot read the Python target's .relr.dyn; use a newer debugger (see ${log_file})"
	fi
	[[ "${status}" == 0 ]] \
		|| fatal "GDB ELF compatibility check failed (exit ${status}): $(cat "${log_file}")"
}

# Node names can differ from the runner's isolated UTS hostname.
memory_node_for_local_addresses() {
	local nodes_file=$1 addresses_file=$2
	jq -er --slurpfile addresses "${addresses_file}" '
		[$addresses[0][] | .addr_info[]? | .local] as $local_addresses
		| [.items[] | select(any(.status.addresses[]?;
			.type == "InternalIP" and
			(.address as $ip | $local_addresses | index($ip) != null)))
			| .metadata.name]
		| if length == 1 then .[0]
		  else error("expected exactly one Kubernetes node matching local interface addresses") end
	' "${nodes_file}"
}

memory_compile_fixture() {
	compile_user_fixture \
		"${ROOT_DIR}/integration/testdata/test_profiler_python_memory.user.c" \
		"${MEMORY_LIBRARY}" -shared -fPIC
}

memory_fixture_has_ack() {
	[[ "${MEMORY_TARGET_PID}" =~ ^[1-9][0-9]*$ ]] \
		|| fatal "invalid Python fixture PID: ${MEMORY_TARGET_PID}"
	kill -0 "${MEMORY_TARGET_PID}" 2> /dev/null \
		|| fatal "Python fixture exited: $(cat "${MEMORY_FIXTURE_ERR}")"
	grep -qx "$1" "${MEMORY_FIXTURE_LOG}"
}

memory_wait_for_ack() {
	wait_until 10 0.1 memory_fixture_has_ack "$1" \
		|| fatal "missing fixture acknowledgement '$1': $(cat "${MEMORY_FIXTURE_LOG}" "${MEMORY_FIXTURE_ERR}")"
}

memory_request_phase() {
	kill -USR1 "${MEMORY_TARGET_PID}"
	memory_wait_for_ack "$1"
}

memory_delivered_bytes() {
	local log_file=$1 field=$2
	awk -v pid="${MEMORY_TARGET_PID}" -v field="${field}" '
		index($0, "memray deltas delivered pid=" pid " ") && match($0, field "=[0-9]+") {
			value = substr($0, RSTART, RLENGTH)
			sub(/^[^=]*=/, "", value)
			total += value
		}
		END { printf "%.0f\n", total }
	' "${log_file}"
}

memory_delta_delivery_ready() {
	local collector_pid=$1 log_file=$2 field=$3 expected_total=$4 actual_total
	kill -0 "${collector_pid}" 2> /dev/null \
		|| fatal "profiler exited before delta delivery: $(cat "${log_file}")"
	actual_total=$(memory_delivered_bytes "${log_file}" "${field}")
	[[ "${actual_total}" -ge "${expected_total}" ]]
}

assert_memory_delta_delivery() {
	local collector_pid=$1 log_file=$2 field=$3 expected_total=$4
	wait_until 5 0.1 memory_delta_delivery_ready "${collector_pid}" "${log_file}" "${field}" "${expected_total}" \
		|| fatal "${field} did not reach ${expected_total}: $(cat "${log_file}")"
}

assert_memory_log_contains() {
	local log_file=$1 pattern=$2
	grep -qE -- "${pattern}" "${log_file}" \
		|| fatal "missing '${pattern}' in ${log_file}: $(cat "${log_file}")"
}

assert_memory_duplicate_rejected() {
	local status=$1 log_file=$2
	[[ "${status}" -ne 0 && "${status}" -ne 124 ]] \
		|| fatal "duplicate profiler was not rejected promptly: $(cat "${log_file}")"
	assert_memory_log_contains "${log_file}" 'Memray session already active'
}

assert_memory_cancel_path() {
	[[ "$1" == /tmp/.huatuo-memray-cancel-"${MEMORY_TARGET_PID}"-* ]] \
		|| fatal "unexpected cancellation path: $1"
}

assert_memory_no_profile() {
	[[ ! -s "$1" ]] || fatal "canceled startup published a profile: $1"
}

assert_memory_capture_succeeded() {
	local collector_pid=$1 log_file=$2
	wait "${collector_pid}" || fatal "profiler failed: $(cat "${log_file}")"
}

assert_memory_startup_cancelled() {
	local collector_pid=$1 log_file=$2 profile_path=$3
	if wait "${collector_pid}"; then
		fatal "startup cancellation reported a successful capture"
	fi
	assert_memory_log_contains "${log_file}" 'collect Memray session'
	assert_memory_no_profile "${profile_path}"
}

assert_memory_no_tracking_errors() {
	local status=0
	grep -qE 'Traceback|Failed to write output' "${MEMORY_FIXTURE_ERR}" || status=$?
	assert_eq "${status}" 1 "target tracking log must contain no failures" \
		|| fatal "target tracking failed: $(cat "${MEMORY_FIXTURE_ERR}")"
}

assert_memory_import_paths_restored() {
	jq -Rse '
		split("\n") | [.[] | fromjson? | select(.event == "import-path")] as $states |
		[$states[] | select(.phase == "ready")] as $ready |
		[$states[] | select(.phase == "tracking")] as $tracking |
		[$states[] | select(.phase == "deactivated")] as $stopped |
		($ready | length) == 1 and
		($tracking | length) > 0 and
		($tracking | length) == ($stopped | length) and
		all($states[]; .paths == $ready[0].paths)
	' "${MEMORY_FIXTURE_LOG}" > /dev/null \
		|| fatal "Memray changed the target import path: $(cat "${MEMORY_FIXTURE_LOG}")"
}

assert_memory_separate_runtime_layout() {
	local bin_dir=$1 tool_dir=$2
	[[ ! -e "${bin_dir}/../tools/memray" ]] \
		|| fatal "separate profiler unexpectedly has a default runtime bundle"
	[[ -d "${tool_dir}/memray" ]] || fatal "configured runtime bundle is missing"
}

assert_memory_distinct_jobs() {
	[[ "$1" != "$2" ]] || fatal "duplicate request reused the first job ID"
}
