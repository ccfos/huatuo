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

readonly API_TOKEN="integration-admin"
readonly FAILURE_LOG_PATTERN='panic:|fatal|level=(error|panic|fatal)|"level":"(error|panic|fatal)"'

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

assert_endpoints() {
	local cases=(
		"GET|/healthz|204|public|empty"
		"GET|/readyz|204|public|empty"
		"GET|/v1/profiles/missing/raw|404|admin|ignore"
		"POST|/v1/profiles/flamegraph/querier.v1.QuerierService/ProfileTypes|404|admin|ignore"
	)
	local test_case method path expected_status auth body_mode
	local body status
	local index=0
	local -a curl_args

	for test_case in "${cases[@]}"; do
		IFS='|' read -r method path expected_status auth body_mode <<< "${test_case}"
		body="${HUATUO_BAMAI_TEST_TMPDIR}/endpoint-${index}.body"
		curl_args=(
			-sS
			"${CURL_TIMEOUT[@]}"
			-X "${method}"
			-o "${body}"
			-w '%{http_code}'
		)
		if [[ "${auth}" == "admin" ]]; then
			curl_args+=(-H "Authorization: Bearer ${API_TOKEN}")
		fi

		status=$(curl "${curl_args[@]}" "${APISERVER_ADDR}${path}")
		assert_eq "${status}" "${expected_status}" "${method} ${path} status" \
			|| fatal "${method} ${path} returned status ${status}, expected ${expected_status}"
		if [[ "${body_mode}" == "empty" && -s "${body}" ]]; then
			fatal "${method} ${path} returned a non-empty response body"
		fi
		index=$((index + 1))
	done
}

assert_metrics_endpoint() {
	local body="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
	local headers="${HUATUO_BAMAI_TEST_TMPDIR}/metrics.headers"
	local status
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -D "${headers}" -o "${body}" \
		-w '%{http_code}' "${APISERVER_ADDR}/metrics")

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
	--disable-cgroup \
	--log-debug
assert_endpoints
assert_metrics_endpoint
! grep -qiE "${FAILURE_LOG_PATTERN}" "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" \
	|| fatal "huatuo-apiserver log contains an unexpected failure"
