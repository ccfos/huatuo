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
# Example:
# wait_until 10 1 "check ready" my_check_func "arg1" "arg2"
wait_until() {
	local timeout=$1 interval=$2 desc=$3
	shift 3
	local func=$1
	shift

	if ! declare -f "$func" >/dev/null 2>&1; then
		fatal "❌ wait_until expects function or command: \"$func\""
	fi

	local end=$(($(date +%s) + timeout))
	local attempt=0
	while (($(date +%s) < end)); do
		attempt=$((attempt + 1))
		log_info "wait attempt #${attempt}: ${desc}"
		if "$func" "$@"; then
			return 0
		fi
		sleep "$interval"
	done

	fatal "❌ timeout waiting for: ${desc} after ${timeout}s"
}

# ------------------------------- huatuo-bamai --------------------------------
HUATUO_BAMAI_PID=""

huatuo_bamai_start() {
	local args=("$@")
	[[ -x "${HUATUO_BAMAI_BIN}" ]] || fatal "huatuo-bamai binary not found: ${HUATUO_BAMAI_BIN}"

	log_info "starting huatuo-bamai: ${args[*]}"
	${HUATUO_BAMAI_BIN} "${args[@]}" >${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log 2>&1 &
	HUATUO_BAMAI_PID=$!

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
      [ .[]
        | select(.hostname != null)
        | select(.hostname | test($re))
      ] | length
    ' 2>/dev/null || echo 0
}

# -------------------------------- integration --------------------------------
integration_test_huatuo_bamai_config() {
	cat >"${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" <<'EOF'
# the blacklist for tracing and metrics
BlackList = ["metax_gpu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq"]
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
}
