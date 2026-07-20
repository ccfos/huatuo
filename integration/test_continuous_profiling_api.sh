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

# Verify the complete native CPU continuous-profiling path: apiserver creates
# a bamai task, profiler uploads multiple windows through toolstream, and the
# apiserver reads the stored profiles back from Elasticsearch.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly ES_IMAGE="${HUATUO_ES_TEST_IMAGE:-docker.elastic.co/elasticsearch/elasticsearch:8.15.5}"
readonly ES_PASSWORD="huatuo-integration"
readonly API_USER="integration-admin"
readonly OTHER_USER="integration-other"
readonly PROFILE_DURATION=12
readonly PROFILE_INTERVAL=5
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c"

ES_CONTAINER_ID=""
ES_ADDR=""
APISERVER_PID=""
TARGET_PID=""
APISERVER_PORT=""
APISERVER_ADDR=""
LAST_PROFILE_DIAGNOSTIC="no raw profile request made"

cleanup() {
	local status=$?
	[[ -n "${TARGET_PID}" ]] && stop_by_pid "${TARGET_PID}" 5 || true
	[[ -n "${APISERVER_PID}" ]] && stop_by_pid "${APISERVER_PID}" 10 || true
	huatuo_bamai_stop "${HUATUO_BAMAI_TEST_TMPDIR}" || true
	if [[ -n "${ES_CONTAINER_ID}" ]]; then
		if [[ ${status} -ne 0 ]]; then
			docker logs "${ES_CONTAINER_ID}" \
				> "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch.log" 2>&1 || true
		fi
		docker rm -f "${ES_CONTAINER_ID}" > /dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

require_environment() {
	is_container && skip "continuous profiling requires bare-metal cgroup/PMU access"
	command -v docker > /dev/null || skip "docker command is not installed"
	docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"
	command -v jq > /dev/null || skip "jq command is not installed"
	command -v ss > /dev/null || skip "ss command is not installed"
	command -v timeout > /dev/null || fatal "timeout command is not installed"
	[[ -x "${ROOT_DIR}/_output/bin/huatuo-apiserver" ]] \
		|| fatal "huatuo-apiserver binary missing"
	[[ -x "${ROOT_DIR}/_output/bin/huatuo-bamai" ]] || fatal "huatuo-bamai binary missing"
	[[ -x "${ROOT_DIR}/_output/bin/profiler" ]] || fatal "profiler binary missing"
	[[ -r "${ROOT_DIR}/_output/bpf/native_cpu_profiler.o" ]] \
		|| fatal "native CPU profiler BPF object missing"
	[[ -r /proc/sys/kernel/perf_event_paranoid ]] || skip "perf_event is unavailable"
	local paranoid
	paranoid=$(cat /proc/sys/kernel/perf_event_paranoid)
	[[ "${paranoid}" -le 2 ]] || skip "kernel.perf_event_paranoid=${paranoid} blocks sampling"
	port_is_available 19704 || fatal "huatuo-bamai port 19704 is already in use"
	APISERVER_PORT=$(allocate_port) || fatal "failed to allocate an apiserver port"
	APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"
}

port_is_available() {
	local port=$1
	! ss -H -ltn | awk '{ print $4 }' | grep -Eq "[:.]${port}$"
}

allocate_port() {
	local attempt port
	for ((attempt = 0; attempt < 20; attempt++)); do
		port=$((20000 + RANDOM % 20001))
		if port_is_available "${port}"; then
			echo "${port}"
			return 0
		fi
	done
	return 1
}

start_elasticsearch() {
	if ! docker image inspect "${ES_IMAGE}" > /dev/null 2>&1; then
		log_info "pulling Elasticsearch image: ${ES_IMAGE}"
		if ! timeout 5m docker pull "${ES_IMAGE}" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-pull.log" 2>&1; then
			skip "failed to pull Elasticsearch image: ${ES_IMAGE}"
		fi
	fi

	ES_CONTAINER_ID=$(docker run --detach --rm \
		--publish 127.0.0.1::9200 \
		--env discovery.type=single-node \
		--env xpack.security.enabled=false \
		--env ES_JAVA_OPTS=-Xms512m\ -Xmx512m \
		"${ES_IMAGE}" \
		2> "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-run.log")
	local es_port
	es_port=$(docker port "${ES_CONTAINER_ID}" 9200/tcp | awk -F: 'NR == 1 { print $NF }')
	[[ -n "${es_port}" ]] || fatal "failed to resolve Elasticsearch port"
	ES_ADDR="http://127.0.0.1:${es_port}"
	wait_until 120 2 elasticsearch_ready \
		|| fatal "Elasticsearch did not become ready at ${ES_ADDR}"
}

elasticsearch_ready() {
	curl -sf "${CURL_TIMEOUT[@]}" \
		"${ES_ADDR}/_cluster/health?wait_for_status=yellow&timeout=2s" \
		| jq -e '.timed_out == false and (.status == "yellow" or .status == "green")' \
			> /dev/null
}

write_configs() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF
BlackList = ["metax_gpu", "ascend_npu", "softlockup", "ethtool", "netstat_hw", "iolatency", "memory_free", "memory_reclaim", "reschedipi", "softirq", "iotracing", "dropwatch"]

[Storage.ES]
    Address = "${ES_ADDR}"
    Username = "elastic"
    Password = "${ES_PASSWORD}"
    Index = "huatuo_continuous_profiling_test"

[Storage.LocalFile]
    Path = ""
EOF

	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf" << EOF
[APIServer]
    TCPAddr = "127.0.0.1:${APISERVER_PORT}"

[ElasticSearch]
    Address = "${ES_ADDR}"
    Username = "elastic"
    Password = "${ES_PASSWORD}"
    Index = "huatuo_continuous_profiling_test"

[[Auth.users]]
    ID = "${API_USER}"
    Name = "Integration administrator"
    IsAdmin = true

[[Auth.users]]
    ID = "${OTHER_USER}"
    Name = "Integration user"
    Permissions = ["/v1/profiles", "/v1/profiles/**"]

[Profiling]
    AggregationInterval = ${PROFILE_INTERVAL}
    ExecutionTimeout = 20
    MaxProfilerProcs = 1
    FlameGraphBaseURL = "http://grafana.invalid/d"
EOF
}

