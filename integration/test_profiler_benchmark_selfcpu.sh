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

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly PROFILER_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly NATIVE_BPF_OBJECT="${ROOT_DIR}/_output/bpf/native_cpu_profiler.o"
readonly CPU_LIMIT_PERCENT=10
readonly SAMPLE_SECONDS=5
readonly PROFILER_DURATION=15

is_container && skip "native CPU profiler requires bare-metal cgroup/PMU access"

[[ -x "${PROFILER_BIN}" ]] || fatal "profiler binary not found: ${PROFILER_BIN}"
[[ -r "${NATIVE_BPF_OBJECT}" ]] || fatal \
	"native CPU profiler BPF object not found: ${NATIVE_BPF_OBJECT}"
perf_event_paranoid=$(cat /proc/sys/kernel/perf_event_paranoid) || fatal \
	"perf_event_paranoid not readable: perf unavailable"
if ((perf_event_paranoid > 2)); then
	skip "kernel.perf_event_paranoid=${perf_event_paranoid} blocks CPU profiling"
fi

clock_ticks=$(getconf CLK_TCK) || fatal "cannot determine kernel clock tick rate"
[[ "${clock_ticks}" =~ ^[1-9][0-9]*$ ]] || fatal \
	"invalid kernel clock tick rate: ${clock_ticks}"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-selfcpu.XXXXXX")
PROFILER_STDOUT="${WORK_DIR}/profiler.stdout"
PROFILER_STDERR="${WORK_DIR}/profiler.stderr"
PROFILER_TARGET_PID=""
PROFILER_SELFPID=""

cleanup() {
	stop_by_pid "${PROFILER_SELFPID}"
	stop_by_pid "${PROFILER_TARGET_PID}"
}
trap cleanup EXIT

# An idle target isolates the profiler's own scheduling and aggregation cost.
sleep $((PROFILER_DURATION + SAMPLE_SECONDS)) &
PROFILER_TARGET_PID=$!

"${PROFILER_BIN}" \
	--type cpu \
	--language c \
	--pid "${PROFILER_TARGET_PID}" \
	--duration "${PROFILER_DURATION}" \
	--freq 99 \
	--output-format collapsed \
	--output-path "${WORK_DIR}" \
	--aggr-interval 5 \
	--verbose > "${PROFILER_STDOUT}" 2> "${PROFILER_STDERR}" &
PROFILER_SELFPID=$!

sleep 2
wait_until 10 1 "profiler ready" profiler_ready "${PROFILER_STDOUT}" || fatal \
	"profiler did not enter its data reading loop"
kill -0 "${PROFILER_SELFPID}" 2> /dev/null || fatal \
	"profiler exited before CPU sampling"

start_ticks=$(awk '{print $14 + $15}' "/proc/${PROFILER_SELFPID}/stat") || fatal \
	"cannot read profiler CPU counters"
start_time_ns=$(date +%s%N)

sleep "${SAMPLE_SECONDS}"

kill -0 "${PROFILER_SELFPID}" 2> /dev/null || fatal \
	"profiler exited during CPU sampling"
end_ticks=$(awk '{print $14 + $15}' "/proc/${PROFILER_SELFPID}/stat") || fatal \
	"cannot read profiler CPU counters after sampling"
end_time_ns=$(date +%s%N)

cpu_percent=$(awk \
	-v ticks="$((end_ticks - start_ticks))" \
	-v hz="${clock_ticks}" \
	-v elapsed_ns="$((end_time_ns - start_time_ns))" \
	'BEGIN { printf "%.2f", ticks * 100000000000 / (hz * elapsed_ns) }')

if awk -v actual="${cpu_percent}" -v limit="${CPU_LIMIT_PERCENT}" \
	'BEGIN { exit !(actual > limit) }'; then
	fatal "profiler CPU usage ${cpu_percent}% exceeds ${CPU_LIMIT_PERCENT}%"
fi

echo "profiler CPU usage ${cpu_percent}% is within ${CPU_LIMIT_PERCENT}%"
