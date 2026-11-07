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
verify_result "$first"
verify_result "$second"
pass
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" apiserver
cat "$SUMMARY"