start_bamai() {
	(
		cd "${ROOT_DIR}/_output"
		exec bin/huatuo-bamai \
			--config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" \
			--config bamai.conf \
			--region integration \
			--disable-kubelet \
			--log-debug
	) > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" 2>&1 &
	echo "$!" > "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	wait_until "${WAIT_HUATUO_BAMAI_TIMEOUT}" "${WAIT_HUATUO_BAMAI_INTERVAL}" \
		continuous_profiling_bamai_ready || fatal "huatuo-bamai did not become ready"
}

continuous_profiling_bamai_ready() {
	local pid
	pid=$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" 2> /dev/null || true)
	if [[ -z "${pid}" ]] || ! kill -0 "${pid}" 2> /dev/null; then
		fatal "huatuo-bamai exited during startup: $(tail -n 80 "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log")"
	fi
	curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_METRICS_API}" > /dev/null
}

start_apiserver() {
	(
		cd "${HUATUO_BAMAI_TEST_TMPDIR}"
		exec "${ROOT_DIR}/_output/bin/huatuo-apiserver" \
			--config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" \
			--config apiserver.conf
	) > "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" 2>&1 &
	APISERVER_PID=$!
	wait_until 120 2 apiserver_ready || fatal "huatuo-apiserver did not become ready"
}

apiserver_ready() {
	if ! kill -0 "${APISERVER_PID}" 2> /dev/null; then
		fatal "huatuo-apiserver exited during startup: $(tail -n 80 "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log")"
	fi
	curl -sf "${CURL_TIMEOUT[@]}" \
		-H "Authorization: ${API_USER}" \
		"${APISERVER_ADDR}/v1/profiles/capabilities" > /dev/null
}

start_fixture() {
	local fixture_bin="${HUATUO_BAMAI_TEST_TMPDIR}/callchain"
	compile_user_fixture "${FIXTURE_SRC}" "${fixture_bin}"
	"${fixture_bin}" > "${HUATUO_BAMAI_TEST_TMPDIR}/fixture.out" \
		2> "${HUATUO_BAMAI_TEST_TMPDIR}/fixture.err" &
	TARGET_PID=$!
	kill -0 "${TARGET_PID}" 2> /dev/null || fatal "CPU fixture exited immediately"
}

