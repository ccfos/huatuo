#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
# Verify task limits, failure responses and sample-label queries through live services.
set -euo pipefail

source "${ROOT_DIR}/integration/lib_apiserver.sh"
source "${ROOT_DIR}/integration/lib_apiserver_query.sh"
require_commands python3

assert_sample_label_filters() {
	local job_id=$1 profile_type=process_cpu:cpu:nanoseconds:cpu:nanoseconds
	local selector request_json pid total filtered_total expression
	local result="${HUATUO_BAMAI_TEST_TMPDIR}/sample-labels.json"
	selector=$(jq -nr --arg id "$job_id" --arg type "$profile_type" '
		"{scope=\"host\",id=" + ($id|tojson) + ",__profile_type__=" + ($type|tojson) + "}"')
	request_json=$(jq -nc --arg selector "$selector" '{matchers:[$selector],end:"4102444800000"}')
	assert_profile_query "$APISERVER_ADDR" "$API_TOKEN" LabelNames "$request_json" "$result"
	for expression in pid comm cpu; do
		assert_eq "$(jq --arg name "$expression" '.names | index($name) != null' "$result")" \
			true "sample label $expression" || fatal "sample label $expression is missing"
	done
	request_json=$(jq -nc --arg selector "$selector" '{name:"pid",matchers:[$selector],end:"4102444800000"}')
	assert_profile_query "$APISERVER_ADDR" "$API_TOKEN" LabelValues "$request_json" "$result"
	pid=$(jq -r '.names[0] // empty' "$result")
	assert_eq "$(jq '.names | length > 0' "$result")" true "sample PID values" || fatal "PID label has no values"
	total=$(jq -er '.flamegraph.total' "${HUATUO_BAMAI_TEST_TMPDIR}/flamegraph-${job_id}.json")
	for expression in "pid=\"${pid}\"" 'future_label="unrecorded"'; do
		request_json=$(jq -nc --arg type "$profile_type" --arg id "$job_id" --arg filter "$expression" '
			{profileTypeID:$type,labelSelector:("{scope=\"host\",id="+($id|tojson)+","+$filter+"}"),end:"4102444800000"}')
		assert_profile_query "$APISERVER_ADDR" "$API_TOKEN" SelectMergeStacktraces "$request_json" "$result"
		filtered_total=$(jq -er '.flamegraph.total' "$result")
		assert_eq "$(jq -n --argjson total "$total" --argjson filtered "$filtered_total" \
			'$filtered >= 0 and $filtered <= $total')" true "filtered sample weight" \
			|| fatal "filtered weight exceeds the unfiltered weight"
		if [[ $expression == future_label=* ]]; then
			assert_eq "$filtered_total" 0 "unknown sample label" || fatal "unknown label matched samples"
		else
			assert_eq "$(jq --argjson total "$filtered_total" -n '$total > 0')" true "selected PID sample weight" || fatal "selected PID has no samples"
		fi
	done
}

assert_no_runtime_panics() {
	local status=0
	# Failure scenarios intentionally emit error logs.
	grep -Ein 'panic:|fatal error:' "${HUATUO_BAMAI_TEST_TMPDIR}"/apiserver*.log \
		"${HUATUO_BAMAI_TEST_TMPDIR}"/bamai-*.log "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" >&2 || status=$?
	case $status in
	1) ;;
	0) fatal "runtime panic found in service logs" ;;
	*) fatal "cannot scan service logs: grep exited $status" ;;
	esac
}

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
	CURRENT_CASE=sample-label-filters
	assert_sample_label_filters "$completed"
	pass
	CURRENT_CASE=invalid-requests
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
continuous_profiling_start_native_cpu_fixture TARGET_PID "${ROOT_DIR}/integration/testdata/test_apiserver_profiling_workload.c"
functional_cases
assert_no_runtime_panics
cat "$SUMMARY"
