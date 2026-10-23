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

# Bash does not export arrays; every test subprocess needs its own defaults.
if ! declare -p CURL_TIMEOUT > /dev/null 2>&1; then
	CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
fi

# --------------------------------- log --------------------------------------

TEST_LOG_TAG=${TEST_LOG_TAG:-INTEGRATION}

log_info() {
	printf '[%s][%s] %s\n' \
		"$(TZ=UTC-8 date '+%Y-%m-%dT%H:%M:%S+08:00')" "${TEST_LOG_TAG}" "$*"
}
log_warn() {
	printf '[%s][%s][WARN] %s\n' \
		"$(TZ=UTC-8 date '+%Y-%m-%dT%H:%M:%S+08:00')" "${TEST_LOG_TAG}" "$*" >&2
}
log_error() {
	printf '[%s][%s][ERROR] %s\n' \
		"$(TZ=UTC-8 date '+%Y-%m-%dT%H:%M:%S+08:00')" "${TEST_LOG_TAG}" "$*" >&2
}

# Keep phase timings separate so the runner can show them on successful cases.
record_test_timing() {
	printf '[TIMING] case=%s subject=%s phase=%s duration=%ss\n' \
		"${HUATUO_BAMAI_TEST_TMPDIR##*/}" "$1" "$2" "$((SECONDS - $3))" \
		>> "${HUATUO_BAMAI_TEST_TMPDIR}/timings.log"
}

fatal() {
	printf '[%s][%s][FAIL] %s\n' \
		"$(TZ=UTC-8 date '+%Y-%m-%dT%H:%M:%S+08:00')" "${TEST_LOG_TAG}" "$*" >&2
	exit 1
}

# The runner reserves 77 for skipped cases; cleanup still runs through EXIT.
skip() {
	printf '[%s][%s][SKIP] ⏭️ %s\n' \
		"$(TZ=UTC-8 date '+%Y-%m-%dT%H:%M:%S+08:00')" "${TEST_LOG_TAG}" "$*"
	exit 77
}

test_results_init() {
	test_passed=0
	test_skipped=0
	test_failed=0
}

test_result_record() {
	local name=$1 status=$2
	case ${status} in
	0)
		test_passed=$((test_passed + 1))
		log_info "PASS: ${name}"
		;;
	77)
		test_skipped=$((test_skipped + 1))
		log_info "SKIP: ${name}"
		;;
	*)
		test_failed=$((test_failed + 1))
		log_error "FAIL: ${name} (exit ${status})"
		;;
	esac
}

test_results_summary() {
	log_info "summary: total=$((test_passed + test_skipped + test_failed)) passed=${test_passed} skipped=${test_skipped} failed=${test_failed}"
}

report_http_response() {
	local label=$1 response_file=$2 error_file=$3
	if [[ -r "${response_file}" ]]; then
		log_info "${label} response: $(< "${response_file}")"
	else
		log_error "${label} response file missing: ${response_file}"
	fi
	if [[ -s "${error_file}" ]]; then
		log_error "${label} curl error: $(< "${error_file}")"
	fi
}

# --------------------------------- utils ------------------------------------

