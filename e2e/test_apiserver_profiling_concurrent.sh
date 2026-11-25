#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

source "${ROOT_DIR}/integration/lib_apiserver.sh"

# Replace the runner baseline with the profiling-specific configuration.
huatuo_bamai_stop
start_test_storage
start_node
start_server
continuous_profiling_start_native_cpu_fixture TARGET_PID "${ROOT_DIR}/integration/testdata/test_apiserver_profiling_workload.c"

CURRENT_CASE=same-host-concurrency
create 15
first=$JOB_ID
create 15
second=$JOB_ID
wait_job "$first" running
wait_job "$second" running
# Both must still be running at the same observation point.
continuous_profile_status_is "$first" running "${HUATUO_BAMAI_TEST_TMPDIR}/concurrent-first.json" || fatal 'first job finished before concurrent execution'
wait_job "$first" completed
profiling_results_fetch "$first" "$HUATUO_BAMAI_TEST_TMPDIR" 'process_cpu:cpu:nanoseconds:cpu:nanoseconds' \
	|| fatal "failed to fetch profiling results"
jq -e .data "$HUATUO_BAMAI_TEST_TMPDIR/raw-$first.json" | assert_profile_windows - 2 'process_cpu:cpu:nanoseconds:cpu:nanoseconds' \
	|| fatal "invalid profiling windows; inspect $HUATUO_BAMAI_TEST_TMPDIR/raw-$first.json"
assert_profile_flamegraph "$HUATUO_BAMAI_TEST_TMPDIR/flamegraph-$first.json" \
	|| fatal "profiling flamegraph validation failed"
wait_job "$second" completed
profiling_results_fetch "$second" "$HUATUO_BAMAI_TEST_TMPDIR" 'process_cpu:cpu:nanoseconds:cpu:nanoseconds' \
	|| fatal "failed to fetch profiling results"
jq -e .data "$HUATUO_BAMAI_TEST_TMPDIR/raw-$second.json" | assert_profile_windows - 2 'process_cpu:cpu:nanoseconds:cpu:nanoseconds' \
	|| fatal "invalid profiling windows; inspect $HUATUO_BAMAI_TEST_TMPDIR/raw-$second.json"
assert_profile_flamegraph "$HUATUO_BAMAI_TEST_TMPDIR/flamegraph-$second.json" \
	|| fatal "profiling flamegraph validation failed"
pass
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" apiserver
cat "$SUMMARY"
