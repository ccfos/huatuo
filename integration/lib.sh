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

# A fresh execution context binds reasons to its main shell. Ordinary child
# shells inherit the owner and cannot publish a caught error as the case result.
if [[ -n ${RUNNER_REASON_DIR:-} && -z ${RUNNER_REASON_OWNER:-} ]]; then
	RUNNER_REASON_OWNER=${BASHPID}
	export RUNNER_REASON_OWNER
fi

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

record_exit_reason() {
	local code=$1 reason=$2
	[[ -n ${RUNNER_REASON_DIR:-} ]] || return 0
	[[ ${RUNNER_REASON_OWNER:-${BASHPID}} == "${BASHPID}" ]] || return 0
	printf '%s\n' "${reason}" > "${RUNNER_REASON_DIR}/reason.${code}.log"
}

fatal() {
	record_exit_reason 1 "$*" || log_error "cannot record failure reason"
	printf '[%s][%s][FAIL] %s\n' \
		"$(TZ=UTC-8 date '+%Y-%m-%dT%H:%M:%S+08:00')" "${TEST_LOG_TAG}" "$*" >&2
	exit 1
}

# The runner reserves 77 for skipped cases; cleanup still runs through EXIT.
skip() {
	record_exit_reason 77 "$*" || {
		log_error "cannot record skip reason"
		exit 1
	}
	printf '[%s][%s][SKIP] ⏭️ %s\n' \
		"$(TZ=UTC-8 date '+%Y-%m-%dT%H:%M:%S+08:00')" "${TEST_LOG_TAG}" "$*"
	exit 77
}

