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

# Verify the CPU tool selection, real profiler/toolstream publication, and event
# correlation without an apiserver. Stack sampling is covered by profiler tests.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/lib_continuous_profiling.sh"
source "${ROOT_DIR}/integration/config.sh"

require_commands jq ss docker
docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"
require_readable /sys/kernel/btf/vmlinux

readonly CPU_PROFILER_FIXTURE="${HUATUO_BAMAI_TEST_TMPDIR}/cpu-profiler-fixture"
readonly CPU_PROFILER_STAT="${CPU_PROFILER_FIXTURE}/proc/stat"
readonly CPU_PROFILER_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/cpusys"
cpu_profiler_load_pid=""
cpu_profiler_stat_writer_pid=""

cleanup_cpu_profiler() {
	local status=$?
	stop_and_wait_by_pid "${cpu_profiler_load_pid}" 5 || true
	if ((status != 0)); then storage_dump_logs || true; fi
	huatuo_bamai_stop "${HUATUO_BAMAI_TEST_TMPDIR}" || true
	stop_and_wait_by_pid "${cpu_profiler_stat_writer_pid}" 5 || true
	storage_stop || true
}
trap cleanup_cpu_profiler EXIT

cpu_profiler_stat_sample_consumed() {
	local pid fd
	[[ -r "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" ]] || return 1
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	[[ -d "/proc/${pid}/fd" ]] || return 1
	for fd in "/proc/${pid}/fd/"*; do
		[[ -e "${fd}" ]] || continue
		[[ "${fd}" -ef "${CPU_PROFILER_STAT}" ]] && return 1
	done
	return 0
}

write_cpu_profiler_stat_sample() {
	printf 'cpu %d 0 %d 800\n' "$1" "$2" > "${CPU_PROFILER_STAT}"
	# Do not let the next writer join a reader that still owns this sample.
	wait_until 5 0.05 cpu_profiler_stat_sample_consumed \
		|| fatal "huatuo-bamai did not finish reading the CPU statistics sample"
}

serve_cpu_profiler_stat_samples() {
	write_cpu_profiler_stat_sample 100 100
	write_cpu_profiler_stat_sample 190 110
	# Keep later reads unblocked without creating another usage delta.
	while :; do
		write_cpu_profiler_stat_sample 210 190
	done
}

cpusys_trigger_ready() {
	local event_file=$1
	[[ -s $event_file ]] || return 1
	jq -e '
        .tracer_name == "cpusys" and .tracer_type == "autotracing" and
        .tracer_data.system_percent == 80 and
        .tracer_data.system_percent_delta == 70 and
        .tracer_data.flamedata == null and
        (.tracer_id | length > 0)
    ' "$event_file" > /dev/null || return 1
}

mkdir -p "${CPU_PROFILER_FIXTURE}/proc" "${CPU_PROFILER_FIXTURE}/sys"
mkfifo "${CPU_PROFILER_STAT}"
storage_start

# The synthetic counters only trigger profiling; real sampling needs CPU activity.
bash -c 'while :; do :; done' &
cpu_profiler_load_pid=$!
kill -0 "${cpu_profiler_load_pid}" 2> /dev/null \
	|| fatal "CPU profiling workload exited before collection"

serve_cpu_profiler_stat_samples &
cpu_profiler_stat_writer_pid=$!
# Only the CPU sampler may consume the FIFO; unrelated tracers are not under test.
integration_huatuo_bamai_start \
	write_config_bamai_cpusys_profiler \
	--region dev \
	--procfs-prefix "${CPU_PROFILER_FIXTURE}" \
	--disable-tracing blk_iostat \
	--disable-tracing irqtracing \
	--disable-tracing tcp_retransmit \
	--disable-kubelet \
	--log-debug

cpu_profiler_deadline=$((SECONDS + 45))
wait_until 45 1 cpusys_trigger_ready "$CPU_PROFILER_EVENT" \
	|| fatal "CPU trigger event missing; inspect ${CPU_PROFILER_EVENT}"
cpu_profiler_task_id=$(jq -er .tracer_id "$CPU_PROFILER_EVENT")
wait_until "$((cpu_profiler_deadline - SECONDS))" 1 profiling_publication_ready \
	'cpu-profiler-integration*' "$cpu_profiler_task_id" \
	'process_cpu:cpu:nanoseconds:cpu:nanoseconds' "$HUATUO_BAMAI_TEST_TMPDIR" \
	|| fatal "CPU profile or publication marker missing; inspect profiling-publication.json"
cat "${CPU_PROFILER_EVENT}"
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