require_commands() {
	local command
	for command in "$@"; do
		if [[ ${command} == */* ]]; then
			[[ -f "${command}" && -x "${command}" ]] || skip "command is not executable: ${command}"
		else
			command -v "${command}" > /dev/null 2>&1 || skip "command is not installed: ${command}"
		fi
	done
}

require_readable() {
	local path
	for path in "$@"; do
		[[ -r "${path}" ]] || skip "file is not readable: ${path}"
	done
}

require_toml_parser() {
	require_commands python3 jq
	python3 -c 'import importlib.util; __import__("tomllib" if importlib.util.find_spec("tomllib") else "tomli")' \
		> /dev/null 2>&1 || skip "TOML parsing requires Python 3.11+ or the Python tomli module"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}/tools"
	cp "${ROOT_DIR}/integration/testdata/tools/test_toml_to_json.py" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/tools/toml-to-json.py"
}

assert_toml_values() {
	local config_file=$1 expected=$2
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/$(basename "${config_file}").json"
	local error_file="${response_file}.error"
	python3 "${HUATUO_BAMAI_TEST_TMPDIR}/tools/toml-to-json.py" "${config_file}" \
		> "${response_file}" 2> "${error_file}" \
		|| fatal "cannot parse TOML ${config_file}: $(< "${error_file}")"
	jq -e --argjson expected "${expected}" '
		. as $actual | all($expected | to_entries[];
			. as $entry | ($actual | getpath($entry.key | split("."))) == $entry.value)
	' "${response_file}" > /dev/null \
		|| fatal "${config_file}: expected ${expected}; actual $(< "${response_file}")"
}

require_build_output() {
	[[ -d "${ROOT_DIR}/_output" ]] || fatal "build output directory missing: ${ROOT_DIR}/_output; run make build"
}

assert_eq() {
	local actual=$1 expect=$2 msg=${3:-""}
	[[ "$actual" == "$expect" ]] && return 0
	log_info "assert_eq: ${msg} actual=${actual}, expect=${expect}"
	return 1
}

assert_readiness_endpoint() {
	local body="${HUATUO_BAMAI_TEST_TMPDIR}/readyz.body"
	local error_file="${HUATUO_BAMAI_TEST_TMPDIR}/readyz.curl-error"
	local curl_status=0 status
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${body}" -w '%{http_code}' \
		"${APISERVER_ADDR}/readyz" 2> "${error_file}") || curl_status=$?
	log_info "GET /readyz: curl exit=${curl_status}, HTTP status=${status}"
	report_http_response "GET /readyz" "${body}" "${error_file}"
	[[ ${curl_status} -eq 0 ]] || fatal "GET /readyz: curl exited ${curl_status}"
	assert_eq "${status}" "204" "GET /readyz status" \
		|| fatal "GET /readyz returned status ${status}, expected 204"
	if [[ -s "${body}" ]]; then
		fatal "GET /readyz returned a non-empty response body"
	fi
}

# assert_process_alive <pid> <start_time> <description>
assert_process_alive() {
	local pid=$1 start_time=$2 description=$3
	! process_exited "${pid}" "${start_time}" \
		|| fatal "${description}: PID ${pid} exited or its identity changed"
}

assert_log_has_no_failure() {
	local log_file=$1 component=$2 status=0
	local failure_pattern='(^|[[:space:]])panic:|(^|[[:space:]])level=(error|panic|fatal)([[:space:]]|$)|(^|[[:space:]])level="(error|panic|fatal)"([[:space:]]|$)|(^|[,{])[[:space:]]*"level"[[:space:]]*:[[:space:]]*"(error|panic|fatal)"'

	if [[ ! -f "${log_file}" || ! -r "${log_file}" ]]; then
		log_error "${component} log is not a readable file: ${log_file}"
		return 1
	fi
	grep -niE "${failure_pattern}" "${log_file}" >&2 || status=$?
	case ${status} in
	1) return 0 ;;
	0) log_error "${component} log contains an unexpected failure: ${log_file}" ;;
	*) log_error "cannot scan ${component} log ${log_file}: grep exited ${status}" ;;
	esac
	return 1
}

allocate_available_port() {
	local protocol=${1:-tcp} attempt port sockets
	[[ ${protocol} == tcp || ${protocol} == udp ]] \
		|| fatal "unsupported port protocol: ${protocol}; expected tcp or udp"
	if [[ -n ${INTEGRATION_PORT_DIR:-} ]]; then
		mkdir -p "${INTEGRATION_PORT_DIR}"
		allocate_worker_port "${protocol}" 9> "${INTEGRATION_PORT_DIR}/lock"
		return
	fi
	sockets=$(ss -H -an "--${protocol}") || return 1
	for ((attempt = 0; attempt < 20; attempt++)); do
		port=$((20000 + RANDOM % 20001))
		if ! awk '{ print $4 }' <<< "${sockets}" | grep -Eq "[:.]${port}$"; then
			echo "${port}"
			return 0
		fi
	done
	return 1
}

# The counter reserves ports across command substitutions and test subprocesses.
allocate_worker_port() {
	local protocol=$1 port=${INTEGRATION_PORT_BASE:?worker port base is missing} sockets
	flock 9
	sockets=$(ss -H -an "--${protocol}") || return 1
	if [[ -f ${INTEGRATION_PORT_DIR}/next ]]; then read -r port < "${INTEGRATION_PORT_DIR}/next"; fi
	while ((port < INTEGRATION_PORT_BASE + 1000)); do
		printf '%s\n' "$((port + 1))" > "${INTEGRATION_PORT_DIR}/next"
		if ! awk '{ print $4 }' <<< "${sockets}" | grep -Eq "[:.]${port}$"; then
			echo "${port}"
			return 0
		fi
		port=$((port + 1))
	done
	log_error "worker port range is exhausted: ${INTEGRATION_PORT_BASE}"
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

# wait_until <timeout> <interval> <func> [args...]
# Returns 0 on success, 1 on timeout.
wait_until() {
	local timeout=$1 interval=$2
	shift 2
	local func=$1
	shift

	if ! type -t "$func" > /dev/null 2>&1; then
		log_error "wait_until expects function or command: [${func}]"
		return 1
	fi

	local invocation="${func}"
	if (($# > 0)); then
		invocation+=" $*"
	fi
	local start end now elapsed
	start=$(date +%s)
	end=$((start + timeout))
	local attempt=0

	while true; do
		now=$(date +%s)
		((now < end)) || break
		attempt=$((attempt + 1))
		elapsed=$((now - start))
		log_info "wait attempt #${attempt} (${elapsed}s/${timeout}s): [${invocation}]"
		if "$func" "$@"; then
			return 0
		fi
		sleep "$interval"
	done

	log_error "wait_until timeout: func/cmd: [${invocation}]"
	return 1
}

profiler_ready() {
	local stdout=$1
	[[ -f "${stdout}" ]] && grep -q "data reading loop started" "${stdout}"
}

# Expose the real PID, start time and pipes to the calling Shell test. A
# temporary FIFO writer keeps a fast child exit from blocking the parent's open.
# Callers own cleanup and close the returned descriptors when done.
start_piped_process() {
	local prefix=$1 gate_fd ready child_pid child_start_time
	shift
	mkfifo "${prefix}.stdin" "${prefix}.stdout"
	exec {PROCESS_INPUT_FD}<> "${prefix}.stdin"
	exec {gate_fd}<> "${prefix}.stdout"
	(
		exec {gate_fd}>&-
		exec {PROCESS_INPUT_FD}>&-
		# Record identity before exec so even a fast exit cannot race PID reuse.
		child_pid=${BASHPID}
		child_start_time=$(process_start_time "${child_pid}") || exit 1
		printf 'pipe-ready %s\n' "${child_start_time}"
		exec setsid "$@"
	) < "${prefix}.stdin" > "${prefix}.stdout" 2> "${prefix}.err" &
	PROCESS_PID=$!
	exec {PROCESS_OUTPUT_FD}< "${prefix}.stdout"
	# Keep the gate until the child owns stdout, avoiding a transient EOF if
	# the parent reaches read before the child has opened its redirections.
	IFS=' ' read -r -t 15 -u "${PROCESS_OUTPUT_FD}" ready PROCESS_START_TIME \
		&& [[ ${ready} == pipe-ready && ${PROCESS_START_TIME} =~ ^[0-9]+$ ]] \
		|| fatal "process pipe did not open: ${prefix}"
	exec {gate_fd}>&-
}

assert_process_message() {
	local output_fd=$1 output_log=$2 expected=$3 timeout=${4:-15}
	local line remaining deadline=$((SECONDS + timeout))
	while ((SECONDS < deadline)); do
		remaining=$((deadline - SECONDS))
		IFS= read -r -t "${remaining}" -u "${output_fd}" line \
			|| fatal "process exited or timed out before '${expected}'; see ${output_log}"
		printf '%s\n' "${line}" >> "${output_log}"
		[[ ${line} == *"${expected}"* ]] && return 0
	done
	fatal "timed out waiting for '${expected}'; see ${output_log}"
}

# Only live jobs from this Shell are eligible for teardown. Reaped PIDs may
# already have been reused; do not keep a global registry of old process IDs.
cleanup_piped_processes() {
	local pid
	while IFS= read -r pid; do
		kill -KILL -- "-${pid}" 2> /dev/null || kill -KILL "${pid}" 2> /dev/null || true
		wait "${pid}" 2> /dev/null || true
	done < <(jobs -pr)
}

# Shared by Java, Python and container exception tests; scenario choices and
# fault injection stay in the caller. Only one profiler runs at a time.
start_exception_profiler() {
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

# Decode only complete profile records, keeping stack names and PID labels
# together. Invalid profile JSON must fail, rather than look like no samples.
read_profiler_output() {
	local timeout=$1 line
	IFS= read -r -t "${timeout}" -u "${PROFILER_OUTPUT_FD}" line || return $?
	printf '%s\n' "${line}" >> "${PROFILER_OUT}"
	case ${line} in
	'{"profile_type":'* | '{"container_id":'*)
		jq -c '
			.profile as $p
			| ($p.function // [] | map({key: (.id | tostring), value: $p.string_table[.name]}) | from_entries) as $functions
			| ($p.location // [] | map({key: (.id | tostring), value: [.line[]? | $functions[.function_id | tostring]]}) | from_entries) as $locations
			| $p.sample[]? | select(any(.value[]; tonumber > 0))
			| {pid: ([.label[]? | select($p.string_table[.key] == "pid") | $p.string_table[.str]][0]),
			   names: [.location_id[] | $locations[tostring][]]}
		' <<< "${line}" >> "${PROFILER_STACKS}" \
			|| fatal "invalid profiler output: ${PROFILER_OUT}"
		;;
	esac
}

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

# The FIFO and every recorded collector must finish within one deadline.
# Call before fallback cleanup so a leaked collector fails the scenario.
assert_collectors_exited() {
	local timeout=${1:-5} line status index
	local deadline=$((SECONDS + timeout))
	while true; do
		((SECONDS < deadline)) || fatal "py-spy collectors did not close their output pipe"
		status=0
		IFS= read -r -t "$((deadline - SECONDS))" -u "${collector_fd}" line || status=$?
		[[ ${status} -ne 1 ]] || break
		[[ ${status} -eq 0 ]] || fatal "py-spy collector still holds its output pipe"
		printf '%s\n' "${line}" >> "${case_dir}/collectors.out"
	done
	exec {collector_fd}<&-
	for index in "${!collector_pids[@]}"; do
		process_exited "${collector_pids[index]}" "${collector_start_times[index]}" \
			|| wait_until "$((deadline - SECONDS))" 0.1 process_exited \
				"${collector_pids[index]}" "${collector_start_times[index]}" \
			|| fatal "collector PID ${collector_pids[index]} did not exit before deadline"
	done
}

kprobe_available() {
	local symbol=$1
	local file
	local files=(
		"/sys/kernel/tracing/available_filter_functions"
		"/sys/kernel/debug/tracing/available_filter_functions"
	)

	for file in "${files[@]}"; do
		[[ -r "${file}" ]] || continue
		awk -v sym="${symbol}" '$1 == sym { found = 1; exit } END { exit !found }' "${file}" && return 0
	done

	return 1
}

# Tracefs may be mounted independently or exposed through debugfs.
tracepoint_available() {
	local group=$1 name=$2 root

	for root in \
		/sys/kernel/tracing \
		/sys/kernel/debug/tracing; do
		[[ -e "${root}/events/${group}/${name}/id" ]] && return 0
	done

	return 1
}

# compile_user_fixture <source> <output> [compiler flags...]
# Keep stack frames observable so profiler fixtures produce stable call chains.
compile_user_fixture() {
	require_commands gcc
	local source=$1
	local output=$2
	shift 2
	local compile_log="${output}.compile.log"

	log_info "compiling fixture: $(basename "${source}")"
	gcc -O0 -g -Wall -Wextra -fno-inline -fno-omit-frame-pointer "$@" \
		-o "${output}" "${source}" \
		2> "${compile_log}" \
		|| fatal "gcc failed compiling ${source}:"$'\n'"$(< "${compile_log}")"
}

# compile_bpf_fixture <source> <output> [extra_cflags]
compile_bpf_fixture() {
	require_commands clang "${ROOT_DIR}/build/clang.sh"
	local source=$1
	local output=$2
	local extra_cflags=${3:-}
	local compile_log="${output}.compile.log"

	log_info "compiling BPF fixture: $(basename "${source}")"
	BPF_EXTRA_CFLAGS="${extra_cflags}" "${ROOT_DIR}/build/clang.sh" \
		-s "${source}" -o "${output}" -I "${ROOT_DIR}/bpf/include" \
		> "${compile_log}" 2>&1 \
		|| fatal "clang.sh failed compiling ${source}:"$'\n'"$(< "${compile_log}")"
}

# ------------------------- bpf tool test scaffolding -------------------------

# bpf_tool_setup <binary-name> [bpf-name] [work-prefix]
bpf_tool_setup() {
	[[ $# -ge 1 ]] || fatal "bpf_tool_setup requires a binary name"

	local binary_name=$1
	local bpf_name=${2:-${binary_name}}
	local work_prefix=${3:-${binary_name}}
	TOOL_BIN="${ROOT_DIR}/_output/bin/${binary_name}"
	TOOL_BPF="${ROOT_DIR}/_output/bpf/${bpf_name}.o"

	TOOL_WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/${work_prefix}.XXXXXX")
	TOOL_OUT="${TOOL_WORK_DIR}/${binary_name}.out"
	TOOL_ERR="${TOOL_WORK_DIR}/${binary_name}.err"
}

# Print non-empty text files; empty and binary files add no useful diagnostics.
dump_text_files() {
	local dir=$1
	local file

	[[ -d "${dir}" ]] || return 0

	while IFS= read -r -d '' file; do
		grep -Iq '' "${file}" || continue
		log_error "----- FILE (${file}) -----"
		if [[ ${file##*/} == test.log ]]; then cat "${file}" >&2; else sed -n '1,160p' "${file}" >&2; fi
	done < <(find "${dir}" -type f -size +0c -print0)
}

