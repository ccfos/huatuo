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

# Verify iotracing autotracing with deterministic diskstats and the real
# iotracing/BPF subprocess. The test covers threshold detection, CLI argument
# compatibility, task correlation, and local persistence.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

command -v jq > /dev/null || skip "jq command is not installed"
[[ -x "${HUATUO_BAMAI_BIN}" ]] \
	|| fatal "huatuo-bamai binary missing: ${HUATUO_BAMAI_BIN}"
[[ -x "${ROOT_DIR}/_output/bin/iotracing" ]] \
	|| fatal "iotracing binary missing: ${ROOT_DIR}/_output/bin/iotracing"
[[ -r "${ROOT_DIR}/_output/bpf/iotracing.o" ]] \
	|| fatal "iotracing BPF object missing: ${ROOT_DIR}/_output/bpf/iotracing.o"

readonly IOTRACING_FIXTURE_ROOT="${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-fixture"
readonly IOTRACING_DISKSTATS="${IOTRACING_FIXTURE_ROOT}/proc/diskstats"
readonly IOTRACING_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/iotracing"

mkdir -p \
	"${IOTRACING_FIXTURE_ROOT}/bin" \
	"${IOTRACING_FIXTURE_ROOT}/bpf" \
	"${IOTRACING_FIXTURE_ROOT}/proc" \
	"${IOTRACING_FIXTURE_ROOT}/sys" \
	"${IOTRACING_FIXTURE_ROOT}/dev"

cp "${HUATUO_BAMAI_BIN}" "${IOTRACING_FIXTURE_ROOT}/bin/huatuo-bamai"
cp "${ROOT_DIR}/_output/bin/iotracing" "${IOTRACING_FIXTURE_ROOT}/bin/iotracing"
cp "${ROOT_DIR}/_output/bpf/iotracing.o" "${IOTRACING_FIXTURE_ROOT}/bpf/iotracing.o"

# IOsTotalTicks is the tenth counter after the device name. Increasing it by
# 500 over each five-second interval yields 10% utilization.
write_diskstats() {
	local io_ticks=$1
	printf '8 0 vda 100 0 100 100 100 0 100 100 0 %s %s 0 0 0 0 0 0 0\n' \
		"${io_ticks}" "${io_ticks}" > "${IOTRACING_DISKSTATS}"
}

iotracing_event_is_valid() {
	[[ -s "${IOTRACING_EVENT}" ]] || return 1
	jq -e '
		.tracer_name == "iotracing"
		and .tracer_type == "autotracing"
		and .tracer_data.reason_snapshot.type == "ioutil"
		and .tracer_data.reason_snapshot.major_num == 8
		and .tracer_data.reason_snapshot.minor_num == 0
		and .tracer_data.reason_snapshot.iostatus.io_util == 10
		and (.tracer_data.process_file_io_stats | type == "array")
		and ((.tracer_data.process_file_io_stats | length) <= 1)
		and all(
			.tracer_data.process_file_io_stats[];
			((.total_files | length) <= 1)
		)
		and (.tracer_data.io_schedule_timeout_stacks | type == "array")
	' "${IOTRACING_EVENT}" > /dev/null
}

write_diskstats 100

# Running a copied daemon makes its executable and BPF directories test-owned.
HUATUO_BAMAI_BIN="${IOTRACING_FIXTURE_ROOT}/bin/huatuo-bamai"
integration_huatuo_bamai_start \
	write_iotracing_autotracing_config \
	--region dev \
	--procfs-prefix "${IOTRACING_FIXTURE_ROOT}" \
	--disable-kubelet \
	--disable-cgroup \
	--log-debug

# Hold each counter value across a five-second sampling tick. Two consecutive
# over-threshold deltas are required before iotracing starts.
sleep 6
write_diskstats 600
sleep 6
write_diskstats 1100

wait_until 10 1 iotracing_event_is_valid \
	|| fatal "iotracing did not persist the expected autotracing event"
