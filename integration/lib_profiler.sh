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

# Profiler scenarios load lib.sh before this library.
set -euo pipefail

# Native PID filters use host PIDs; container-local PIDs cannot select targets.
require_native_pid_profiling() {
	require_commands stat
	local pid_namespace_inode
	pid_namespace_inode=$(stat -Lc '%i' /proc/self/ns/pid 2> /dev/null) \
		|| skip "cannot inspect PID namespace: /proc/self/ns/pid"
	# Linux reserves PROC_PID_INIT_INO (0xeffffffc) for the initial PID
	# namespace. Comparing with /proc/1 would also accept container-local init.
	if [[ ${pid_namespace_inode} != 4026531836 ]]; then
		skip "native PID profiling requires the host PID namespace (inode=${pid_namespace_inode}); use --pid=host for containers"
	fi
}

# Keep source discovery aligned with the vendored Cilium BTF loader. Parsing
# belongs to the profiler: malformed or unreadable sources must fail the test.
kernel_btf_sources() {
	local -n sources=$1
	local release
	release=$(uname -r) || fatal "cannot determine the running kernel release"
	sources=(
		/sys/kernel/btf/vmlinux
		"/boot/vmlinux-${release}"
		"/lib/modules/${release}/vmlinux-${release}"
		"/lib/modules/${release}/build/vmlinux"
		"/usr/lib/modules/${release}/kernel/vmlinux"
		"/usr/lib/debug/boot/vmlinux-${release}"
		"/usr/lib/debug/boot/vmlinux-${release}.debug"
		"/usr/lib/debug/lib/modules/${release}/vmlinux"
	)
}

require_kernel_btf() {
	local path
	local -a candidates=()
	kernel_btf_sources candidates
	for path in "${candidates[@]}"; do
		[[ ! -e ${path} ]] || return 0
	done
	skip "kernel BTF unavailable: ${candidates[*]}"
}