# Call from the child's parent shell so wait can verify its exit status.
# Unlike cleanup, a shutdown assertion must not hide a timeout with SIGKILL.
assert_process_shutdown_by_pid() {
	local pid=${1:-} timeout=${2:-10} status=0
	if [[ ! ${pid} =~ ^[1-9][0-9]*$ || ! ${timeout} =~ ^[1-9][0-9]*$ ]]; then
		log_error "invalid stop arguments: pid=${pid}, timeout=${timeout}; expected positive integers"
		return 1
	fi

	if ! kill -TERM "${pid}" 2> /dev/null; then
		log_error "failed to send SIGTERM to pid=${pid}"
		return 1
	fi
	if ! wait_until "${timeout}" 0.1 process_exited "${pid}"; then
		log_error "pid=${pid} did not exit within ${timeout}s after SIGTERM"
		return 1
	fi

	wait "${pid}" || status=$?
	if [[ ${status} -ne 0 ]]; then
		log_error "pid=${pid} exited with status ${status} after SIGTERM"
	fi
	return "${status}"
}

# Signal with graceful polling, then SIGKILL as fallback.
# $1=pid  $2=timeout_seconds (default 10)  $3=signal (default TERM).
stop_by_pid() {
	local pid=$1 timeout=${2:-10} signal=${3:-TERM}
	kill -0 "${pid}" 2> /dev/null || return 0
	kill -"${signal}" "${pid}" 2> /dev/null || true
	local waited=0
	while kill -0 "${pid}" 2> /dev/null && ((waited < timeout * 10)); do
		sleep 0.1
		waited=$((waited + 1))
	done
	kill -KILL "${pid}" 2> /dev/null || true
}

