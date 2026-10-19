#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

source "${ROOT_DIR}/integration/apiserver/lib.sh"

# The e2e runner starts a baseline daemon; this case needs profiling storage.
huatuo_bamai_stop
start_test_storage
start_node
start_server
continuous_profiling_start_native_cpu_fixture TARGET_PID "${ROOT_DIR}/integration/apiserver/testdata/workload.c"

CURRENT_CASE=lifecycle-list-raw-metrics
create
completed=$JOB_ID
verify_result "$completed"
request GET '/v1/profiling?limit=1&offset=0' 200
jq -e --arg id "$completed" '.data.items[0].request_id == $id and .data.limit == 1' "$RESPONSE" > /dev/null
request GET "/v1/profiling/$completed/raw?limit=1&offset=0" 200
jq -e '.data.limit == 1 and .data.offset == 0 and .data.has_more == true and (.data.items | length) == 1' "$RESPONSE" > /dev/null
request GET "/v1/profiling/$completed/raw?limit=1&offset=1" 200
jq -e '.data.offset == 1 and (.data.items | length) == 1' "$RESPONSE" > /dev/null
request GET /metrics 200
grep -Eq '^huatuo_apiserver_agent_requests_total\{.*\} [1-9]' "$RESPONSE" || fatal 'agent request counter did not increase'
grep -Eq '^huatuo_apiserver_agent_request_duration_seconds_count\{.*\} [1-9]' "$RESPONSE" || fatal 'agent latency observations missing'
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" apiserver
pass
cat "$SUMMARY"