# A failed step terminates its caller; use ordinary shell control flow for
# expected failures. Each explicit step owns and consumes its reason channel.
run_test_step() {
	(($# > 0)) || fatal "run_test_step requires an external command"
	local step_dir status=0 reason
	step_dir=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/tmp.step.XXXXXX") \
		|| fatal "cannot create step reason directory in ${HUATUO_BAMAI_TEST_TMPDIR}"
	(
		unset RUNNER_REASON_OWNER
		export RUNNER_REASON_DIR=${step_dir}
		exec "$@"
	) &
	local pid=$!
	wait "${pid}" || status=$?
	if ((status)); then
		reason="step command ${1} exited with code ${status}"
		if [[ -f ${step_dir}/reason.${status}.log ]]; then reason=$(< "${step_dir}/reason.${status}.log"); fi
		record_exit_reason "${status}" "${reason}" || log_error "cannot propagate step failure reason"
	fi
	rm -rf -- "${step_dir}" || fatal "cannot remove step reason directory: ${step_dir}"
	if ((status)); then exit "${status}"; fi
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

runner_build_signal_reset_launcher() {
	local -n output_launcher=$1
	# Ignored signals survive exec, so reset them before Bash starts.
	output_launcher=(python3 -c '
import os
import signal
import sys

for signum in (signal.SIGINT, signal.SIGTERM):
    signal.signal(signum, signal.SIG_DFL)
signal.pthread_sigmask(signal.SIG_UNBLOCK, (signal.SIGINT, signal.SIGTERM))
os.execvp(sys.argv[1], sys.argv[1:])
')
}

require_readable() {
	local path
	for path in "$@"; do
		[[ -r "${path}" ]] || skip "file is not readable: ${path}"
	done
}

require_toml_reader() {
	require_commands python3 jq
	python3 -c 'import importlib.util; __import__(next(name for name in ("tomllib", "tomli", "toml") if importlib.util.find_spec(name)))' \
		> /dev/null 2>&1 || skip "TOML parsing requires Python 3.11+ or the tomli or toml module"
}

# Decode framework configuration without installing a fixture into each workspace.
read_toml_json() {
	require_commands python3
	python3 - "$1" << 'PYTHON'
import json
import importlib.util
import sys
parser_name = next((name for name in ("tomllib", "tomli", "toml") if importlib.util.find_spec(name)), None)
if parser_name is None:
    sys.exit("TOML parsing requires Python 3.11+ or the tomli or toml module")
parser = __import__(parser_name)
with open(sys.argv[1], encoding="utf-8") as source:
    json.dump(parser.loads(source.read()), sys.stdout, default=str)
print()
PYTHON
}

assert_toml_values() {
	require_toml_reader
	local config_file=$1 expected=$2
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/$(basename "${config_file}").json"
	local error_file="${response_file}.error"
	read_toml_json "${config_file}" \
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
	local protocol=${1:-tcp}
	[[ ${protocol} == tcp || ${protocol} == udp ]] \
		|| fatal "unsupported port protocol: ${protocol}; expected tcp or udp"
	local INTEGRATION_PORT_DIR=${INTEGRATION_PORT_DIR:-${HUATUO_BAMAI_TEST_TMPDIR}/ports}
	local INTEGRATION_PORT_BASE=${INTEGRATION_PORT_BASE:-20000}
	require_commands flock ss
	mkdir -p "${INTEGRATION_PORT_DIR}"
	allocate_worker_port "${protocol}" 9> "${INTEGRATION_PORT_DIR}/lock"
}

# Clear only the previous execution's reservations. The scheduler has reaped
# that worker before assigning the slot again; keep the cursor across executions.
prepare_execution_ports() {
	require_commands flock ss
	mkdir -p "${INTEGRATION_PORT_DIR}"
	(
		flock 9 || exit 1
		: > "${INTEGRATION_PORT_DIR}/allocated"
	) 9> "${INTEGRATION_PORT_DIR}/lock"
}

# Reservations coordinate unbound services in this execution; socket probing
# also excludes listeners left by other processes when the cursor wraps.
allocate_worker_port() {
	local protocol=$1 port=${INTEGRATION_PORT_BASE:?worker port base is missing} sockets attempt used
	local -A allocated=()
	flock 9 || return 1
	sockets=$(ss -H -an "--${protocol}") || {
		log_error "cannot inspect ${protocol} sockets in the execution network namespace"
		return 1
	}
	if [[ -f ${INTEGRATION_PORT_DIR}/allocated ]]; then
		while read -r used; do allocated[$used]=1; done < "${INTEGRATION_PORT_DIR}/allocated"
	fi
	if [[ -f ${INTEGRATION_PORT_DIR}/next ]]; then read -r port < "${INTEGRATION_PORT_DIR}/next"; fi
	for ((attempt = 0; attempt < 1000; attempt++)); do
		((port < INTEGRATION_PORT_BASE + 1000)) || port=$INTEGRATION_PORT_BASE
		printf '%s\n' "$((port + 1))" > "${INTEGRATION_PORT_DIR}/next" || return 1
		# Consume all socket records so an early match cannot cause SIGPIPE under pipefail.
		if [[ -z ${allocated[$port]:-} ]] && awk -v port="${port}" '
			$4 ~ ("[:.]" port "$") { used = 1 }
			END { exit used }
		' <<< "${sockets}"; then
			printf '%s\n' "$port" >> "${INTEGRATION_PORT_DIR}/allocated" || return 1
			printf '%s\n' "$port"
			return 0
		fi
		port=$((port + 1))
	done
	log_error "execution port range is exhausted: ${INTEGRATION_PORT_BASE}"
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
cleanup_running_jobs() {
	local pid
	while IFS= read -r pid; do
		kill -KILL -- "-${pid}" 2> /dev/null || kill -KILL "${pid}" 2> /dev/null || true
		wait "${pid}" 2> /dev/null || true
	done < <(jobs -pr)
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

# Call from the child's parent shell so wait can verify its exit status.
# Unlike cleanup, a shutdown assertion must not hide a timeout with SIGKILL.
# Optional third argument selects TERM (default) or INT.
assert_process_shutdown_by_pid() {
	local pid=${1:-} timeout=${2:-10} signal=${3:-TERM} status=0
	if [[ ! ${pid} =~ ^[1-9][0-9]*$ || ! ${timeout} =~ ^[1-9][0-9]*$ ]]; then
		log_error "invalid stop arguments: pid=${pid}, timeout=${timeout}; expected positive integers"
		return 1
	fi

	case ${signal} in
	TERM | INT) ;;
	*)
		log_error "unsupported stop signal ${signal}; expected TERM or INT"
		return 1
		;;
	esac

	if ! kill -"${signal}" "${pid}" 2> /dev/null; then
		log_error "failed to send SIG${signal} to pid=${pid}"
		return 1
	fi
	if ! wait_until "${timeout}" 0.1 process_exited "${pid}"; then
		log_error "pid=${pid} did not exit within ${timeout}s after SIG${signal}"
		return 1
	fi

	wait "${pid}" || status=$?
	if [[ ${status} -ne 0 ]]; then
		log_error "pid=${pid} exited with status ${status} after SIG${signal}"
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
	require_commands ss
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
	local pid listeners port=${HUATUO_BAMAI_ADDR##*:}
	pid=$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" 2> /dev/null || echo "")
	[[ -n "$pid" ]] || return 1

	if process_exited "${pid}"; then
		fatal "huatuo-bamai pid=${pid} exited during startup; see ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	fi

	curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_METRICS_API}" > /dev/null || return 1
	# A successful HTTP response can come from an older daemon on the same port.
	listeners=$(ss -H -ltnp "sport = :${port}") || return 1
	grep -Fq "pid=${pid}," <<< "${listeners}" || return 1
	if process_exited "${pid}"; then
		fatal "huatuo-bamai pid=${pid} exited during readiness check; see ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	fi
}

huatuo_bamai_stop() {
	local test_workspace=${1:-${HUATUO_BAMAI_TEST_TMPDIR}}
	local pid
	pid=$(cat "${test_workspace}/huatuo-bamai.pid" 2> /dev/null || echo "")
	if [[ -n ${pid} ]]; then
		stop_by_pid "${pid}" || return 1
		# Device-owning callers must not release resources while readers remain.
		if ! wait_until 2 0.1 process_exited "${pid}"; then
			log_error "huatuo-bamai pid=${pid} is still running; retaining PID file"
			return 1
		fi
	fi
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
	if [[ -n ${pid} ]]; then
		stop_by_pid "${pid}" || return 1
		if ! wait_until 2 0.1 process_exited "${pid}"; then
			log_error "huatuo-apiserver pid=${pid} is still running; retaining PID file"
			return 1
		fi
	fi
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

# Cleanup runs inside the case namespace; diagnostics are printed by the runner
# after stdout/stderr have closed, avoiding recursive reads of the live logs.
integration_test_cleanup() {
	local test_workspace=$1
	local cleanup_status=0
	huatuo_apiserver_stop "${test_workspace}" || {
		cleanup_status=1
		RUNNER_CLEANUP_REASON="failed to stop huatuo-apiserver"
	}
	huatuo_bamai_stop "${test_workspace}" || {
		cleanup_status=1
		RUNNER_CLEANUP_REASON="${RUNNER_CLEANUP_REASON:+${RUNNER_CLEANUP_REASON}; }failed to stop huatuo-bamai"
	}

	return "$cleanup_status"
}

# Compare the public build-info sample with the same service's /version response.
assert_build_info() {
	local address=$1 namespace=$2 expected_host=$3 expected_region=$4
	local endpoint response_file error_file status curl_status
	for endpoint in version metrics; do
		response_file="${HUATUO_BAMAI_TEST_TMPDIR}/build-info-${endpoint}.body"
		error_file="${HUATUO_BAMAI_TEST_TMPDIR}/build-info-${endpoint}.curl-error"
		curl_status=0
		status=$(curl -sS "${CURL_TIMEOUT[@]}" \
			-D "${response_file}.headers" -o "${response_file}" -w '%{http_code}' \
			"${address}/${endpoint}" 2> "${error_file}") || curl_status=$?
		log_info "GET /${endpoint}: curl exit=${curl_status}, HTTP status=${status}"
		if [[ ${endpoint} == version || ${curl_status} -ne 0 || ${status} != 200 ]]; then
			report_http_response "GET /${endpoint}" "${response_file}" "${error_file}"
		fi
		[[ ${curl_status} -eq 0 ]] || fatal "GET /${endpoint}: curl exited ${curl_status}"
		assert_eq "${status}" 200 "GET /${endpoint} status" \
			|| fatal "GET /${endpoint}: expected HTTP 200, got ${status}"
	done

	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/build-info-metrics.body"
	local version_file="${HUATUO_BAMAI_TEST_TMPDIR}/build-info-version.body"
	grep -qiE '^Content-Type:[[:space:]]*text/plain([;[:space:]]|$)' "${metrics_file}.headers" \
		|| fatal "GET /metrics: expected text/plain, headers: $(< "${metrics_file}.headers")"
	grep -Fxq "# TYPE ${namespace}_build_info gauge" "${metrics_file}" \
		|| fatal "expected ${namespace}_build_info to be a Gauge"

	local count sample labels expected
	count=$(grep -cE "^${namespace}_build_info(\{|[[:space:]])" "${metrics_file}" || true)
	assert_eq "${count}" 1 "build-info sample count" \
		|| fatal "expected one build-info sample, got ${count}"
	sample=$(grep -E "^${namespace}_build_info(\{|[[:space:]])" "${metrics_file}")
	log_info "build-info sample: ${sample}"
	# Match the exporter's sorted labels and Prometheus text escaping, not JSON escaping.
	labels=$(jq -er --arg host "${expected_host}" --arg region "${expected_region}" '
		.data | {name, version, git_commit, git_tree_state, build_time, go_version, compiler, platform}
		| if all(.[]; type == "string" and length > 0) then .
		  else error("missing nonempty build identity field") end
		| . + {host: $host, region: $region}
		| to_entries | sort_by(.key)
		| map(.key + "=\"" + (.value
			| split("\\") | join("\\\\")
			| split("\n") | join("\\n")
			| split("\"") | join("\\\"")) + "\"")
		| join(",")
	' "${version_file}") || fatal "GET /version: cannot encode build identity labels"
	expected="${namespace}_build_info{${labels}} 1"
	assert_eq "${sample}" "${expected}" "build-info sample" \
		|| fatal "build-info: expected ${expected}, got ${sample}"
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
	huatuo_bamai_initialize_address
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
	huatuo_bamai_configure_default_address
	huatuo_bamai_start "${runtime_args[@]}"
}

# The default belongs to this execution, not a service restart or config file.
# Export it across the worker/test boundary; explicit configs may change the
# client address without replacing the execution's default.
huatuo_bamai_initialize_address() {
	[[ -z ${HUATUO_BAMAI_DEFAULT_ADDR:-} ]] || return 0
	local port
	port=$(allocate_available_port) || fatal "cannot allocate bamai HTTP port"
	export HUATUO_BAMAI_DEFAULT_ADDR="http://127.0.0.1:${port}"
	export HUATUO_BAMAI_ADDR=${HUATUO_BAMAI_DEFAULT_ADDR}
	export HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
}

# Preserve explicit listeners and derive the local client endpoint from them.
huatuo_bamai_configure_default_address() {
	require_toml_reader
	local config="${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" address parsed port
	parsed=$(read_toml_json "${config}") \
		|| fatal "cannot parse bamai configuration: ${config}"
	address=$(jq -er '
		(.HTTPServer // {}) |
		if has("ListenAddress") then .ListenAddress | select(type == "string" and length > 0)
		else "" end
	' <<< "${parsed}") \
		|| fatal "HTTPServer.ListenAddress must be a nonempty string: ${config}"
	if [[ -z ${address} ]]; then
		address=${HUATUO_BAMAI_DEFAULT_ADDR#http://}
		if ! python3 -c 'import toml' > /dev/null 2>&1 \
			&& ! python3 -c 'import tomli_w' > /dev/null 2>&1; then
			skip "TOML writing requires python3-toml or python3-tomli-w"
		fi
		# A serializer handles quoted and inline tables. Reparse and compare before
		# replacement so unsupported values cannot silently change other settings.
		python3 - "${config}" "${address}" << 'PYTHON' || fatal "cannot update bamai listener; original configuration retained: ${config}"
import json
import importlib.util
import os
from pathlib import Path
import sys
import tempfile
try:
    import toml
except ModuleNotFoundError:
    import tomli_w as toml
parser_name = next((name for name in ("tomllib", "tomli", "toml") if importlib.util.find_spec(name)), None)
if parser_name is None:
    sys.exit("TOML parsing requires Python 3.11+ or the tomli or toml module")
parser = __import__(parser_name)
path = Path(sys.argv[1])
with path.open(encoding="utf-8") as source:
    expected = parser.loads(source.read())
expected.setdefault("HTTPServer", {})["ListenAddress"] = sys.argv[2]
text = toml.dumps(expected)
actual = parser.loads(text)
# JSON normalization also compares TOML dates, times and NaN values consistently.
if json.dumps(actual, sort_keys=True, default=str) != json.dumps(expected, sort_keys=True, default=str):
    sys.exit("TOML serialization changed configuration values")
temporary = None
try:
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, prefix=".bamai.", delete=False) as target:
        temporary = target.name
        os.fchmod(target.fileno(), path.stat().st_mode & 0o777)
        target.write(text)
    parser.loads(Path(temporary).read_text(encoding="utf-8"))
    os.replace(temporary, path)
finally:
    if temporary is not None and os.path.exists(temporary):
        os.unlink(temporary)
PYTHON
	fi
	case ${address} in
	:*) address="127.0.0.1${address}" ;;
	0.0.0.0:*) address="127.0.0.1:${address##*:}" ;;
	\[::\]:*) address="[::1]:${address##*:}" ;;
	esac
	port=${address##*:}
	[[ ${port} =~ ^[0-9]{1,5}$ ]] && ((10#${port} > 0 && 10#${port} <= 65535)) \
		|| fatal "bamai listener requires a port between 1 and 65535: ${address}"
	export HUATUO_BAMAI_ADDR="http://${address}"
	export HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
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

# Save every attempt, including failed retries, without retaining an older body.
fetch_metrics_response() {
	local prefix=$1 status curl_status=0
	: > "${prefix}.headers" || return 1
	: > "${prefix}.txt" || return 1
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -D "${prefix}.headers" -o "${prefix}.txt" \
		-w '%{http_code}' "${HUATUO_BAMAI_METRICS_API}" 2> "${prefix}.err") || curl_status=$?
	printf 'curl_exit=%s\nhttp_status=%s\n' "${curl_status}" "${status}" > "${prefix}.status" || return 1
	[[ ${curl_status} -eq 0 && ${status} == 200 ]]
}

assert_metrics_response() {
	local prefix=$1
	if ! fetch_metrics_response "${prefix}"; then
		cat "${prefix}.status" "${prefix}.headers" "${prefix}.txt" "${prefix}.err" >&2
		fatal "/metrics: expected curl=0 HTTP=200; see ${prefix}.status"
	fi
	if ! grep -Eqi '^Content-Type:[[:blank:]]*text/plain([;[:space:]]|$)' "${prefix}.headers"; then
		cat "${prefix}.headers" >&2
		fatal "/metrics: expected text/plain; see ${prefix}.headers"
	fi
}

# assert_metrics <desc> <present_pattern>... [-- <absent_pattern>...]
assert_metrics() {
	local desc=$1 pattern found status
	local metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	shift

	while (($#)) && [[ $1 != -- ]]; do
		grep -qE "huatuo_bamai_($1)" "${metrics_file}" \
			|| fatal "${desc}: expected present but not found: $1"
		shift
	done
	(($#)) || return 0
	shift

	(($#)) || return 0
	local -a absent=()
	for pattern in "$@"; do
		absent+=(-e "huatuo_bamai_(${pattern})")
	done
	status=0
	found=$(grep -oE "${absent[@]}" "${metrics_file}") || status=$?
	case ${status} in
	0) fatal "${desc}: expected absent but found: ${found}" ;;
	1) ;;
	*) fatal "${desc}: cannot search ${metrics_file}: grep exited ${status}" ;;
	esac
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

storage_send_udp_packets() {
	local port=$1 count=${2:-1} packet
	for ((packet = 0; packet < count; packet++)); do
		printf 'huatuo-storage-drop' 2>> "${HUATUO_BAMAI_TEST_TMPDIR}/traffic.log" \
			> "/dev/udp/127.0.0.99/${port}" \
			|| fatal "failed to send UDP packet to 127.0.0.99:${port}"
	done
}