# Sampling tools start separate process groups that can outlive their daemon.
stop_by_process_group() {
	local pgid=$1 timeout=${2:-5} waited=0
	[[ ${pgid} =~ ^[0-9]+$ && ${pgid} -gt 1 ]] \
		|| fatal "invalid process group ID: ${pgid}"
	kill -TERM -- "-${pgid}" 2> /dev/null || return 0
	while kill -0 -- "-${pgid}" 2> /dev/null && ((waited < timeout)); do
		sleep 1
		waited=$((waited + 1))
	done
	kill -KILL -- "-${pgid}" 2> /dev/null || true
}

stop_and_wait_by_pid() {
	local pid=${1:-} timeout=${2:-10} signal=${3:-TERM}
	[[ -n "${pid}" ]] || return 0
	stop_by_pid "${pid}" "${timeout}" "${signal}"
	wait "${pid}"
}

# ----------------------------- huatuo-bamai ----------------------------------

huatuo_bamai_start() {
	[[ -f "${HUATUO_BAMAI_BIN}" && -x "${HUATUO_BAMAI_BIN}" ]] \
		|| fatal "huatuo-bamai binary missing or not executable: ${HUATUO_BAMAI_BIN}; run make build"
	log_info "starting huatuo-bamai: $*"
	"${HUATUO_BAMAI_BIN}" "$@" > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" 2>&1 &
	local pid=$!
	echo "$pid" > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	log_info "huatuo-bamai pid: ${pid}"

	wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" "${WAIT_HUATUO_BAMAI_INTERVAL}" \
		huatuo_bamai_ready
}

