#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

source "${ROOT_DIR}/integration/lib_apiserver.sh"

# The e2e runner starts a baseline daemon; this case needs profiling storage.
huatuo_bamai_stop
start_test_storage
start_node
start_server
continuous_profiling_start_native_cpu_fixture TARGET_PID "${ROOT_DIR}/integration/testdata/test_apiserver_profiling_workload.c"

CURRENT_CASE=lifecycle-list-raw-metrics
create
completed=$JOB_ID
create
second=$JOB_ID
verify_result "$completed"
verify_result "$second"
# A valid on-CPU result must not satisfy an off-CPU matrix entry.
if (
	trap - EXIT
	verify_result "$completed" 2 process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds
) > "${HUATUO_BAMAI_TEST_TMPDIR}/wrong-profile-type.log" 2>&1; then
	fatal 'on-CPU result accepted as off-CPU'
fi
grep -q 'unexpected profile type' "${HUATUO_BAMAI_TEST_TMPDIR}/wrong-profile-type.log" || fatal 'profile type rejection failed for an unrelated reason'
for offset in 0 1; do
	if ((offset == 0)); then expected=$second; else expected=$completed; fi
	request GET "/v1/profiling?limit=1&offset=$offset" 200
	jq -e --arg id "$expected" --argjson offset "$offset" '
		(.data.items | length) == 1 and .data.items[0].request_id == $id and
		.data.limit == 1 and .data.offset == $offset and .data.has_more == ($offset == 0)
	' "$RESPONSE" > /dev/null || fatal "incorrect job page at offset $offset"
done
raw="${HUATUO_BAMAI_TEST_TMPDIR}/raw-${completed}.json"
jq -e '.data.items[0].uploaded_timestamp != .data.items[1].uploaded_timestamp' "$raw" > /dev/null || fatal 'raw windows need distinct timestamps for pagination'
for offset in 0 1; do
	request GET "/v1/profiling/$completed/raw?limit=1&offset=$offset" 200
	cp "$RESPONSE" "${HUATUO_BAMAI_TEST_TMPDIR}/raw-${completed}-page-${offset}.json"
	jq -e --slurpfile full "$raw" --argjson offset "$offset" '
		.data.limit == 1 and .data.offset == $offset and
		.data.has_more == (($full[0].data.items | length) > ($offset + 1)) and
		.data.items == [$full[0].data.items[$offset]]
	' "$RESPONSE" > /dev/null || fatal "incorrect raw page at offset $offset"
done
request GET /metrics 200
grep -Eq '^huatuo_apiserver_agent_requests_total\{.*\} [1-9]' "$RESPONSE" || fatal 'agent request counter did not increase'
grep -Eq '^huatuo_apiserver_agent_request_duration_seconds_count\{.*\} [1-9]' "$RESPONSE" || fatal 'agent latency observations missing'
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" apiserver
pass
cat "$SUMMARY"
