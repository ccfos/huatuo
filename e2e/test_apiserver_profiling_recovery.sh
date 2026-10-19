#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

source "${ROOT_DIR}/integration/apiserver/lib.sh"

# Replace the runner baseline with the profiling-specific configuration.
huatuo_bamai_stop
start_test_storage
start_node
start_server
continuous_profiling_start_native_cpu_fixture TARGET_PID "${ROOT_DIR}/integration/apiserver/testdata/workload.c"

CURRENT_CASE=graceful-recovery
create 40
first=$JOB_ID
wait_job "$first" running
restarting_pid=$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-apiserver.pid")
kill -TERM "$restarting_pid"
server_exited() { ! kill -0 "$restarting_pid" 2> /dev/null; }
wait_until 20 1 server_exited || fatal 'apiserver did not exit on SIGTERM'
wait "$restarting_pid" || fatal 'apiserver graceful exit returned a failure'
rm "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-apiserver.pid"
curl -fsS "${CURL_TIMEOUT[@]}" -H 'Authorization: Bearer integration-node-token' "$HUATUO_BAMAI_ADDR/v1/operations/$first" > "${HUATUO_BAMAI_TEST_TMPDIR}/operation-before-restart.json"
jq -e '.data.status == "running"' "${HUATUO_BAMAI_TEST_TMPDIR}/operation-before-restart.json" > /dev/null
start_server
verify_result "$first"
request GET /metrics 200
grep -q '^huatuo_apiserver_jobs_recovered_total 1$' "$RESPONSE" || fatal 'recovery counter is not one'
pass
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" apiserver
cat "$SUMMARY"
