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

require_commands jq

readonly CPUSYS_FIXTURE_ROOT="${HUATUO_BAMAI_TEST_TMPDIR}/cpusys-fixture"
readonly CPUSYS_PROC_STAT="${CPUSYS_FIXTURE_ROOT}/proc/stat"
readonly CPUSYS_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/cpusys"
export CPUSYS_PERF_CALLS_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/perf.calls"

mkdir -p "${CPUSYS_FIXTURE_ROOT}/proc" "${CPUSYS_FIXTURE_ROOT}/sys" "${CPUSYS_FIXTURE_ROOT}/tools"
# Sample 1: user=100, nice=0, system=100, idle=800, total=1000.
# The daemon reads this initial value before starting its sampling ticker.
cp "${HUATUO_BAMAI_TEST_FIXTURES}/proc/stat" "${CPUSYS_PROC_STAT}"
cp "${ROOT_DIR}/integration/testdata/tools/test_perf.sh" "${CPUSYS_FIXTURE_ROOT}/tools/perf"

# $1 contains cumulative user, nice, system, and idle ticks in /proc/stat
# order. cpusys calculates total as their sum and system usage from deltas.
hold_cpu_sample() {
	local counters=$1
	printf 'cpu %s\n' "${counters}" > "${CPUSYS_PROC_STAT}.next"
	mv "${CPUSYS_PROC_STAT}.next" "${CPUSYS_PROC_STAT}"
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
	write_config_bamai_cpusys_autotracing \
	--region dev \
	--procfs-prefix "${CPUSYS_FIXTURE_ROOT}" \
	--tools-bin-dir "${CPUSYS_FIXTURE_ROOT}/tools" \
	--disable-kubelet \
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

# Exercise each new trigger independently through the same daemon and store.
# Keep the real workload case separate so these contracts also run off-VM.
write_config_additional_cpu() {
	write_config_bamai_cpusys_autotracing | awk -v mode="${cpusys_mode}" '
 /SysThreshold = 45/ { sub(/45/, "100") }
 /DeltaSysThreshold = 20/ { sub(/20/, "100") }
 /UserThreshold = 100/ { sub(/100/, mode == "user" ? 75 : 100) }
 /UsageThreshold = 100/ { sub(/100/, mode == "total" ? 75 : 100) }
 { print }
 /\[AutoTracing.CPUSys\]/ {
   print "    IntervalTracing = 1800"
 }'
}
assert_additional_cpu_event() {
	[[ -s ${CPUSYS_EVENT} ]] || return 1
	jq -e -s --arg mode "${cpusys_mode}" '
 length == 1 and (.[0] |
 .tracer_name == "cpusys" and .tracer_type == "autotracing"
 and (.container_id // "") == ""
 and .tracer_data[$mode + "_percent"] == 80
 and .tracer_data[$mode + "_percent_delta"] == 70
 and .tracer_data[$mode + "_percent_threshold"] == 75
 and .tracer_data[$mode + "_percent_delta_threshold"] == 45
 and .tracer_data.flamedata == [{"level":0,"value":1,"self":1,"label":"fixture_system_work"}])
 ' "${CPUSYS_EVENT}" > /dev/null
}
cpusys_sample_after() {
	local before=$1 count
	count=$(grep -Fc 'msg="sampled host cpu"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" || true)
	[[ ${count} -gt ${before} ]]
}
hold_additional_cpu_sample() {
	local before
	before=$(grep -Fc 'msg="sampled host cpu"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" || true)
	printf 'cpu %s\n' "$1" > "${CPUSYS_PROC_STAT}.next"
	mv "${CPUSYS_PROC_STAT}.next" "${CPUSYS_PROC_STAT}"
	wait_until 5 .05 cpusys_sample_after "${before}" || fatal "CPU fixture sample was not consumed"
}
for cpusys_mode in user total; do
	assert_huatuo_bamai_shutdown_with_signal TERM
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${HUATUO_BAMAI_TEST_TMPDIR}/cpu-before-${cpusys_mode}.log"
	cp "${CPUSYS_EVENT}" "${CPUSYS_EVENT}.before-${cpusys_mode}"
	rm -f "${CPUSYS_EVENT}" "${CPUSYS_PERF_CALLS_FILE}"
	printf 'cpu 100 0 100 800\n' > "${CPUSYS_PROC_STAT}"
	integration_huatuo_bamai_start write_config_additional_cpu \
		--region dev --procfs-prefix "${CPUSYS_FIXTURE_ROOT}" \
		--tools-bin-dir "${CPUSYS_FIXTURE_ROOT}/tools" --disable-kubelet --log-debug
	if [[ ${cpusys_mode} == user ]]; then
		hold_additional_cpu_sample "110 0 100 890"
		perf_has_run && fatal "user trigger fired below threshold"
		hold_additional_cpu_sample "190 0 100 910"
	else
		hold_additional_cpu_sample "105 0 105 890"
		perf_has_run && fatal "total trigger fired below threshold"
		hold_additional_cpu_sample "145 0 145 910"
	fi
	wait_until 10 .1 assert_additional_cpu_event || fatal "${cpusys_mode} CPU event invalid or missing"
	if [[ ${cpusys_mode} == user ]]; then
		hold_additional_cpu_sample "200 0 100 1000"
		hold_additional_cpu_sample "280 0 100 1020"
	else
		hold_additional_cpu_sample "150 0 150 1000"
		hold_additional_cpu_sample "190 0 190 1020"
	fi
	[[ $(wc -l < "${CPUSYS_PERF_CALLS_FILE}") -eq 1 ]] || fatal "${cpusys_mode} trigger bypassed cooldown"
	assert_additional_cpu_event || fatal "${cpusys_mode} cooldown emitted another event"
done
assert_huatuo_bamai_shutdown_with_signal TERM
