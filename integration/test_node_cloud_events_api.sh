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

# Verify the generated Node CloudEvents API contract and SSE transport.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly EVENT_API_TOKEN="integration-node-token"
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
readonly EVENT_STREAM_CURL_TIMEOUT=(--connect-timeout 2 --max-time 10)
event_stream_pid=""
event_capacity_status=""
event_capacity_curl_status=0

command -v curl > /dev/null || skip "curl command is not installed"
command -v jq > /dev/null || skip "jq command is not installed"
command -v ss > /dev/null || skip "ss command is not installed"
[[ -x "${HUATUO_BAMAI_BIN}" ]] \
	|| fatal "huatuo-bamai binary missing: ${HUATUO_BAMAI_BIN}"

EVENT_API_PORT=$(allocate_available_port) \
	|| fatal "failed to allocate a huatuo-bamai API port"
readonly EVENT_API_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${EVENT_API_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
export HUATUO_BAMAI_ADDR HUATUO_BAMAI_METRICS_API

cleanup() {
	stop_event_stream
	huatuo_bamai_stop
}
trap cleanup EXIT

stop_event_stream() {
	[[ -n "${event_stream_pid}" ]] || return 0
	kill "${event_stream_pid}" 2> /dev/null || true
	wait "${event_stream_pid}" 2> /dev/null || true
	event_stream_pid=""
}

write_event_api_config() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq", "iotracing"]

[HTTPServer]
    ListenAddress = "127.0.0.1:${EVENT_API_PORT}"
    MaxEventStreamClients = 1
    EventStreamKeepAliveIntervalSeconds = 1

[HTTPServer.Auth]
    BearerToken = "${EVENT_API_TOKEN}"
EOF
}

assert_error_response() {
	local label=$1 content_type=$2 request_body=$3 expected_status=$4 expected_code=$5
	local authenticated=$6
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/${label}.json"
	local error_file="${HUATUO_BAMAI_TEST_TMPDIR}/${label}.curl-error"
	local curl_status=0 status
	local auth_args=()
	if [[ "${authenticated}" == "true" ]]; then
		auth_args=(-H "Authorization: Bearer ${EVENT_API_TOKEN}")
	fi

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" \
		-w '%{http_code}' -X POST \
		-H "Content-Type: ${content_type}" "${auth_args[@]}" \
		"${HUATUO_BAMAI_ADDR}/v1/events/watch" \
		-d "${request_body}" 2> "${error_file}") || curl_status=$?
	report_http_response "${label}" "${response_file}" "${error_file}"
	if [[ ${curl_status} -ne 0 ]]; then
		fatal "${label}: curl exited ${curl_status}"
	fi
	assert_eq "${status}" "${expected_status}" "${label} status" \
		|| fatal "${label} returned status ${status}, expected ${expected_status}"
	jq -e --arg expected_code "${expected_code}" \
		'.error.code == $expected_code' "${response_file}" > /dev/null \
		|| fatal "${label} did not return ${expected_code}: $(< "${response_file}")"
}

assert_openapi_contract() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/node-openapi.json"
	local error_file="${HUATUO_BAMAI_TEST_TMPDIR}/node-openapi.curl-error"
	local curl_status=0 status
	status=$(curl -sS "${CURL_TIMEOUT[@]}" \
		"${HUATUO_BAMAI_ADDR}/openapi.json" -o "${response_file}" \
		-w '%{http_code}' 2> "${error_file}") || curl_status=$?
	if [[ ${curl_status} -ne 0 || "${status}" != "200" ]]; then
		report_http_response "GET /openapi.json" "${response_file}" "${error_file}"
		fatal "GET /openapi.json: curl exited ${curl_status}, status ${status:-unavailable}"
	fi
	jq -e '
        .paths["/v1/events/watch"].post.responses["200"]
            .content["text/event-stream"]
    ' "${response_file}" > /dev/null \
		|| fatal "Node OpenAPI omitted the CloudEvents SSE contract"
}

assert_response_header() {
	local headers_file=$1 name=$2 expected=$3
	grep -qi "^${name}: ${expected}"$'\r$' "${headers_file}" \
		|| fatal "event stream ${name} header is not ${expected}"
}

event_stream_capacity_is_released() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/event-capacity.body"
	local error_file="${HUATUO_BAMAI_TEST_TMPDIR}/event-capacity.curl-error"
	event_capacity_curl_status=0
	event_capacity_status=$(curl -sS -N "${CURL_TIMEOUT[@]}" \
		-o "${response_file}" -w '%{http_code}' -X POST \
		-H "Authorization: Bearer ${EVENT_API_TOKEN}" \
		-H 'Content-Type: application/json' \
		-H 'Accept: text/event-stream' \
		"${HUATUO_BAMAI_ADDR}/v1/events/watch" \
		-d '{"filters":{"tracer_name":"^integration-never$"}}' \
		2> "${error_file}") || event_capacity_curl_status=$?

	[[ "${event_capacity_status}" == "200" && ${event_capacity_curl_status} -eq 28 ]]
}

assert_event_stream_heartbeat() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/event-stream.body"
	local headers_file="${HUATUO_BAMAI_TEST_TMPDIR}/event-stream.headers"
	local error_file="${HUATUO_BAMAI_TEST_TMPDIR}/event-stream.error"

	curl -sS -N "${EVENT_STREAM_CURL_TIMEOUT[@]}" -D "${headers_file}" \
		-o "${response_file}" -X POST \
		-H "Authorization: Bearer ${EVENT_API_TOKEN}" \
		-H 'Content-Type: application/json' \
		-H 'Accept: text/event-stream' \
		"${HUATUO_BAMAI_ADDR}/v1/events/watch" \
		-d '{"filters":{"tracer_name":"^integration-never$"}}' \
		2> "${error_file}" &
	event_stream_pid=$!
	wait_until 3 0.1 grep -qi '^HTTP/1.1 200' "${headers_file}" \
		|| {
			report_http_response "event stream" "${response_file}" "${error_file}"
			fatal "event stream did not open"
		}
	assert_response_header "${headers_file}" Content-Type text/event-stream
	assert_response_header "${headers_file}" Cache-Control no-cache
	assert_response_header "${headers_file}" Connection keep-alive
	assert_response_header "${headers_file}" X-Accel-Buffering no

	assert_error_response stream-limit application/json '{}' \
		429 event_stream_limit_exceeded true

	wait_until 3 0.1 grep -q '^: ping$' "${response_file}" \
		|| fatal "event stream did not emit a heartbeat"
	stop_event_stream

	wait_until 6 0.1 event_stream_capacity_is_released || {
		report_http_response "event stream capacity probe" \
			"${HUATUO_BAMAI_TEST_TMPDIR}/event-capacity.body" \
			"${HUATUO_BAMAI_TEST_TMPDIR}/event-capacity.curl-error"
		fatal "event stream capacity was not released: curl exited ${event_capacity_curl_status}, status ${event_capacity_status:-unavailable}"
	}
}

integration_huatuo_bamai_start write_event_api_config
assert_openapi_contract
assert_error_response missing-auth application/json '{}' 401 unauthenticated false
assert_error_response unsupported-media text/plain '{}' 415 unsupported_media_type true
assert_error_response unknown-filter application/json \
	'{"filters":{"unknown":"value"}}' 400 invalid_request true
assert_error_response invalid-regex application/json \
	'{"filters":{"tracer_name":"[invalid"}}' 400 invalid_request true
assert_event_stream_heartbeat
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" huatuo-bamai