require_profiler_bpf_sampling() {
	require_readable /proc/self/status
	local cap_eff
	cap_eff=$(awk '$1 == "CapEff:" { print $2 }' /proc/self/status)
	# BPF perf-event programs require both capabilities on newer kernels;
	# CAP_SYS_ADMIN provides the legacy equivalent.
	if (((16#${cap_eff} & (1 << 21)) == 0)); then
		(((16#${cap_eff} & (1 << 39)) != 0)) \
			|| skip "BPF program loading requires CAP_BPF or CAP_SYS_ADMIN"
		(((16#${cap_eff} & (1 << 38)) != 0)) \
			|| skip "BPF perf-event sampling requires CAP_PERFMON or CAP_SYS_ADMIN"
	fi
	# The profiler raises RLIMIT_MEMLOCK to infinity during BPF initialization.
	if [[ $(ulimit -Hl) != unlimited ]]; then
		(((16#${cap_eff} & (1 << 24)) != 0)) \
			|| skip "profiler BPF initialization requires CAP_SYS_RESOURCE to raise RLIMIT_MEMLOCK"
	fi
}

profiler_ready() {
	local stdout=$1
	[[ -f "${stdout}" ]] && grep -q "data reading loop started" "${stdout}"
}

# Shared CPU sampling setup; scenario choices and fault injection stay in the
# caller. One active profiler per shell: callers may read PROFILER_PID,
# PROFILER_OUTPUT_FD and the three output paths, but must not replace them.
# assert_profiler_exit drains/closes the FD and reaps/clears the PID. Callers
# register cleanup_running_jobs on EXIT for failures before that handoff.
start_cpu_profiler() {
	local out_dir=$1 language=$2 duration=$3 interval=$4
	shift 4
	mkdir -p "${out_dir}"
	PROFILER_OUT="${out_dir}/profiler.out"
	PROFILER_ERR="${out_dir}/profiler.err"
	PROFILER_STACKS="${out_dir}/stacks.json"
	: > "${PROFILER_OUT}"
	: > "${PROFILER_STACKS}"
	start_piped_process "${out_dir}/profiler" "${ROOT_DIR}/_output/bin/profiler" \
		--type cpu --language "${language}" --duration "${duration}" \
		--aggr-interval "${interval}" --freq 99 \
		--output-format json --output-path - --log-level info "$@"
	PROFILER_PID=${PROCESS_PID}
	PROFILER_OUTPUT_FD=${PROCESS_OUTPUT_FD}
	exec {PROCESS_INPUT_FD}>&-
}

# Internal reader for one line-delimited JSON record or text log at a time.
# Classify objects by their fields, not serialization order; retain raw input
# before decoding so malformed profile output remains available for diagnosis.
read_profiler_output() {
	local timeout=$1 line="" read_status=0
	IFS= read -r -t "${timeout}" -u "${PROFILER_OUTPUT_FD}" line || read_status=$?
	if ((read_status)); then
		if [[ -n ${line} ]]; then
			printf '%s' "${line}" >> "${PROFILER_OUT}"
			fatal "incomplete profiler output record: ${PROFILER_OUT}"
		fi
		return "${read_status}"
	fi
	printf '%s\n' "${line}" >> "${PROFILER_OUT}"
	if [[ ${line} =~ ^[[:space:]]*\{ ]]; then
		jq -sc '
			if length != 1 then error("expected one JSON object per line") else .[0] end
			| select(has("profile") or has("profile_type"))
			| if (.profile | type) != "object" then error("expected a profile object") else . end
			| .profile as $p
			| ($p.function // [] | map({key: (.id | tostring), value: $p.string_table[.name]}) | from_entries) as $functions
			| ($p.location // [] | map({key: (.id | tostring), value: [.line[]? | $functions[.function_id | tostring]]}) | from_entries) as $locations
			| $p.sample[]? | select(any(.value[]; tonumber > 0))
			| {pid: ([.label[]? | select($p.string_table[.key] == "pid") | $p.string_table[.str]][0]),
			   names: [.location_id[] | $locations[tostring][]]}
		' <<< "${line}" >> "${PROFILER_STACKS}" \
			|| fatal "invalid profiler output: ${PROFILER_OUT}"
	fi
}

# Internal predicate shared by immediate and streaming assertions.
profiler_has_stack() {
	local pid=$1 method=$2
	jq -se --arg pid "${pid}" --arg method "${method}" '
		any(.[]; ($pid == "*" or .pid == $pid) and any(.names[]; contains($method)))
	' "${PROFILER_STACKS}" > /dev/null
}

assert_profiler_stack() {
	profiler_has_stack "$@" || fatal "missing stack PID=$1 method=$2; see ${PROFILER_OUT}"
}

assert_profiler_samples_before_deadline() {
	local pid=$1 method=$2 deadline=$((SECONDS + 15))
	while ! profiler_has_stack "${pid}" "${method}"; do
		((SECONDS < deadline)) || fatal "timed out waiting for ${method}; see ${PROFILER_OUT}"
		read_profiler_output "$((deadline - SECONDS))" \
			|| fatal "profiler exited or timed out before ${method}; see ${PROFILER_ERR}"
	done
}

# assert_profiler_exit <expected_status> [timeout_seconds]
assert_profiler_exit() {
	local expected=$1 timeout=${2:-15} status=0 read_status
	local deadline=$((SECONDS + timeout))
	while true; do
		((SECONDS < deadline)) || fatal "profiler did not finish; see ${PROFILER_ERR}"
		read_status=0
		read_profiler_output "$((deadline - SECONDS))" || read_status=$?
		[[ ${read_status} -eq 0 ]] && continue
		[[ ${read_status} -eq 1 ]] || fatal "profiler output timed out; see ${PROFILER_ERR}"
		break
	done
	exec {PROFILER_OUTPUT_FD}<&-
	PROFILER_OUTPUT_FD=""
	# EOF can precede process exit. Use only the remaining drain budget before
	# collecting the exit status in the parent Shell.
	process_exited "${PROFILER_PID}" \
		|| wait_until "$((deadline - SECONDS))" 0.1 process_exited "${PROFILER_PID}" \
		|| fatal "profiler did not exit before deadline; see ${PROFILER_ERR}"
	wait "${PROFILER_PID}" || status=$?
	PROFILER_PID=""
	assert_eq "${status}" "${expected}" "profiler exit; see ${PROFILER_ERR}" \
		|| fatal "unexpected profiler exit status"
	! grep -q 'panic:' "${PROFILER_OUT}" "${PROFILER_ERR}" || fatal "profiler panicked"
}

# assert_collectors_exited <output_fd> <output_log> <pids_array> <start_times_array> [timeout]
# Consume and close the caller's pipe, then check process identities under one
# deadline. This asserts natural teardown; fallback cleanup stays with the caller.
assert_collectors_exited() {
	local output_fd=$1 output_log=$2 timeout=${5:-5} line status index
	local -n collector_process_ids=$3 collector_process_start_times=$4
	[[ ${#collector_process_ids[@]} == ${#collector_process_start_times[@]} ]] \
		|| fatal "collector PID and start-time counts differ"
	local deadline=$((SECONDS + timeout))
	while true; do
		((SECONDS < deadline)) || fatal "collectors did not close their output pipe; see ${output_log}"
		status=0
		IFS= read -r -t "$((deadline - SECONDS))" -u "${output_fd}" line || status=$?
		[[ ${status} -ne 1 ]] || break
		[[ ${status} -eq 0 ]] || fatal "collector still holds its output pipe; see ${output_log}"
		printf '%s\n' "${line}" >> "${output_log}"
	done
	exec {output_fd}<&-
	for index in "${!collector_process_ids[@]}"; do
		process_exited "${collector_process_ids[index]}" "${collector_process_start_times[index]}" \
			|| wait_until "$((deadline - SECONDS))" 0.1 process_exited \
				"${collector_process_ids[index]}" "${collector_process_start_times[index]}" \
			|| fatal "collector PID ${collector_process_ids[index]} did not exit before deadline"
	done
}

# Verify periodic JSON publication while the profiling command is still running.
# The command arguments supply the target and sampling mode.
assert_java_profiler_streams() (
	local out_dir=$1
	shift
	local profiler_pid=""
	mkdir -p "${out_dir}"
	trap '[[ -z "${profiler_pid}" ]] || stop_and_wait_by_pid "${profiler_pid}" 5 || true' EXIT
	"$@" --duration 12 --aggr-interval 2 --output-format json --output-path - \
		> "${out_dir}/windows.json" 2> "${out_dir}/profiler.err" &
	profiler_pid=$!

	profile_windows_are_ready() {
		jq -se '[.[] | select((.profile.sample | length) > 0)] | length >= 2' \
			"${out_dir}/windows.json" > /dev/null
	}
	wait_until 10 0.1 profile_windows_are_ready 2> /dev/null \
		|| fatal "two Java windows not published during collection (see ${out_dir}/profiler.err)"
	kill -0 "${profiler_pid}" 2> /dev/null \
		|| fatal "Java profiler exited before streaming output was observed"
	local status=0
	stop_and_wait_by_pid "${profiler_pid}" 5 INT || status=$?
	profiler_pid=""
	[[ ${status} -eq 0 ]] || fatal "Java streaming profiler exited ${status} (see ${out_dir}/profiler.err)"
	profile_windows_are_ready \
		|| fatal "expected multiple non-empty Java windows in ${out_dir}/windows.json"
)
