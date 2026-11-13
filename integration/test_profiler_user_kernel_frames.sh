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

# Exercise the real profiler CLI from async-profiler output to JSON/pprof.
# A deterministic sampler forces identical names in user and kernel stacks;
# actual JVM sampling and symbolization are outside this test's boundary.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

readonly PPROF_TOOL_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/tools"
readonly PPROF_CLASSES_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/classes"
readonly PPROF_OUTPUT="${HUATUO_BAMAI_TEST_TMPDIR}/profile.json"
readonly PPROF_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/profiler.log"

require_commands "${PROFILER_TOOL_DIR}/bin/asprof" java javac jq timeout

pprof_target_pid=""
cleanup() {
	[[ -n ${pprof_target_pid} ]] && stop_by_pid "${pprof_target_pid}" 5 || true
}
trap cleanup EXIT

assert_user_kernel_frames() {
	# Distinct names also ensure the mixed stack uses distinct locations.
	jq -se '
def frame($p; $id):
  ($p.location[] | select(.id == $id) | .line[0].function_id) as $fid
  | ($p.function[] | select(.id == $fid) | .name) as $name
  | $p.string_table[$name];

[
  .[]
  | .profile as $p
  | $p.sample[]
  | {names: [.location_id[] | frame($p; .)], value: .value}
]
| sort_by(.names) == (
  [
    {names: ["shared"], value: [20000000]},
    {names: ["shared_[k]"], value: [30000000]},
    {names: ["shared_[k]", "shared"], value: [50000000]}
  ]
  | sort_by(.names)
)
	' "${PPROF_OUTPUT}" > /dev/null \
		|| fatal "expected distinct user/kernel frames and weights 20/30/50ms: $(cat "${PPROF_OUTPUT}")"
}

mkdir -p "${PPROF_TOOL_DIR}/bin" "${PPROF_TOOL_DIR}/lib" "${PPROF_CLASSES_DIR}"
install -m 755 "${ROOT_DIR}/integration/testdata/test_profiler_pprof_asprof.sh" "${PPROF_TOOL_DIR}/bin/asprof"
# The sampler fixture does not load an agent, but the provider requires its file.
printf 'fixture agent\n' > "${PPROF_TOOL_DIR}/lib/libasyncProfiler.so"
javac -d "${PPROF_CLASSES_DIR}" "${ROOT_DIR}/integration/testdata/TestProfilerJavaMultiPID.java"

java -cp "${PPROF_CLASSES_DIR}" TestProfilerJavaMultiPID alpha \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/target.log" 2>&1 &
pprof_target_pid=$!
wait_until 30 1 grep -qx ready "${HUATUO_BAMAI_TEST_TMPDIR}/target.log" || fatal "JVM fixture did not become ready: $(cat "${HUATUO_BAMAI_TEST_TMPDIR}/target.log")"

if ! timeout 30 "${ROOT_DIR}/_output/bin/profiler" \
	--type cpu --language java --pid "${pprof_target_pid}" \
	--tool-path "${PPROF_TOOL_DIR}" --duration 1 --freq 100 --aggr-interval 1 \
	--output-format json --output-path "${PPROF_OUTPUT}" > "${PPROF_LOG}" 2>&1; then
	fatal "profiler failed: $(cat "${PPROF_LOG}")"
fi
assert_user_kernel_frames
log_info "pprof preserves user/kernel frame identity and sample weights"
