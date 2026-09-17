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

# Verify irqtracing's completeness contract on a real kernel: under a softirq
# flood on the target cpu the result must not pretend to be complete —
# nmissed > 0 and flamedata present. The drop counter and the ksoftirqd
# window snapshot are read after detach; the snapshot read contract itself
# is covered by unit tests (TestReadDroppedSamples, TestReadKsoftirqdWindow).

set -exuo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/irqtracing"
readonly TOOL_BPF="${ROOT_DIR}/_output/bpf/irq_tracing.o"
readonly OUT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/irqtracing"
readonly TARGET_IP="127.0.0.99"
readonly TARGET_PORT=9999
readonly DURATION=3
readonly FLOOD_DURATION=$((DURATION + 2)) # load must outlast the collection window

[[ $EUID -eq 0 ]] || fatal "requires root (BPF requires CAP_BPF/CAP_SYS_ADMIN)"
[[ -x ${TOOL_BIN} ]] || fatal "missing irqtracing binary: ${TOOL_BIN}"
[[ -f ${TOOL_BPF} ]] || fatal "missing irqtracing bpf object: ${TOOL_BPF}"
command -v jq > /dev/null 2>&1 || fatal "jq required"
command -v taskset > /dev/null 2>&1 || fatal "taskset required"

mkdir -p "${OUT_DIR}"

# Use a cpu the test itself is allowed to run on: nproc only reports the
# allowed count and does not guarantee cpu 0 is in the current cpuset.
expand_cpus() {
	awk -v list="$1" 'BEGIN {
		n = split(list, parts, ",");
		for (i = 1; i <= n; i++) {
			if (parts[i] ~ /-/) {
				split(parts[i], r, "-");
				for (c = r[1]; c <= r[2]; c++) printf "%d\n", c;
			} else {
				printf "%d\n", parts[i];
			}
		}
	}'
}
mapfile -t ALLOWED_CPUS < <(expand_cpus "$(awk '/^Cpus_allowed_list:/ {print $2}' /proc/self/status)")
[[ ${#ALLOWED_CPUS[@]} -ge 1 ]] || fatal "no allowed cpu in /proc/self/status"
# Prefer a non-zero cpu: cpu 0 often runs container/host housekeeping and its
# runqueue behavior is less representative.
if [[ ${#ALLOWED_CPUS[@]} -ge 2 ]]; then
	readonly TARGET_CPU=${ALLOWED_CPUS[1]}
else
	readonly TARGET_CPU=${ALLOWED_CPUS[0]}
fi

flood_pid=""
tool_pid=""

# Kill the flood or the tool if the script exits before they finish; idempotent.
cleanup_load() {
	if [[ -n "${flood_pid}" ]]; then
		kill -9 "${flood_pid}" 2> /dev/null || true
	fi
	if [[ -n "${tool_pid}" ]]; then
		kill -9 "${tool_pid}" 2> /dev/null || true
	fi
}
trap cleanup_load EXIT

# flood_udp <cpu>: spam UDP packets at a closed loopback port pinned to <cpu>;
# every packet raises a NET_RX softirq on that cpu.
flood_udp() {
	local cpu=$1
	taskset -c "${cpu}" timeout "${FLOOD_DURATION}" bash -c "
		while :; do printf x >/dev/udp/${TARGET_IP}/${TARGET_PORT}; done
	" > /dev/null 2>&1
}

log_info "irqtracing: flood cpu ${TARGET_CPU}, expect nmissed > 0 and flamedata"

# Bounds for the process lifecycle below: the collector gets load/attach
# grace plus DURATION plus snapshot slack before it is killed; the flood
# must be running shortly after launch. Every wait is polled so a stalled
# BPF load/attach/detach or map read fails the run instead of hanging the
# whole test lane.
readonly ATTACH_GRACE=2 # x0.5s: collector load+attach grace
readonly TOOL_DEADLINE=$((DURATION + 30))
readonly FLOOD_READY_DEADLINE=20 # x0.5s

"${TOOL_BIN}" \
	--bpf-path "${TOOL_BPF}" \
	--target-cpu "${TARGET_CPU}" \
	--duration "${DURATION}" \
	--output-path "${OUT_DIR}" > "${OUT_DIR}/path.txt" 2> "${OUT_DIR}/err.txt" &
tool_pid=$!

# The collector has no readiness signal, so the grace poll only detects an
# early exit (attach failure) instead of sleeping blind, and can never
# stall the lane.
for _ in $(seq 1 $((ATTACH_GRACE * 2))); do
	if ! kill -0 "${tool_pid}" 2> /dev/null; then
		wait "${tool_pid}" || true
		fatal "irqtracing exited before collection started (stderr: $(tail -1 "${OUT_DIR}/err.txt"))"
	fi
	sleep 0.5
done

flood_udp "${TARGET_CPU}" &
flood_pid=$!

# Bounded readiness poll: the flood must come up shortly after launch and
# stay running, otherwise the softirq load is missing and the run is void.
flood_ready=0
until kill -0 "${flood_pid}" 2> /dev/null; do
	flood_ready=$((flood_ready + 1))
	if ((flood_ready >= FLOOD_READY_DEADLINE)); then
		fatal "flood failed to start on cpu ${TARGET_CPU}"
	fi
	sleep 0.5
done
sleep 0.5
kill -0 "${flood_pid}" || fatal "flood failed to stay running on cpu ${TARGET_CPU}"

# Bounded wait for the collector: kill and diagnose on deadline instead of
# waiting forever.
tool_waited=0
while kill -0 "${tool_pid}" 2> /dev/null; do
	tool_waited=$((tool_waited + 1))
	if ((tool_waited >= TOOL_DEADLINE)); then
		kill -9 "${tool_pid}" 2> /dev/null || true
		fatal "irqtracing did not exit within ${TOOL_DEADLINE}s (stderr: $(tail -1 "${OUT_DIR}/err.txt"))"
	fi
	sleep 1
done
if ! wait "${tool_pid}"; then
	fatal "irqtracing failed on cpu ${TARGET_CPU} (stderr: $(tail -1 "${OUT_DIR}/err.txt"))"
fi
wait "${flood_pid}" || true
flood_pid=""
tool_pid=""

result_file=$(cat "${OUT_DIR}/path.txt")
nmissed=$(jq -r '.nmissed' "${result_file}")
flamedata_null=$(jq -r '.flamedata == null' "${result_file}")
rq_tasks=$(jq -r '.rq_tasks | length' "${result_file}")

log_info "irqtracing: result=${result_file} nmissed=${nmissed} flamedata_null=${flamedata_null} rq_tasks=${rq_tasks}"

((nmissed > 0)) || fatal "expected nmissed > 0 under flood, got ${nmissed}"
[[ "${flamedata_null}" == "false" ]] || fatal "expected flamedata under flood"

# The rq_tasks snapshot is read after detach (see cmd/irqtracing/main.go); its
# content depends on a task being queued on the target cpu at the dump
# instant and on ksoftirqd servicing softirqs, both load/timing dependent, so
# it is logged here but not asserted. The snapshot read contract itself is
# covered by TestReadKsoftirqdWindow in cmd/irqtracing.

log_info "PASS"
