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

# Verify huatuo-apiserver exposes its public health, readiness, and metrics APIs.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

# The config writer reads the token from the calling test's scope.
readonly API_TOKEN="integration-admin"

command -v curl > /dev/null || skip "curl command is not installed"
command -v ss > /dev/null || skip "ss command is not installed"
[[ -x "${HUATUO_APISERVER_BIN}" ]] \
	|| fatal "huatuo-apiserver binary missing: ${HUATUO_APISERVER_BIN}"

APISERVER_PORT=$(allocate_available_port) || fatal "failed to allocate an apiserver port"
readonly APISERVER_PORT
readonly APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"

cleanup() {
	huatuo_apiserver_stop
}
trap cleanup EXIT

assert_curl_succeeded() {
	local label=$1 response_file=$2 curl_status=$3
	[[ ${curl_status} -ne 0 ]] || return 0

	if [[ -r "${response_file}" ]]; then
		log_error "${label} response: $(< "${response_file}")"
	else
		log_error "${label} response file missing: ${response_file}"
	fi
	fatal "${label}: curl exited ${curl_status}"
}

assert_endpoints() {
	local paths=(
		"/healthz"
		"/readyz"
	)
	local path response_file status
	local curl_status

	for path in "${paths[@]}"; do
		response_file="${HUATUO_BAMAI_TEST_TMPDIR}/${path#/}.body"
		curl_status=0
		status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
			"${APISERVER_ADDR}${path}") || curl_status=$?
		assert_curl_succeeded "GET ${path}" "${response_file}" "${curl_status}"
		assert_eq "${status}" "204" "GET ${path} status" \
			|| fatal "GET ${path} returned status ${status}, expected 204"
		if [[ -s "${response_file}" ]]; then
			fatal "GET ${path} returned a non-empty response body"
		fi
	done
}

assert_metrics_endpoint() {
	local body="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	local headers="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.headers"
	local curl_status=0 status
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -D "${headers}" -o "${body}" \
		-w '%{http_code}' "${APISERVER_ADDR}/metrics") || curl_status=$?
	assert_curl_succeeded "GET /metrics" "${body}" "${curl_status}"

	assert_eq "${status}" "200" "GET /metrics status" \
		|| fatal "/metrics returned status ${status}"
	grep -qi '^Content-Type: text/plain' "${headers}" \
		|| fatal "/metrics did not return a Prometheus text content type"
	grep -q '^huatuo_apiserver_go_goroutines[{ ]' "${body}" \
		|| fatal "/metrics omitted huatuo-apiserver runtime metrics"
	grep -q '^huatuo_http_server_requests_total{method="GET",route="/healthz",status="204"} ' "${body}" \
		|| fatal "/metrics omitted the /healthz HTTP request counter"
}

integration_huatuo_apiserver_start write_apiserver_apis_config \
	--log-debug
assert_endpoints
assert_metrics_endpoint
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" huatuo-apiserver
