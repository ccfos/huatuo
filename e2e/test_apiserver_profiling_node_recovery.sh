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

CURRENT_CASE=node-dies-during-sampling
create 60
wait_job "$JOB_ID" running
pid=$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
mapfile -t ORPHAN_PIDS < <(ps -eo pid=,ppid=,pgid= | awk -v parent="$pid" '$2 == parent && $1 == $3 {print $1}')
assert_huatuo_bamai_shutdown_with_signal KILL
wait_job "$JOB_ID" failed
assert_reason "$JOB_ID" node_unavailable
stop_orphans
start_node
create
verify_result "$JOB_ID"
pass
# Node loss intentionally logs failures; runtime panics must still fail.
if grep -Eiq 'panic:|fatal error:' "${HUATUO_BAMAI_TEST_TMPDIR}"/*.log; then fatal 'runtime panic found in service logs'; fi
cat "$SUMMARY"
