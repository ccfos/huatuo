#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

source "${ROOT_DIR}/integration/lib_apiserver.sh"

assert_node_operation_running() {
	local id=$1 phase=$2
	local file="${HUATUO_BAMAI_TEST_TMPDIR}/operation-${phase}.json"
	local status actual curl_status=0 error_file="${file%.json}.curl-error"
	: > "$file"
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -H 'Authorization: Bearer integration-node-token' \
		-o "$file" -w '%{http_code}' "$HUATUO_BAMAI_ADDR/v1/operations/$id" 2> "$error_file") || curl_status=$?
	log_info "Node operation $id ($phase): curl exit=$curl_status, HTTP status=$status"
	report_http_response "Node operation $id ($phase)" "$file" "$error_file"
	[[ $curl_status -eq 0 ]] || fatal "Node operation $id ($phase): curl exited $curl_status"
	assert_eq "$status" 200 "Node operation $id ($phase) HTTP status" \
		|| fatal "Node operation $id ($phase): HTTP status $status, expected 200"
	actual=$(jq -er '.data.status' "$file") \
		|| fatal "Node operation $id ($phase): missing or invalid status, expected running"
	assert_eq "$actual" running "Node operation $id ($phase) status" \
		|| fatal "Node operation $id ($phase): expected running, got $actual"
}

# Replace the runner baseline with the profiling-specific configuration.
huatuo_bamai_stop
start_test_storage
start_node
start_server
continuous_profiling_start_native_cpu_fixture TARGET_PID "${ROOT_DIR}/integration/testdata/test_apiserver_profiling_workload.c"

CURRENT_CASE=graceful-recovery
create 30
first=$JOB_ID
wait_job "$first" running
assert_huatuo_apiserver_shutdown_with_signal TERM
assert_node_operation_running "$first" before-restart
start_server
assert_node_operation_running "$first" after-restart
verify_result "$first"
request GET /metrics 200
grep -q '^huatuo_apiserver_jobs_recovered_total 1$' "$RESPONSE" || fatal 'recovery counter is not one'
pass
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" apiserver
cat "$SUMMARY"