huatuo_bamai_ready() {
	local pid
	pid=$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" 2> /dev/null || echo "")
	[[ -n "$pid" ]] || return 1

	if ! kill -0 "${pid}" 2> /dev/null; then
		log_error "huatuo-bamai pid=${pid} exited"
		return 1
	fi

	curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_METRICS_API}" > /dev/null
}

huatuo_bamai_stop() {
	local test_workspace=${1:-${HUATUO_BAMAI_TEST_TMPDIR}}
	local pid
	pid=$(cat "${test_workspace}/huatuo-bamai.pid" 2> /dev/null || echo "")
	[[ -n "$pid" ]] && stop_by_pid "${pid}"
	rm -f "${test_workspace}/huatuo-bamai.pid"
}

# Call in the shell that started the daemon so wait returns its exit status.
assert_huatuo_bamai_shutdown() {
	local pid_file="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	local pid status=0
	[[ -r "${pid_file}" ]] || fatal "huatuo-bamai PID file is not readable: ${pid_file}"
	pid=$(< "${pid_file}")
	[[ -n "${pid}" ]] || fatal "huatuo-bamai PID file is empty: ${pid_file}"
	huatuo_bamai_stop || fatal "failed to stop huatuo-bamai pid=${pid}"
	wait "${pid}" || status=$?
	assert_eq "${status}" 0 "huatuo-bamai pid=${pid} shutdown exit status" \
		|| fatal "huatuo-bamai pid=${pid} did not exit successfully: status=${status}"
}

