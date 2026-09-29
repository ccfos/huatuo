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

# Verify the config-to-storage irqtracing path with deterministic proc/stat
# samples and the real CLI/BPF subprocess.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

command -v jq > /dev/null || skip "jq command is not installed"
command -v ss > /dev/null || skip "ss command is not installed"
tracepoint_available irq softirq_raise || skip "irq/softirq_raise is unavailable"
tracepoint_available irq softirq_entry || skip "irq/softirq_entry is unavailable"
[[ -x "${HUATUO_BAMAI_BIN}" ]] \
	|| fatal "huatuo-bamai binary missing: ${HUATUO_BAMAI_BIN}"
[[ -x "${ROOT_DIR}/_output/bin/irqtracing" ]] \
	|| fatal "irqtracing binary missing: ${ROOT_DIR}/_output/bin/irqtracing"
[[ -r "${ROOT_DIR}/_output/bpf/irqtracing.o" ]] \
	|| fatal "irqtracing BPF object missing: ${ROOT_DIR}/_output/bpf/irqtracing.o"

IRQTRACING_API_PORT=$(allocate_available_port) \
	|| fatal "failed to allocate a huatuo-bamai API port"
readonly IRQTRACING_API_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${IRQTRACING_API_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"
export HUATUO_BAMAI_ADDR HUATUO_BAMAI_METRICS_API

readonly IRQTRACING_FIXTURE_ROOT="${HUATUO_BAMAI_TEST_TMPDIR}/irqtracing-fixture"
readonly IRQTRACING_STAT="${IRQTRACING_FIXTURE_ROOT}/proc/stat"
readonly IRQTRACING_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/irqtracing"

mkdir -p \
	"${IRQTRACING_FIXTURE_ROOT}/bin" \
	"${IRQTRACING_FIXTURE_ROOT}/bpf" \
	"${IRQTRACING_FIXTURE_ROOT}/proc" \
	"${IRQTRACING_FIXTURE_ROOT}/sys" \
	"${IRQTRACING_FIXTURE_ROOT}/dev"

cp "${HUATUO_BAMAI_BIN}" "${IRQTRACING_FIXTURE_ROOT}/bin/huatuo-bamai"
cp "${ROOT_DIR}/_output/bin/irqtracing" "${IRQTRACING_FIXTURE_ROOT}/bin/irqtracing"
cp "${ROOT_DIR}/_output/bpf/irqtracing.o" "${IRQTRACING_FIXTURE_ROOT}/bpf/irqtracing.o"

write_stat_sample() {
	printf 'cpu0 %d 0 0 900 0 %d 0 0\n' "$1" "$2" > "${IRQTRACING_STAT}"
	wait_until 5 0.05 stat_sample_consumed \
		|| fatal "huatuo-bamai did not finish reading the proc/stat sample"
}

# Waiting for the reader to close the FIFO preserves one sample per open. If
# the next writer opens earlier, Scanner can consume both samples before EOF.
stat_sample_consumed() {
	local pid fd
	[[ -r "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" ]] || return 1
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	for fd in "/proc/${pid}/fd/"*; do
		[[ -e "${fd}" ]] || continue
		[[ "${fd}" -ef "${IRQTRACING_STAT}" ]] && return 1
	done
	return 0
}

serve_stat_samples() {
	write_stat_sample 100 0
	write_stat_sample 150 0
	write_stat_sample 200 0

	local irq=0
	while :; do
		irq=$((irq + 50))
		write_stat_sample 200 "${irq}"
	done
}

irqtracing_event_is_valid() {
	[[ -s "${IRQTRACING_EVENT}" ]] || return 1
	jq -e '
		def epoch: sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601;
		.tracer_name == "irqtracing"
		and .tracer_type == "autotracing"
		and .tracer_data.rule == "rule_cpu_pct_spike"
		and .tracer_data.trigger_cpu == 0
		and .tracer_data.trace_duration == 1
		and ((.started_timestamp | type) == "string")
		and (((.uploaded_timestamp | epoch) -
		      (.started_timestamp | epoch)) >= 1)
		and (.tracer_data.hit_cpus | length) == 1
		and .tracer_data.hit_cpus[0].cpu == 0
		and (.tracer_data.nmissed | type) == "number"
	' "${IRQTRACING_EVENT}" > /dev/null
}

mkfifo "${IRQTRACING_STAT}"
stat_writer_pid=""
cleanup_stat_writer() {
	if [[ -n "${stat_writer_pid}" ]]; then
		kill "${stat_writer_pid}" 2> /dev/null || true
		wait "${stat_writer_pid}" 2> /dev/null || true
	fi
}
trap cleanup_stat_writer EXIT

serve_stat_samples &
stat_writer_pid=$!

HUATUO_BAMAI_BIN="${IRQTRACING_FIXTURE_ROOT}/bin/huatuo-bamai"
integration_huatuo_bamai_start \
	write_irqtracing_autotracing_config \
	--region dev \
	--procfs-prefix "${IRQTRACING_FIXTURE_ROOT}" \
	--disable-kubelet \
	--log-debug

wait_until 15 1 irqtracing_event_is_valid \
	|| fatal "irqtracing did not persist the expected autotracing event"
