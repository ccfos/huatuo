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

if [[ -n "${__HUATUO_LIB_CONTINUOUS_PROFILING_SH_LOADED:-}" ]]; then
	return 0
fi
readonly __HUATUO_LIB_CONTINUOUS_PROFILING_SH_LOADED=1

CONTINUOUS_PROFILE_DIAGNOSTIC="no raw profile request made"

continuous_profiling_requirements() {
	command -v docker > /dev/null || skip "docker command is not installed"
	docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"
	command -v jq > /dev/null || skip "jq command is not installed"
	command -v ss > /dev/null || skip "ss command is not installed"
	command -v timeout > /dev/null || fatal "timeout command is not installed"
	[[ -x "${HUATUO_APISERVER_BIN}" ]] \
		|| fatal "huatuo-apiserver binary missing"
	[[ -x "${ROOT_DIR}/_output/bin/huatuo-bamai" ]] \
		|| fatal "huatuo-bamai binary missing"
	[[ -x "${ROOT_DIR}/_output/bin/profiler" ]] \
		|| fatal "profiler binary missing"
	[[ -r "${ROOT_DIR}/_output/bpf/native_cpu_profiler.o" ]] \
		|| fatal "native CPU profiler BPF object missing"
	[[ -r /proc/sys/kernel/perf_event_paranoid ]] \
		|| skip "perf_event is unavailable"

	local paranoid
	paranoid=$(< /proc/sys/kernel/perf_event_paranoid)
	[[ "${paranoid}" -le 2 ]] \
		|| skip "kernel.perf_event_paranoid=${paranoid} blocks sampling"
}

continuous_profiling_cleanup() {
	local status=$1 target_pid=${2:-}

	[[ -n "${target_pid}" ]] && stop_by_pid "${target_pid}" 5 || true
	huatuo_apiserver_stop
	huatuo_bamai_stop "${HUATUO_BAMAI_TEST_TMPDIR}" || true
	if [[ -n "${ELASTICSEARCH_CONTAINER_ID}" ]]; then
		if [[ ${status} -ne 0 ]]; then
			elasticsearch_dump_logs || true
		fi
		elasticsearch_stop || true
	fi
}

continuous_profiling_start_native_cpu_fixture() {
	local output_variable=$1
	local fixture_source=${2:-"${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c"}
	local fixture_bin="${HUATUO_BAMAI_TEST_TMPDIR}/callchain"
	local fixture_pid

	compile_user_fixture "${fixture_source}" "${fixture_bin}"
	"${fixture_bin}" > "${HUATUO_BAMAI_TEST_TMPDIR}/fixture.out" \
		2> "${HUATUO_BAMAI_TEST_TMPDIR}/fixture.err" &
	fixture_pid=$!
	kill -0 "${fixture_pid}" 2> /dev/null \
		|| fatal "CPU fixture exited immediately"
	printf -v "${output_variable}" '%s' "${fixture_pid}"
}

continuous_profile_create_cpu() {
	local response_file=$1 duration=$2
	local description=${3:-"create native CPU profile"}
	local status curl_status=0

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" \
		-w '%{http_code}' -X POST \
		-H "Authorization: Bearer ${API_TOKEN}" \
		-H 'Content-Type: application/json' \
		"${APISERVER_ADDR}/v1/profiles" \
		-d "{\"type\":\"cpu\",\"language\":\"c\",\"duration_seconds\":${duration},\"hostname\":\"127.0.0.1\"}") \
		|| curl_status=$?
	if [[ -r "${response_file}" ]]; then
		log_info "${description} response: $(< "${response_file}")"
	else
		log_error "${description} response file missing: ${response_file}"
	fi
	[[ ${curl_status} -eq 0 ]] \
		|| fatal "${description} request failed with curl exit code ${curl_status}"
	assert_eq "${status}" "201" "${description}" \
		|| fatal "${description} failed: $(< "${response_file}")"
}

continuous_profile_status_is() {
	local profile_id=$1 expected_status=$2 response_file=$3

	curl -sf "${CURL_TIMEOUT[@]}" -H "Authorization: Bearer ${API_TOKEN}" \
		"${APISERVER_ADDR}/v1/profiles/${profile_id}" \
		> "${response_file}" \
		|| return 1
	jq -e --arg expected_status "${expected_status}" \
		'.data.status == $expected_status' "${response_file}" > /dev/null
}

continuous_profile_windows_are_stored() {
	local profile_id=$1 response_file=$2
	local status count

	status=$(
		curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
			-H "Authorization: Bearer ${API_TOKEN}" \
			"${APISERVER_ADDR}/v1/profiles/${profile_id}/raw"
	) || {
		CONTINUOUS_PROFILE_DIAGNOSTIC="raw profile request failed before receiving an HTTP response"
		return 1
	}
	count=$(jq -er '
		.data.items
		| map(
			has("uploaded_at")
			and has("captured_at")
			and has("profile_type")
			and has("profile")
			and (has("tracer_id") | not)
			and (has("tracer_data") | not)
		)
		| if all then length else error("invalid raw profile contract") end
	' "${response_file}" 2> /dev/null) || {
		CONTINUOUS_PROFILE_DIAGNOSTIC="raw profile response status=${status}, invalid body: $(< "${response_file}")"
		return 1
	}
	CONTINUOUS_PROFILE_DIAGNOSTIC="raw profile response status=${status}, windows=${count}"
	[[ "${status}" == "200" && "${count}" -ge 2 ]]
}

continuous_profiling_start_stack() {
	elasticsearch_start
	integration_huatuo_bamai_start \
		write_continuous_profiling_bamai_config \
		--region integration \
		--disable-kubelet \
		--log-debug
	integration_huatuo_apiserver_start \
		write_continuous_profiling_apiserver_config \
		"$@"
}