# Field 22 identifies a process across exec and PID reuse. Strip comm first:
# its parenthesized value may contain spaces or closing parentheses.
process_start_time() {
	local stat
	local -a fields
	IFS= read -r stat < "/proc/$1/stat" || return 1
	read -r -a fields <<< "${stat##*) }"
	printf '%s\n' "${fields[19]}"
}

# process_exited <pid> [start_time]
# Read identity and state together; a replacement PID is not the original child.
process_exited() {
	local stat
	local -a fields
	IFS= read -r stat 2> /dev/null < "/proc/$1/stat" || return 0
	read -r -a fields <<< "${stat##*) }"
	[[ ${fields[0]} == Z || ${fields[0]} == X || ($# -ge 2 && ${fields[19]} != "$2") ]]
}

# Call in the shell that started the daemon to verify its actual exit status.
# assert_daemon_shutdown_with_signal <pid_file> <TERM|KILL> [sent] [timeout_seconds]
assert_daemon_shutdown_with_signal() {
	local pid_file=$1 signal=$2 already_sent=${3:-} timeout=${4:-65}
	local pid status=0 expected
	case ${signal} in
	TERM) expected=0 ;;
	KILL) expected=137 ;;
	*) fatal "unsupported daemon stop signal: ${signal}" ;;
	esac
	[[ -r ${pid_file} ]] || fatal "daemon PID file is not readable: ${pid_file}"
	pid=$(< "${pid_file}")
	[[ ${pid} =~ ^[0-9]+$ && ${pid} -gt 1 ]] || fatal "invalid daemon PID in ${pid_file}: ${pid}"
	[[ ${already_sent} == sent ]] || kill -"${signal}" "${pid}"
	if ! wait_until "${timeout}" 0.1 process_exited "${pid}"; then
		kill -KILL "${pid}" 2> /dev/null || true
		wait "${pid}" 2> /dev/null || true
		rm -f "${pid_file}"
		fatal "daemon pid=${pid} did not stop after ${signal} within ${timeout} seconds"
	fi
	wait "${pid}" || status=$?
	rm -f "${pid_file}"
	[[ ${status} == "${expected}" ]] || fatal "daemon pid=${pid} ${signal} exit status ${status}, expected ${expected}"
}

# assert_huatuo_bamai_shutdown_with_signal <TERM|KILL> [sent]
# Lifecycle tests require the real exit status instead of best-effort teardown.
assert_huatuo_bamai_shutdown_with_signal() {
	assert_daemon_shutdown_with_signal "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" "$@"
}

# --------------------------- huatuo-apiserver -------------------------------

huatuo_apiserver_start() {
	[[ -f "${HUATUO_APISERVER_BIN}" && -x "${HUATUO_APISERVER_BIN}" ]] \
		|| fatal "huatuo-apiserver binary missing or not executable: ${HUATUO_APISERVER_BIN}; run make build"
	log_info "starting huatuo-apiserver: $*"
	"${HUATUO_APISERVER_BIN}" "$@" > "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" 2>&1 &
	local pid=$!
	echo "$pid" > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-apiserver.pid"
	log_info "huatuo-apiserver pid: ${pid}"

	wait_until "${WAIT_HUATUO_APISERVER_TIMEOUT}" "${WAIT_HUATUO_APISERVER_INTERVAL}" \
		huatuo_apiserver_ready
}

