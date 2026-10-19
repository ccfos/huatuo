#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

MODE=$1
source "${ROOT_DIR}/integration/apiserver/lib.sh"

static_cases() {
	CURRENT_CASE=capabilities-and-no-result-storage
	WITH_STORAGE=false
	start_server
	"$PROBE" capabilities "$APISERVER_ADDR" > "${HUATUO_BAMAI_TEST_TMPDIR}/capabilities.json"
	request GET /readyz 204
	request GET /v1/profiling 200
	jq -e '.data.items == []' "$RESPONSE" > /dev/null
	local status
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -o "${HUATUO_BAMAI_TEST_TMPDIR}/unauthenticated.json" -w '%{http_code}' "$APISERVER_ADDR/v1/profiling")
	assert_eq "$status" 401 || fatal 'profiling endpoint accepted an unauthenticated request'
	huatuo_apiserver_stop
	WITH_STORAGE=true
	pass
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

static_cases
CURRENT_CASE=stack-startup
start_test_storage
start_node
start_server
# Keep the load alive throughout recovery and long-running cases.
compile_user_fixture "${ROOT_DIR}/integration/apiserver/testdata/workload.c" "${HUATUO_BAMAI_TEST_TMPDIR}/callchain"
"${HUATUO_BAMAI_TEST_TMPDIR}/callchain" &
TARGET_PID=$!
case "$MODE" in
functional) functional_cases ;;
load | soak)
	CURRENT_CASE="$MODE"
	create
	verify_result "$JOB_ID"
	python3 "${ROOT_DIR}/integration/apiserver/load.py" --mode "$MODE" --url "$APISERVER_ADDR" \
		--job "$JOB_ID" --output "$HUATUO_BAMAI_TEST_TMPDIR" \
		--seconds "${APISERVER_TEST_SECONDS:-$([[ $MODE == load ]] && echo 600 || echo 86400)}" \
		--clients "${APISERVER_TEST_CLIENTS:-100}" --rps "${APISERVER_TEST_RPS:-100}" \
		--max-rss-growth-mib "${APISERVER_TEST_MAX_RSS_MIB:-256}" \
		--max-fd-growth "${APISERVER_TEST_MAX_FDS:-32}" \
		--max-goroutine-growth "${APISERVER_TEST_MAX_GOROUTINES:-64}" &
	LOAD_PID=$!
	wait "$LOAD_PID"
	LOAD_PID=""
	pass
	;;
esac
# Injected failures may log errors; runtime panics must never be accepted.
if grep -Eiq 'panic:|fatal error:' "${HUATUO_BAMAI_TEST_TMPDIR}"/*.log; then fatal 'runtime panic found in service logs'; fi
cat "$SUMMARY"
