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

#  -------------------------------- log ---------------------------------------
TEST_LOG_TAG=${TEST_LOG_TAG:-"INTEGRATION TEST"}
log_prefix() {
	echo "[${TEST_LOG_TAG}]"
}

log_info() {
	echo "$(log_prefix) $*"
}

log_warn() {
	echo "$(log_prefix)[WARN] $*" >&2
}

log_error() {
	echo "$(log_prefix)[ERROR] $*" >&2
}

fatal() {
	echo "$(log_prefix)[FAIL] $*" >&2
	exit 1
}

# skip <reason>
# Records that the test cannot meaningfully run in this environment and
# exits 0 so the harness treats it as success without false confidence.
skip() {
	echo "$(log_prefix)[SKIP] $*"
	exit 0
}

#  ---------------------------------- utils -----------------------------------
assert_eq() {
	local actual=$1 expect=$2 msg=${3:-""}
	if [[ "$actual" == "$expect" ]]; then
		return 0
	fi

	log_info "assert_eq: ${msg} actual=${actual}, expect=${expect}"
	return 1
}

# wait_until <timeout> <interval> <description> <function> [args...]
# Returns: 0 on success, 1 on timeout (does not exit script)
wait_until() {
	local timeout=$1 interval=$2 desc=$3
	shift 3
	local func=$1
	shift

	if ! type -t "$func" >/dev/null 2>&1; then
		echo "❌ wait_until expects function or command: \"$func\"" >&2
		return 1
	fi

	local end=$(($(date +%s) + timeout))
	local attempt=0
	local ret=0

	while [ "$(date +%s)" -lt "$end" ]; do
		ret=0
		attempt=$((attempt + 1))
		log_info "wait attempt #${attempt}: ${desc}, func/cmd: [${func} ${@}]"
		"$func" "$@" || ret=$?
		if [ "$ret" -eq 0 ]; then
			return 0
		fi

		sleep "$interval"
	done

	log_error "❌ wait_until timeout: ${desc}, func/cmd: [${func} ${@}]"
	return 1
}

# --------------------------- bpf tool test scaffolding ----------------------
# Common scaffolding used by per-tool test_*.sh scripts (e.g.,
# test_dropwatch_ratelimit.sh, test_iotracing.sh).

# bpf_tool_setup <name>
# Populates TOOL_BIN/TOOL_BPF/TOOL_OUT/TOOL_ERR for the named tool and
# aborts unless running as root with both build artifacts present.
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

# dump_tool_logs_and_fail <msg>
# Streams TOOL_OUT and TOOL_ERR to stderr for post-mortem then aborts.
dump_tool_logs_and_fail() {
	log_error "----- OUT (${TOOL_OUT}) -----"
	[[ -f "${TOOL_OUT}" ]] && cat "${TOOL_OUT}" >&2
	log_error "----- ERR (${TOOL_ERR}) -----"
	[[ -f "${TOOL_ERR}" ]] && cat "${TOOL_ERR}" >&2
	log_error "----- end -----"
	fatal "$*"
}

# ------------------------------- huatuo-bamai --------------------------------
HUATUO_BAMAI_PID=""

huatuo_bamai_start() {
	local args=("$@")
	[[ -x "${HUATUO_BAMAI_BIN}" ]] || fatal "huatuo-bamai binary not found: ${HUATUO_BAMAI_BIN}"

	log_info "starting huatuo-bamai: ${args[*]}"
	${HUATUO_BAMAI_BIN} "${args[@]}" >${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log 2>&1 &
	HUATUO_BAMAI_PID=$!

	sleep 0.5s

	wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" "${WAIT_HUATUO_BAMAI_INTERVAL}" "huatuo-bamai ready" \
		huatuo_bamai_ready
}

huatuo_bamai_ready() {
	# pid check, maybe process already exited
	kill -0 "${HUATUO_BAMAI_PID}" 2>/dev/null || fatal "❌ huatuo-bamai pid=${HUATUO_BAMAI_PID} not exist, maybe exited."
	# healthz
	curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_METRICS_API}" >/dev/null
}

huatuo_bamai_stop() {
	if [[ -n "${HUATUO_BAMAI_PID}" ]]; then
		log_info "stopping huatuo-bamai (pid=${HUATUO_BAMAI_PID})"
		kill "${HUATUO_BAMAI_PID}" || true
		wait "${HUATUO_BAMAI_PID}" || true
	fi
}

huatuo_bamai_metrics() {
	curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_METRICS_API}"
}

# print colored log and check if contains keywords
huatuo_bamai_log_check() {
	sed -E "s/(${HUATUO_BAMAI_MATCH_KEYWORDS})/\x1b[31m\1\x1b[0m/gI" ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log
	! grep -qE "${HUATUO_BAMAI_MATCH_KEYWORDS}" ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log
}

huatuo_bamai_pod_count() {
	local regex=$1
	curl -sf "${CURL_TIMEOUT[@]}" ${HUATUO_BAMAI_PODS_API} |
		jq --arg re "$regex" '
      [ .data[]
        | select(.hostname != null)
        | select(.hostname | test($re))
      ] | length
    ' 2>/dev/null || echo 0
}

# -------------------------------- integration --------------------------------
integration_test_huatuo_bamai_config() {
	cat >"${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" <<'EOF'
# the blacklist for tracing and metrics
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq"]
EOF
}

integration_test_huatuo_bamai_start() {
	[[ -x ${HUATUO_BAMAI_BIN} ]] || fatal "❌ binary not found: ${HUATUO_BAMAI_BIN}"
	[[ -d ${HUATUO_BAMAI_TEST_EXPECTED} ]] || fatal "❌ expected metrics directory not found: ${HUATUO_BAMAI_TEST_EXPECTED}"

	log_info "starting huatuo-bamai (mock fixture fs)"

	integration_test_huatuo_bamai_config

	huatuo_bamai_start "${HUATUO_BAMAI_ARGS_INTEGRATION[@]}"
	log_info "huatuo-bamai started"
}

integration_test_teardown() {
	local exit_code=$1

	huatuo_bamai_stop || true

	# Print details on failure
	if [ "${exit_code}" -ne 0 ]; then
		log_info "the exit code: $exit_code"
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

	if ! huatuo_bamai_log_check; then
		log_error "❌ huatuo-bamai log check failed"
		exit_code=1
	fi

	if [ $exit_code -ne 0 ]; then
		fatal "❌ integration test failed with exit code: ${exit_code}"
	fi
}
