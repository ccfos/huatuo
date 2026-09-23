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
# nmissed > 0 and flamedata present. The drop counter is read after detach;
# that contract is covered by TestDetachAndReadDroppedSamplesDetachBeforeReads.

set -exuo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/irqtracing"
readonly TOOL_BPF="${ROOT_DIR}/_output/bpf/irqtracing.o"
readonly OUT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/irqtracing"
readonly TARGET_IP="127.0.0.99"
readonly TARGET_PORT=9999
readonly DURATION=3
readonly MAX_EVENTS_PER_SECOND_PER_CPU=2
readonly FLOOD_READY="${OUT_DIR}/flood.ready"

[[ $EUID -eq 0 ]] || fatal "requires root (BPF requires CAP_BPF/CAP_SYS_ADMIN)"
[[ -x ${TOOL_BIN} ]] || fatal "missing irqtracing binary: ${TOOL_BIN}"
[[ -f ${TOOL_BPF} ]] || fatal "missing irqtracing bpf object: ${TOOL_BPF}"
command -v jq > /dev/null 2>&1 || fatal "jq required"
command -v taskset > /dev/null 2>&1 || fatal "taskset required"
tracepoint_available irq softirq_raise || skip "irq/softirq_raise is unavailable"
tracepoint_available irq softirq_entry || skip "irq/softirq_entry is unavailable"

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
# Prefer a non-zero cpu because cpu 0 often runs container/host housekeeping.
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

# flood_udp <cpu>: spam UDP packets at a closed loopback port pinned to <cpu>.
# The ready file is written only after the first packet has been sent.
flood_udp() {
	local cpu=$1
	exec taskset -c "${cpu}" bash -c '
		target_ip=$1
		target_port=$2
		ready=$3
		printf x > "/dev/udp/${target_ip}/${target_port}"
		printf ready > "${ready}"
		while :; do
			printf x > "/dev/udp/${target_ip}/${target_port}"
		done
	' _ "${TARGET_IP}" "${TARGET_PORT}" "${FLOOD_READY}" > /dev/null 2>&1
}

flood_is_ready() {
	[[ -s "${FLOOD_READY}" ]] && kill -0 "${flood_pid}" 2> /dev/null
}

tool_has_exited() {
	! kill -0 "${tool_pid}" 2> /dev/null
}

readonly TOOL_DEADLINE=$((DURATION + 30))
log_info "irqtracing: flood cpu ${TARGET_CPU}, expect nmissed > 0 and flamedata"

rm -f "${FLOOD_READY}"
flood_udp "${TARGET_CPU}" &
flood_pid=$!
wait_until 5 0.1 flood_is_ready \
	|| fatal "flood failed to start on cpu ${TARGET_CPU}"

"${TOOL_BIN}" \
	--bpf-path "${TOOL_BPF}" \
	--target-cpu "${TARGET_CPU}" \
	--duration "${DURATION}" \
	--max-events-per-second-per-cpu "${MAX_EVENTS_PER_SECOND_PER_CPU}" \
	--output json > "${OUT_DIR}/result.json" 2> "${OUT_DIR}/err.txt" &
tool_pid=$!

wait_until "${TOOL_DEADLINE}" 1 tool_has_exited \
	|| fatal "irqtracing did not exit within ${TOOL_DEADLINE}s (stderr: $(tail -1 "${OUT_DIR}/err.txt"))"
if ! wait "${tool_pid}"; then
	fatal "irqtracing failed on cpu ${TARGET_CPU} (stderr: $(tail -1 "${OUT_DIR}/err.txt"))"
fi
kill -0 "${flood_pid}" 2> /dev/null \
	|| fatal "flood exited before irqtracing completed"
kill "${flood_pid}" 2> /dev/null || true
wait "${flood_pid}" || true
flood_pid=""
tool_pid=""

result_file="${OUT_DIR}/result.json"
nmissed=$(jq -r '.nmissed' "${result_file}")
flamedata_null=$(jq -r '.flamedata == null' "${result_file}")

log_info "irqtracing: result=${result_file} nmissed=${nmissed} flamedata_null=${flamedata_null}"

((nmissed > 0)) || fatal "expected nmissed > 0 under flood, got ${nmissed}"
[[ "${flamedata_null}" == "false" ]] || fatal "expected flamedata under flood"
if grep -q 'victim\[ksoftirqd' "${result_file}"; then
	fatal "ksoftirqd was recorded as a victim"
fi

log_info "PASS"