assert_api_contract() {
	local status
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${HUATUO_BAMAI_TEST_TMPDIR}/unauthorized.json" \
		-w '%{http_code}' "${APISERVER_ADDR}/v1/profiles/capabilities")
	assert_eq "${status}" "401" "missing Authorization header" \
		|| fatal "capabilities accepted an unauthenticated request"

	curl -sf "${CURL_TIMEOUT[@]}" -H "Authorization: ${API_USER}" \
		"${APISERVER_ADDR}/v1/profiles/capabilities" \
		| jq -e --argjson interval "${PROFILE_INTERVAL}" \
			'.code == 0 and (.data.profile_types | index("cpu")) != null and .data.default_aggregation_interval == $interval' \
			> /dev/null || fatal "capabilities response does not advertise native CPU profiling"
}

create_profile() {
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/create-profile.json"
	local status
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' -X POST \
		-H "Authorization: ${API_USER}" \
		-H 'Content-Type: application/json' \
		"${APISERVER_ADDR}/v1/profiles" \
		-d "{\"type\":\"cpu\",\"language\":\"c\",\"duration\":${PROFILE_DURATION},\"hostname\":\"127.0.0.1\"}")
	assert_eq "${status}" "201" "create native CPU profile" \
		|| fatal "profile creation failed: $(< "${response_file}")"
	PROFILE_ID=$(jq -er '.data.id' "${response_file}") \
		|| fatal "profile creation response has no task ID"
	export PROFILE_ID
}

profile_is_running() {
	require_services_alive
	curl -sf "${CURL_TIMEOUT[@]}" -H "Authorization: ${API_USER}" \
		"${APISERVER_ADDR}/v1/profiles/${PROFILE_ID}" \
		| jq -e '.data.status == "running" and (.data.agent_task_id | length > 0)' > /dev/null
}

profiles_are_stored() {
	require_services_alive
	local response_file="${HUATUO_BAMAI_TEST_TMPDIR}/profiles-raw.json"
	local status
	status=$(
		curl -sS "${CURL_TIMEOUT[@]}" -o "${response_file}" -w '%{http_code}' \
			-H "Authorization: ${API_USER}" \
			"${APISERVER_ADDR}/v1/profiles/${PROFILE_ID}/raw"
	) || {
		LAST_PROFILE_DIAGNOSTIC="raw profile request failed before receiving an HTTP response"
		return 1
	}
	local count
	count=$(jq -er '.data.data | length' "${response_file}" 2> /dev/null) || {
		LAST_PROFILE_DIAGNOSTIC="raw profile response status=${status}, invalid body: $(< "${response_file}")"
		return 1
	}
	LAST_PROFILE_DIAGNOSTIC="raw profile response status=${status}, windows=${count}"
	[[ "${status}" == "200" && "${count}" -ge 2 ]]
}

profile_is_completed() {
	require_services_alive
	kill -0 "${TARGET_PID}" 2> /dev/null || fatal "CPU fixture exited while profiling"
	curl -sf "${CURL_TIMEOUT[@]}" -H "Authorization: ${API_USER}" \
		"${APISERVER_ADDR}/v1/profiles/${PROFILE_ID}" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/profile-status.json" || return 1
	local status
	status=$(jq -er '.data.status' "${HUATUO_BAMAI_TEST_TMPDIR}/profile-status.json") \
		|| fatal "profile status response is invalid"
	case "${status}" in
	completed) return 0 ;;
	pending | running) return 1 ;;
	failed | stopped | timeout)
		fatal "profile entered terminal status ${status}: $(jq -c '.data | {status, error_message, tracer_args}' "${HUATUO_BAMAI_TEST_TMPDIR}/profile-status.json")"
		;;
	*) fatal "profile returned unknown status: ${status}" ;;
	esac
}

require_services_alive() {
	local bamai_pid
	bamai_pid=$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" 2> /dev/null || true)
	[[ -n "${bamai_pid}" ]] && kill -0 "${bamai_pid}" 2> /dev/null \
		|| fatal "huatuo-bamai exited while profiling"
	[[ -n "${APISERVER_PID}" ]] && kill -0 "${APISERVER_PID}" 2> /dev/null \
		|| fatal "huatuo-apiserver exited while profiling"
	docker inspect --format '{{.State.Running}}' "${ES_CONTAINER_ID}" 2> /dev/null \
		| grep -qx true || fatal "Elasticsearch exited while profiling"
}

