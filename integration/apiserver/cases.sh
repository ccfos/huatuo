#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

source "${ROOT_DIR}/integration/apiserver/lib.sh"

functional_cases() {
	local quota first
	for quota in host global; do
		CURRENT_CASE="apiserver-${quota}-quota"
		huatuo_apiserver_stop
		MAX_HOST=3
		MAX_TOTAL=500
		if [[ $quota == host ]]; then MAX_HOST=1; else MAX_TOTAL=1; fi
		start_server
		create 60
		first=$JOB_ID
		request POST /v1/profiling 429 '{"hostname":"127.0.0.1","duration_seconds":12,"scope":"host","type":"cpu","language":"c","mode":"oncpu"}'
		assert_error quota_exceeded
		stop_job "$first"
		pass
	done
	huatuo_apiserver_stop
	MAX_HOST=3
	MAX_TOTAL=500
	start_server

	CURRENT_CASE=node-capacity
	huatuo_bamai_stop
	NODE_CAPACITY=1
	start_node
	create 60
	first=$JOB_ID
	wait_job "$first" running
	create 12
	wait_job "$JOB_ID" failed
	assert_reason "$JOB_ID" execution_capacity_exceeded
	stop_job "$first"
	pass

	CURRENT_CASE=invalid-requests
	# Pagination validation needs a completed task after lifecycle cases moved to e2e.
	create
	local completed=$JOB_ID
	verify_result "$completed"
	local extra body
	for extra in '{"duration_seconds":0}' '{"duration_seconds":-1}' '{"duration_seconds":9223372036854775807}' \
		'{"language":"python","type":"memory","mode":"object_alloc"}' '{"mode":"object_alloc"}' \
		'{"scope":"container"}' '{"hostname":""}' '{"type":"memory","mode":"virtual_alloc"}' '{"language":"java"}'; do
		body=$(jq -nc --argjson extra "$extra" '{hostname:"127.0.0.1",duration_seconds:12,scope:"host",type:"cpu",language:"c",mode:"oncpu"}+$extra')
		request POST /v1/profiling 400 "$body"
		assert_error invalid_request
	done
	request POST /v1/profiling 400 '{'
	request GET '/v1/profiling?limit=0' 400
	request GET "/v1/profiling/$completed/raw?limit=0" 400
	request GET /v1/profiling/does-not-exist 404
	create 43200
	wait_job "$JOB_ID" running
	stop_job "$JOB_ID"
	pass

	CURRENT_CASE=missing-container
	create 12 '{"scope":"container","container_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'
	wait_job "$JOB_ID" failed
	assert_reason "$JOB_ID" execution_start_failed
	pass
	CURRENT_CASE=unreachable-host
	# No server is bound here; avoids DNS and external network dependencies.
	create 12 '{"hostname":"127.0.0.2"}'
	wait_job "$JOB_ID" failed
	assert_reason "$JOB_ID" node_unavailable
	pass
	CURRENT_CASE=node-offline-before-create
	huatuo_bamai_stop
	create
	wait_job "$JOB_ID" failed
	assert_reason "$JOB_ID" node_unavailable
	start_node
	pass
}

CURRENT_CASE=stack-startup
start_test_storage
start_node
start_server
# Keep the workload alive while validating profiling results.
compile_user_fixture "${ROOT_DIR}/integration/testdata/test_apiserver_profiling_workload.c" "${HUATUO_BAMAI_TEST_TMPDIR}/callchain"
"${HUATUO_BAMAI_TEST_TMPDIR}/callchain" &
TARGET_PID=$!
functional_cases
# Injected failures may log errors; runtime panics must never be accepted.
if grep -Eiq 'panic:|fatal error:' "${HUATUO_BAMAI_TEST_TMPDIR}"/*.log; then fatal 'runtime panic found in service logs'; fi
cat "$SUMMARY"
