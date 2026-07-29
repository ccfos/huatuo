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

# Verify that graceful apiserver shutdown leaves an active Agent task running
# and that a replacement apiserver recovers the persisted job.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_continuous_profiling.sh"

readonly ES_PASSWORD="huatuo-integration"
readonly API_TOKEN="integration-admin"
readonly OTHER_API_TOKEN="integration-other"
readonly PROFILE_DURATION=30
readonly PROFILE_INTERVAL=5

continuous_profiling_requirements

APISERVER_PORT=$(allocate_available_port) \
	|| fatal "failed to allocate an apiserver port"
readonly APISERVER_PORT
readonly APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"
readonly AGENT_TASKS_ADDR="${HUATUO_BAMAI_ADDR}/tasks"
readonly PROFILE_CREATE_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/create-profile.json"
readonly PROFILE_STATUS_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/profile-status.json"

TARGET_PID=""
PROFILE_ID=""
STOPPING_APISERVER_PID=""

cleanup() {
	local status=$?
	continuous_profiling_cleanup "${status}" "${TARGET_PID}"
}
trap cleanup EXIT

agent_task_status_is() {
	local expected_status=$1
	curl -sf "${CURL_TIMEOUT[@]}" \
		"${AGENT_TASKS_ADDR}/${PROFILE_ID}" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/agent-task-status.json" \
		|| return 1
	jq -e --arg expected_status "${expected_status}" \
		'.data.status == $expected_status' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/agent-task-status.json" > /dev/null
}

apiserver_process_exited() {
	! kill -0 "${STOPPING_APISERVER_PID}" 2> /dev/null
}

stop_apiserver_for_restart() {
	STOPPING_APISERVER_PID=$(
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-apiserver.pid"
	) || fatal "apiserver pid file is missing"

	kill -TERM "${STOPPING_APISERVER_PID}" \
		|| fatal "failed to signal apiserver pid=${STOPPING_APISERVER_PID}"
	wait_until 20 1 apiserver_process_exited \
		|| fatal "apiserver did not exit gracefully"

	local exit_status=0
	wait "${STOPPING_APISERVER_PID}" || exit_status=$?
	assert_eq "${exit_status}" "0" "graceful apiserver exit" \
		|| fatal "apiserver exited with status ${exit_status}"

	rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-apiserver.pid"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/apiserver-before-restart.log"
}

recovered_job_metric_is_one() {
	curl -sf "${CURL_TIMEOUT[@]}" "${APISERVER_ADDR}/metrics" \
		| grep -q '^huatuo_apiserver_jobs_recovered_total 1$'
}

assert_shutdown_log() {
	local log_file="${HUATUO_BAMAI_TEST_TMPDIR}/apiserver-before-restart.log"
	grep -Fq "leaving active job running during manager shutdown" "${log_file}" \
		|| fatal "shutdown log did not report the active job"
	grep -Fq "active jobs will be recovered by the next manager" "${log_file}" \
		|| fatal "shutdown log did not report recovery intent"
	grep -Fq "${PROFILE_ID}" "${log_file}" \
		|| fatal "shutdown log did not include job ID ${PROFILE_ID}"
}

continuous_profiling_start_stack --disable-cgroup

continuous_profiling_start_native_cpu_fixture TARGET_PID
continuous_profile_create_cpu "${PROFILE_CREATE_RESPONSE}" "${PROFILE_DURATION}"
PROFILE_ID=$(jq -er '.data.id' "${PROFILE_CREATE_RESPONSE}") \
	|| fatal "profile creation response has no job ID"
log_info "created profile job: ${PROFILE_ID}"
wait_until 10 1 continuous_profile_status_is \
	"${PROFILE_ID}" running "${PROFILE_STATUS_RESPONSE}" \
	|| fatal "profile did not enter running state"
wait_until 10 1 agent_task_status_is running \
	|| fatal "Agent task did not enter running state"

stop_apiserver_for_restart
assert_shutdown_log
agent_task_status_is running \
	|| fatal "graceful apiserver shutdown stopped the Agent task"

integration_huatuo_apiserver_start \
	write_continuous_profiling_apiserver_config \
	--disable-cgroup
wait_until 10 1 recovered_job_metric_is_one \
	|| fatal "replacement apiserver did not report one recovered job"
wait_until 10 1 continuous_profile_status_is \
	"${PROFILE_ID}" running "${PROFILE_STATUS_RESPONSE}" \
	|| fatal "recovered profile was not running"
wait_until 60 2 continuous_profile_status_is \
	"${PROFILE_ID}" completed "${PROFILE_STATUS_RESPONSE}" \
	|| fatal "recovered profile did not complete"

assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/apiserver-before-restart.log" \
	"first huatuo-apiserver"
assert_log_has_no_failure \
	"${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" \
	"replacement huatuo-apiserver"
