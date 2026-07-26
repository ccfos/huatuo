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

# Verify cpusys end-to-end with deterministic proc/stat samples. Real CPU
# workloads cannot reliably produce a system-time spike on differently sized
# CI hosts, so only perf output is stubbed while the daemon, sampling loop,
# threshold decision, and local persistence remain real.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

command -v jq > /dev/null || skip "jq command is not installed"
[[ -x "${HUATUO_BAMAI_BIN}" ]] \
	|| fatal "huatuo-bamai binary missing: ${HUATUO_BAMAI_BIN}"

readonly CPUSYS_FIXTURE_ROOT="${HUATUO_BAMAI_TEST_TMPDIR}/cpusys-fixture"
readonly CPUSYS_PROC_STAT="${CPUSYS_FIXTURE_ROOT}/proc/stat"
readonly CPUSYS_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/cpusys"
export CPUSYS_PERF_CALLS_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/perf.calls"

mkdir -p "${CPUSYS_FIXTURE_ROOT}/proc" "${CPUSYS_FIXTURE_ROOT}/tools"
# Sample 1: user=100, nice=0, system=100, idle=800, total=1000.
# The daemon reads this initial value before starting its sampling ticker.
cp "${HUATUO_BAMAI_TEST_FIXTURES}/proc/stat" "${CPUSYS_PROC_STAT}"
cp "${HUATUO_BAMAI_TEST_FIXTURES}/tools/perf" "${CPUSYS_FIXTURE_ROOT}/tools/perf"

# $1 contains cumulative user, nice, system, and idle ticks in /proc/stat
# order. cpusys calculates total as their sum and system usage from deltas.
hold_cpu_sample() {
	local counters=$1
	printf 'cpu %s\n' "${counters}" > "${CPUSYS_PROC_STAT}"
	# Hold each value across at least two one-second sampling ticks so the
	# result does not depend on where the update lands relative to a tick.
	sleep 3
}

perf_has_run() {
	if [[ ! -s "${CPUSYS_PERF_CALLS_FILE}" ]]; then
		log_info "cpusys perf invocation count: 0"
		return 1
	fi

	local call_count
	call_count=$(wc -l < "${CPUSYS_PERF_CALLS_FILE}")
	log_info "cpusys perf invocation count: ${call_count}"
	return 0
}

cpusys_event_is_valid() {
	[[ -s "${CPUSYS_EVENT}" ]] || return 1
	jq -e '
		.tracer_name == "cpusys"
		and .tracer_type == "autotracing"
		and .tracer_data.system_percent == 80
		and .tracer_data.system_percent_threshold == 45
		and .tracer_data.system_percent_delta == 70
		and .tracer_data.system_percent_delta_threshold == 20
		and .tracer_data.flamedata == [{
			"level": 0,
			"value": 1,
			"self": 1,
			"label": "fixture_system_work"
		}]
	' "${CPUSYS_EVENT}" > /dev/null
}

integration_huatuo_bamai_start \
	write_cpusys_autotracing_config \
	--region dev \
	--procfs-prefix "${CPUSYS_FIXTURE_ROOT}" \
	--tools-bin-dir "${CPUSYS_FIXTURE_ROOT}/tools" \
	--disable-kubelet \
	--disable-cgroup \
	--log-debug

# Sample 2: total=1100 and system=110. The 10/100 delta establishes a 10%
# system-time baseline.
hold_cpu_sample "190 0 110 800"

# Sample 3: total=100 and system=10. This simulates host reboot, counter reset,
# or proc source replacement. It must reset history without triggering perf.
hold_cpu_sample "10 0 10 80"
perf_has_run && fatal "cpusys triggered when CPU counters moved backwards"

# Sample 4: total=200 and system=90. Its 80/100 delta establishes the first
# post-rollback percentage; it must not be compared with pre-rollback history.
hold_cpu_sample "30 0 90 80"
perf_has_run && fatal "cpusys triggered before rebuilding the post-rollback baseline"

# Sample 5: total=300 and system=100, moving the system percentage down to 10%.
hold_cpu_sample "120 0 100 80"

# Sample 6: total=400 and system=180. The percentage rises from 10% to 80%,
# exceeding the 45% usage and 20-point delta thresholds.
hold_cpu_sample "140 0 180 80"

wait_until 10 1 perf_has_run || fatal "cpusys did not trigger perf"
wait_until 10 1 cpusys_event_is_valid \
	|| fatal "cpusys did not persist the expected autotracing event"

call_count=$(wc -l < "${CPUSYS_PERF_CALLS_FILE}")
[[ "${call_count}" -eq 1 ]] \
	|| fatal "cpusys invoked perf ${call_count} times, expected once"
