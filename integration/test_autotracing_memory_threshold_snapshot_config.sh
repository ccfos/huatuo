#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Validate configuration through the built daemon, including its startup path.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
require_commands timeout ss
readonly SNAPSHOT_CONFIG_WORKSPACE=${HUATUO_BAMAI_TEST_TMPDIR}
trap huatuo_bamai_stop EXIT

write_snapshot_configuration() {
	write_default_config
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << CONFIG
[HTTPServer]
 ListenAddress = "127.0.0.1:${snapshot_config_port}"
[AutoTracing.MemoryThresholdSnapshot]
CONFIG
	printf '%b\n' "${snapshot_config_body}" >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
}

assert_snapshot_configuration_rejected() {
	local status=$1 expected=$2 file=$3
	assert_eq "${status}" 1 "invalid ${snapshot_config_case} exit status" \
		|| fatal "invalid ${snapshot_config_case} did not fail promptly (status=${status})"
	grep -Fq "${expected}" "${file}" || fatal "missing ${snapshot_config_case} diagnostic: expected ${expected}"
	! grep -q 'started successfully' "${file}" || fatal "daemon started with invalid ${snapshot_config_case}"
}

assert_snapshot_configuration_started() {
	grep -Fq 'started successfully' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "valid ${snapshot_config_case} did not finish startup"
	huatuo_bamai_log_check || fatal "valid ${snapshot_config_case} reported an error"
}

while IFS='|' read -r snapshot_config_case snapshot_config_body snapshot_config_error; do
	HUATUO_BAMAI_TEST_TMPDIR="${SNAPSHOT_CONFIG_WORKSPACE}/${snapshot_config_case}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	snapshot_config_port=$(allocate_available_port)
	HUATUO_BAMAI_ADDR="http://127.0.0.1:${snapshot_config_port}"
	HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
	if [[ -z ${snapshot_config_error} ]]; then
		integration_huatuo_bamai_start write_snapshot_configuration \
			--region integration --disable-storage --disable-kubelet --log-debug
		wait_until 20 .1 grep -Fq "started successfully" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
			|| fatal "valid ${snapshot_config_case} did not finish startup"
		assert_huatuo_bamai_shutdown
		assert_snapshot_configuration_started
	else
		write_snapshot_configuration
		snapshot_config_status=0
		timeout --kill-after=2s 10s "${HUATUO_BAMAI_BIN}" \
			--config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" --config bamai.conf \
			--region integration --disable-storage --disable-kubelet \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/startup.log" 2>&1 || snapshot_config_status=$?
		assert_snapshot_configuration_rejected "${snapshot_config_status}" \
			"${snapshot_config_error}" "${HUATUO_BAMAI_TEST_TMPDIR}/startup.log"
	fi
	log_info "PASS: daemon configuration ${snapshot_config_case}"
done << 'CASES'
minimum|ThresholdPercent = 1\nIntervalTracing = 1\nRunTracingToolTimeout = 1\nMaxMemoryObjectEntries = 1|
maximum|ThresholdPercent = 100\nMaxMemoryObjectEntries = 100|
threshold-zero|ThresholdPercent = 0|threshold percent
interval-zero|IntervalTracing = 0|tracing interval seconds
timeout-zero|RunTracingToolTimeout = 0|tracing tool timeout seconds
entries-zero|MaxMemoryObjectEntries = 0|maximum memory object entries
threshold-negative|ThresholdPercent = -1|threshold percent
threshold-over|ThresholdPercent = 101|threshold percent
interval-negative|IntervalTracing = -1|tracing interval seconds
timeout-negative|RunTracingToolTimeout = -1|tracing tool timeout seconds
timeout-overflow|RunTracingToolTimeout = 9223372036854775807|overflows
entries-negative|MaxMemoryObjectEntries = -1|maximum memory object entries
entries-over|MaxMemoryObjectEntries = 101|maximum memory object entries
wrong-type|ThresholdPercent = "invalid"|loading config
CASES
