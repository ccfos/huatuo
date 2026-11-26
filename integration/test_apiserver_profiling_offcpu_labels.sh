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

# Verify live off-CPU label queries against a workload PID in the host namespace.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler.sh"
require_native_pid_profiling
source "${ROOT_DIR}/integration/lib_apiserver.sh"
source "${ROOT_DIR}/integration/lib_apiserver_query.sh"

exercise_offcpu_reason_filters() {
	local profile_type=process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds
	local job_id selector request_json
	local result="${HUATUO_BAMAI_TEST_TMPDIR}/offcpu-labels.json"
	continuous_profiling_start_native_cpu_fixture TARGET_PID "${ROOT_DIR}/integration/testdata/test_profiler_offcpu.user.c"
	create 12 '{"mode":"offcpu"}'
	job_id=$JOB_ID
	wait_job "$job_id" completed
	profiling_results_fetch "$job_id" "$HUATUO_BAMAI_TEST_TMPDIR" "$profile_type" \
		|| fatal "failed to fetch profiling results"
	jq -e .data "$HUATUO_BAMAI_TEST_TMPDIR/raw-$job_id.json" | assert_profile_windows - 2 "$profile_type" \
		|| fatal "invalid profiling windows; inspect $HUATUO_BAMAI_TEST_TMPDIR/raw-$job_id.json"
	assert_profile_flamegraph "$HUATUO_BAMAI_TEST_TMPDIR/flamegraph-$job_id.json" \
		|| fatal "profiling flamegraph validation failed"
	selector=$(jq -nr --arg id "$job_id" --arg type "$profile_type" --arg pid "$TARGET_PID" \
		'"{scope=\"host\",id="+($id|tojson)+",pid="+($pid|tojson)+",__profile_type__="+($type|tojson)+"}"')
	request_json=$(jq -nc --arg s "$selector" '{matchers:[$s],end:"4102444800000"}')
	assert_profile_query "$APISERVER_ADDR" "$API_TOKEN" LabelNames "$request_json" "$result"
	assert_eq "$(jq '.names | index("offcpu_reason") != null' "$result")" true \
		"off-CPU reason label" || fatal "offcpu_reason is missing from collected samples"
	request_json=$(jq -nc --arg s "$selector" '{name:"offcpu_reason",matchers:[$s],end:"4102444800000"}')
	assert_profile_query "$APISERVER_ADDR" "$API_TOKEN" LabelValues "$request_json" "$result"
	assert_eq "$(jq '.names | index("blocked") != null' "$result")" true \
		"blocked reason" || fatal "offcpu_reason has no blocked samples"
	request_json=$(jq -nc --arg id "$job_id" --arg type "$profile_type" --arg pid "$TARGET_PID" \
		'{profileTypeID:$type,labelSelector:("{scope=\"host\",id="+($id|tojson)+",pid="+($pid|tojson)+",offcpu_reason=\"blocked\"}"),end:"4102444800000"}')
	assert_profile_query "$APISERVER_ADDR" "$API_TOKEN" SelectMergeStacktraces "$request_json" "$result"
	assert_eq "$(jq '.flamegraph.total | tonumber > 0' "$result")" true \
		"blocked nanoseconds" || fatal "offcpu_reason=blocked selected no collected samples"
}

CURRENT_CASE=stack-startup
start_test_storage
start_node
start_server
CURRENT_CASE=offcpu-reason-filters
exercise_offcpu_reason_filters
pass
assert_no_runtime_panics || fatal "service runtime log validation failed"
cat "$SUMMARY"
