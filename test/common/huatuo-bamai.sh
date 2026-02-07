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
	kill -0 "${HUATUO_BAMAI_PID}" 2>/dev/null || fatal "huatuo-bamai not running (pid=${HUATUO_BAMAI_PID})"
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
