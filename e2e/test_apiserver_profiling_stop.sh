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

CURRENT_CASE=stop-and-result-access
create 60
wait_job "$JOB_ID" running
request GET "/v1/profiling/$JOB_ID/raw" 409
assert_error result_not_ready
stop_job "$JOB_ID"
request GET "/v1/profiling/$JOB_ID/raw" 409
assert_error result_unavailable
pass
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.log" apiserver
cat "$SUMMARY"