huatuo_apiserver_ready() {
	local pid
	pid=$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-apiserver.pid" 2> /dev/null || echo "")
	[[ -n "$pid" ]] || return 1

	if ! kill -0 "${pid}" 2> /dev/null; then
		log_error "huatuo-apiserver pid=${pid} exited"
		return 1
	fi

	curl -sf "${CURL_TIMEOUT[@]}" "${APISERVER_ADDR}/readyz" > /dev/null
}

huatuo_apiserver_stop() {
	local test_workspace=${1:-${HUATUO_BAMAI_TEST_TMPDIR}}
	local pid
	pid=$(cat "${test_workspace}/huatuo-apiserver.pid" 2> /dev/null || echo "")
	[[ -n "$pid" ]] && stop_by_pid "${pid}"
	rm -f "${test_workspace}/huatuo-apiserver.pid"
}

# Recovery checks must finish before the still-running sampling task ends.
assert_huatuo_apiserver_shutdown_with_signal() {
	assert_daemon_shutdown_with_signal "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-apiserver.pid" "$1" "${2:-}" 20
}

# integration_huatuo_apiserver_start [config_writer_func] [apiserver args...]
# Materialize writer stdout in the workspace; ":" preserves a prepared config.
integration_huatuo_apiserver_start() {
	local config_writer=${1:-write_config_apiserver_base}
	if [[ $# -gt 0 ]]; then
		shift
	fi
	local runtime_args=(
		"--config-dir" "${HUATUO_BAMAI_TEST_TMPDIR}"
		"--config" "apiserver.conf"
	)
	runtime_args+=("$@")

	if [[ $config_writer != : ]]; then
		"$config_writer" > "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf"
	fi
	huatuo_apiserver_start "${runtime_args[@]}"
}

# Stop shared services, then remove or report the runner-owned test workspace.
integration_test_exit() {
	local exit_code=$1
	local test_workspace=$2

	if [[ -z "${test_workspace}" || "${test_workspace}" != "${HUATUO_BAMAI_TEST_TMPDIR}/"* ]]; then
		log_error "refusing to finalize test workspace outside ${HUATUO_BAMAI_TEST_TMPDIR}: ${test_workspace}"
		return 1
	fi

	local cleanup_status=0
	huatuo_apiserver_stop "${test_workspace}" || cleanup_status=1
	huatuo_bamai_stop "${test_workspace}" || cleanup_status=1
	[[ ${cleanup_status} -eq 0 ]] || exit_code=1

	if [[ ${exit_code} -eq 0 || ${exit_code} -eq 77 ]]; then
		rm -rf -- "${test_workspace}" || return 1
		return 0
	fi

	dump_text_files "${test_workspace}" || true
	log_error "integration test failed with exit code ${exit_code}; artifacts preserved at ${test_workspace}"
	return "${cleanup_status}"
}

huatuo_bamai_metrics() {
	local status curl_status=0
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.response"
	local error_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.curl-error"
	: > "${response_file}"
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
		"${HUATUO_BAMAI_METRICS_API}" 2> "${error_file}") || curl_status=$?
	printf 'curl exit=%s HTTP status=%s\n' "${curl_status}" "${status:-unavailable}" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.status"
	if [[ ${curl_status} -ne 0 || ${status} != 200 ]]; then
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.status" >&2
		report_http_response "GET /metrics" "${response_file}" "${error_file}" >&2
		return 1
	fi
	cat "${response_file}"
}

# Reject error/panic/fatal levels and runtime panics in the log.
assert_huatuo_bamai_log_clean() {
	assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" huatuo-bamai
}

# ----------------------------- metrics helpers --------------------------------

# integration_huatuo_bamai_start [config_writer_func] [huatuo-bamai args...]
# Materialize writer stdout in the workspace; ":" preserves a prepared config.
integration_huatuo_bamai_start() {
	local config_writer=${1:-write_config_bamai_default}
	if [[ $# -gt 0 ]]; then
		shift
	fi
	local runtime_args=(
		"--config-dir" "${HUATUO_BAMAI_TEST_TMPDIR}"
		"--config" "bamai.conf"
	)

	if [[ $# -gt 0 ]]; then
		runtime_args+=("$@")
	else
		runtime_args+=(
			"--region" "dev"
			"--procfs-prefix" "${HUATUO_BAMAI_TEST_FIXTURES}"
			"--disable-storage"
			"--disable-kubelet"
			"--log-debug"
		)
	fi

	if [[ $config_writer != : ]]; then
		"$config_writer" > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	fi
	configure_bamai_address
	huatuo_bamai_start "${runtime_args[@]}"
}

# Explicit addresses are already chosen by API and container config writers.
configure_bamai_address() {
	local config="${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" port
	if awk '
		/^[[:space:]]*\[HTTPServer\][[:space:]]*$/ { http = 1; next }
		/^[[:space:]]*\[/ { http = 0 }
		http && /^[[:space:]]*ListenAddress[[:space:]]*=/ { found = 1 }
		END { exit !found }
	' "${config}"; then return 0; fi
	if [[ -n ${INTEGRATION_PORT_DIR:-} ]]; then
		port=${HUATUO_BAMAI_ADDR##*:}
	else
		port=$(allocate_available_port) || fatal "failed to allocate a huatuo-bamai API port"
	fi
	if grep -Eq '^[[:space:]]*\[HTTPServer\][[:space:]]*$' "${config}"; then
		sed -i "/^[[:space:]]*\\[HTTPServer\\][[:space:]]*$/a\\    ListenAddress = \"127.0.0.1:${port}\"" "${config}"
	else
		printf '\n[HTTPServer]\n    ListenAddress = "127.0.0.1:%s"\n' "${port}" >> "${config}"
	fi
	HUATUO_BAMAI_ADDR="http://127.0.0.1:${port}"
	HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
}

# huatuo_bamai_collect_metrics saves /metrics output to the temp metrics file.
huatuo_bamai_collect_metrics() {
	huatuo_bamai_metrics > "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}

# huatuo_bamai_await_metrics waits until the metrics endpoint responds, then saves.
huatuo_bamai_await_metrics() {
	wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" \
		"${WAIT_HUATUO_BAMAI_INTERVAL}" \
		huatuo_bamai_collect_metrics
}

# assert_metrics <desc> <present_pattern>... [-- <absent_pattern>...]
assert_metrics() {
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
		local pat
		for pat in "${present[@]}"; do
			grep -qE "${prefix}(${pat})" "$metrics_file" \
				|| fatal "${desc}: expected present but not found: ${pat}"
		done
	fi
}

# Both clocks must survive BPF decoding and JSON output without exposing host uptime.
assert_kernel_observation_timestamps() {
	local events_file=$1
	jq -e -s '
		def utc_seconds:
			if type == "string" and test("Z$") then
				sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601
			else error("expected UTC timestamp") end;
		length > 0 and all(.[];
			(has("kernel_observed_ns") | not)
			and ((.observed_timestamp | utc_seconds) as $observed
				| (.kernel_observed_timestamp | utc_seconds) as $kernel
				| $kernel <= $observed + 1
				and $observed - $kernel < 60
				and (now - $observed | fabs) < 120))
	' "${events_file}" > /dev/null \
		|| fatal "invalid kernel/userspace observation timestamps: ${events_file}"
	log_info "event with UTC observation timestamps: $(head -n 1 "${events_file}")"
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
			"${out_dir}/windows.json" > /dev/null 2>&1
	}
	wait_until 10 0.1 profile_windows_are_ready \
		|| fatal "two Java windows not published during collection (see ${out_dir}/profiler.err)"
	kill -0 "${profiler_pid}" 2> /dev/null \
		|| fatal "Java profiler exited before streaming output was observed"
	local status=0
	stop_and_wait_by_pid "${profiler_pid}" 5 INT || status=$?
	profiler_pid=""
	[[ ${status} -eq 0 ]] || fatal "Java streaming profiler exited ${status} (see ${out_dir}/profiler.err)"
	jq -se '[.[] | select((.profile.sample | length) > 0)] | length >= 2' \
		"${out_dir}/windows.json" > /dev/null \
		|| fatal "expected multiple non-empty Java windows in ${out_dir}/windows.json"
)

storage_send_udp_packets() {
	local port=$1 count=${2:-1} packet
	for ((packet = 0; packet < count; packet++)); do
		printf 'huatuo-storage-drop' 2>> "${HUATUO_BAMAI_TEST_TMPDIR}/traffic.log" \
			> "/dev/udp/127.0.0.99/${port}" \
			|| fatal "failed to send UDP packet to 127.0.0.99:${port}"
	done
}

# Keep fixed PID and socket paths private to this case and its child processes.
integration_isolate_run_directory() {
	[[ /var/run -ef "${HUATUO_BAMAI_TEST_TMPDIR}/run" ]] && return 0
	require_commands unshare mount
	exec unshare --mount --propagation private bash -euc '
		mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}/run"
		mount --bind "${HUATUO_BAMAI_TEST_TMPDIR}/run" /var/run
		exec bash "$@"
	' bash "$0" "$@"
}
