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

# This Ubuntu release rejects mock module kprobes with EADDRNOTAVAIL.
skip_known_mock_kprobe_kernel() {
	if [[ $(uname -r) == 5.4.0-216-generic ]]; then
		skip "mock module kprobe attachment is unavailable on Ubuntu kernel 5.4.0-216-generic (EADDRNOTAVAIL); see ebpf-integration-mock_en.md"
	fi
}

# Wait for collector work and storage flushing before checking final events.
stop_bpf_mock_daemon() {
	local pid
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	stop_and_wait_by_pid "${pid}" 60 || fatal "huatuo-bamai did not shut down cleanly"
	rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
}

run_bpf_mock_checker() {
	local feature=$1
	shift
	local runner=${BPF_MOCK_CHECKER_BIN:-${HUATUO_BAMAI_TEST_TMPDIR}/${feature}-mock-checker}
	local build_log="${HUATUO_BAMAI_TEST_TMPDIR}/mock-event-checker-build.log"
	local checker_log="${HUATUO_BAMAI_TEST_TMPDIR}/mock-event-checker.log"
	if [[ -z "${BPF_MOCK_CHECKER_BIN:-}" ]]; then
		require_commands go
		(cd "${ROOT_DIR}" && go build -tags integration -o "${runner}" ./integration/bpf_mock/${feature}/checker) \
			> "${build_log}" 2>&1 || fatal "mock event checker build failed; see ${build_log}"
	fi
	[[ -x "${runner}" ]] || fatal "mock event checker is not executable: ${runner}"
	"${runner}" "$@" \
		> "${checker_log}" 2>&1 || fatal "${feature} event verification failed; see ${checker_log}"
	cat "${checker_log}"
}