profiles_contain_fixture_stack() {
	jq -e '
	      def function_name($profile; $function_id):
	        ($profile.function[]? | select(.id == $function_id) | .name) as $name_id
	        | $profile.string_table[$name_id];
	      def sample_names($profile; $sample):
	        [$sample.location_id[]? as $location_id
	          | $profile.location[]?
	          | select(.id == $location_id)
	          | .line[0]?.function_id as $function_id
	          | function_name($profile; $function_id)];
	      any(
	        (.data.data[].tracer_data.flamedata.profile as $profile
	          | $profile.sample[]? as $sample
	          | sample_names($profile; $sample) as $names
	          | range(0; (($names | length) - 2)) as $index
	          | {names: $names, index: $index});
	        .index as $index
	          | (.names[$index:$index + 3] == ["f3", "f2", "f1"])
	            or (.names[$index:$index + 3] == ["f1", "f2", "f3"])
	      )
	    ' "${HUATUO_BAMAI_TEST_TMPDIR}/profiles-raw.json" > /dev/null
}

assert_profile_lifecycle() {
	wait_until 10 1 profile_is_running || fatal "profile did not enter running state"

	local status
	status=$(curl -sS "${CURL_TIMEOUT[@]}" \
		-o "${HUATUO_BAMAI_TEST_TMPDIR}/forbidden.json" -w '%{http_code}' \
		-H "Authorization: ${OTHER_USER}" \
		"${APISERVER_ADDR}/v1/profiles/${PROFILE_ID}")
	assert_eq "${status}" "403" "non-owner profile access" \
		|| fatal "profile was visible to a non-owner"

	status=$(curl -sS "${CURL_TIMEOUT[@]}" \
		-o "${HUATUO_BAMAI_TEST_TMPDIR}/delete-running.json" -w '%{http_code}' \
		-X DELETE -H "Authorization: ${API_USER}" \
		"${APISERVER_ADDR}/v1/profiles/${PROFILE_ID}")
	assert_eq "${status}" "409" "delete running profile" \
		|| fatal "running profile deletion did not return conflict"

	wait_until 60 2 profile_is_completed || fatal "profile did not complete"
	jq -e --argjson duration "${PROFILE_DURATION}" \
		'.data.duration == $duration and .data.results.url != ""' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/profile-status.json" > /dev/null \
		|| fatal "completed profile metadata is incomplete"

	wait_until 90 2 profiles_are_stored \
		|| fatal "profiling windows were not stored: ${LAST_PROFILE_DIAGNOSTIC}"
	profiles_contain_fixture_stack \
		|| fatal "stored profiles do not contain the fixture f1, f2, and f3 stack frames"

	curl -sf "${CURL_TIMEOUT[@]}" -H "Authorization: ${API_USER}" \
		"${APISERVER_ADDR}/v1/profiles?type=cpu&host=127.0.0.1&status=completed&limit=1&offset=0" \
		| jq -e --arg id "${PROFILE_ID}" '.data.total >= 1 and .data.items[0].id == $id' \
			> /dev/null || fatal "profile list filters did not return the completed task"

	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o /dev/null -w '%{http_code}' -X DELETE \
		-H "Authorization: ${API_USER}" "${APISERVER_ADDR}/v1/profiles/${PROFILE_ID}")
	assert_eq "${status}" "204" "delete completed profile" \
		|| fatal "completed profile deletion failed"
}

require_environment
start_elasticsearch
write_configs
start_bamai
start_apiserver
start_fixture
assert_api_contract
create_profile
assert_profile_lifecycle
readonly FAILURE_LOG_PATTERN='panic:|fatal|level=(error|panic|fatal)|"level":"(error|panic|fatal)"'
! grep -qiE "${FAILURE_LOG_PATTERN}" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
	|| fatal "huatuo-bamai log contains an unexpected failure"
! grep -qiE "${FAILURE_LOG_PATTERN}" "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" \
	|| fatal "huatuo-apiserver log contains an unexpected failure"
