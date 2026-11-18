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

# Exercise the built profiler from deterministic async-profiler output to SVG and TUI.
# JVM sampling and symbolization are outside this output-contract test.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

readonly FLAMEGRAPH_TOOL_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/tools"
readonly FLAMEGRAPH_CLASSES_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/classes"
readonly FLAMEGRAPH_TARGET_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/target.log"

require_commands java javac python3 timeout script stty mkfifo

flamegraph_target_pid=""
flamegraph_tui_pid=""
flamegraph_tui_start_time=""
flamegraph_tui_driver_pid=""
flamegraph_tui_input_fd=""
cleanup() {
	if [[ -n ${flamegraph_tui_pid} ]] && ! process_exited "${flamegraph_tui_pid}" "${flamegraph_tui_start_time}"; then
		stop_by_pid "${flamegraph_tui_pid}" 2 || true
	fi
	flamegraph_tui_pid=""
	flamegraph_tui_start_time=""
	stop_and_wait_by_pid "${flamegraph_tui_driver_pid}" 2 || true
	flamegraph_tui_driver_pid=""
	[[ -z ${flamegraph_tui_input_fd} ]] || exec {flamegraph_tui_input_fd}>&-
	flamegraph_tui_input_fd=""
	[[ -n ${flamegraph_target_pid} ]] && stop_by_pid "${flamegraph_target_pid}" 5 || true
	flamegraph_target_pid=""
}
trap cleanup EXIT

assert_flamegraph_frame() {
	local rows=$1 name=$2 samples=$3 percent=$4 x=$5 width=$6
	local prefix="${name} (${samples} samples, " actual expected
	actual=$(awk -F '\t' -v prefix="${prefix}" 'index($1, prefix) == 1' "${rows}")
	printf -v expected '%s%s%%)\t%s\t%s' "${prefix}" "${percent}" "${x}" "${width}"
	assert_eq "${actual}" "${expected}" "${rows}: SVG frame" \
		|| fatal "SVG frame is missing, duplicated, or has incorrect geometry: $(cat "${rows}")"
}

assert_flamegraph_geometry() {
	local output=$1 rows=$2
	# XML parsing only extracts observable attributes; Shell owns the assertions.
	if ! python3 - "${output}" > "${rows}" << 'PY'; then
import sys
import xml.etree.ElementTree as ET

root = ET.parse(sys.argv[1]).getroot()
ns = {"svg": "http://www.w3.org/2000/svg"}
for group in root.findall("svg:g", ns):
    title = group.find("svg:title", ns)
    rect = group.find("svg:rect", ns)
    if title is not None and rect is not None:
        print(title.text, rect.get("x"), rect.get("width"), sep="\t")
PY
		fatal "cannot parse profiler SVG: ${output}"
	fi

	# At 100 Hz, the provider converts each input sample to 10,000,000 ns.
	assert_flamegraph_frame "${rows}" shared 70000000 70.00 10.0 826.0
	assert_flamegraph_frame "${rows}" 'shared_[k]' 50000000 50.00 10.0 590.0
	assert_flamegraph_frame "${rows}" 'shared_[k]' 30000000 30.00 836.0 354.0
	log_info "${output}: parent=70%/826px, child=50%/590px, self=20%/236px"
}

# script supplies the PTY; all scenario control and cleanup remain in Shell.
start_flamegraph_tui() {
	local scenario=$1 duration=$2 command
	flamegraph_tui_prefix="${HUATUO_BAMAI_TEST_TMPDIR}/${scenario}"
	cat > "${flamegraph_tui_prefix}.sh" << 'SHELL'
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
stty cols 120 rows 32
stty -g > "${FLAMEGRAPH_TUI_PREFIX}.before"
flamegraph_child_pid=""
trap 'stop_and_wait_by_pid "${flamegraph_child_pid}" 2 || true' EXIT
(
	child_pid=${BASHPID}
	child_start_time=$(process_start_time "${child_pid}")
	printf '%s %s\n' "${child_pid}" "${child_start_time}" > "${FLAMEGRAPH_TUI_PREFIX}.pid"
	exec "${ROOT_DIR}/_output/bin/profiler" \
		--type cpu --language java --pid "${FLAMEGRAPH_TARGET_PID}" \
		--tool-path "${FLAMEGRAPH_TOOL_DIR}" --duration "${FLAMEGRAPH_TUI_DURATION}" \
		--freq 100 --aggr-interval 1 --log-level info --output-format tui
) < /dev/tty &
flamegraph_child_pid=$!
status=0
wait "${flamegraph_child_pid}" || status=$?
flamegraph_child_pid=""
stty -g > "${FLAMEGRAPH_TUI_PREFIX}.after"
exit "${status}"
SHELL
	printf -v command 'bash %q' "${flamegraph_tui_prefix}.sh"
	mkfifo "${flamegraph_tui_prefix}.in"
	exec {flamegraph_tui_input_fd}<> "${flamegraph_tui_prefix}.in"
	(
		exec {flamegraph_tui_input_fd}>&-
		exec env TERM=xterm-256color NO_COLOR=1 SHELL=/bin/bash \
			FLAMEGRAPH_TUI_PREFIX="${flamegraph_tui_prefix}" FLAMEGRAPH_TUI_DURATION="${duration}" \
			FLAMEGRAPH_TARGET_PID="${flamegraph_target_pid}" FLAMEGRAPH_TOOL_DIR="${FLAMEGRAPH_TOOL_DIR}" \
			script -q -e -f -c "${command}" "${flamegraph_tui_prefix}.raw"
	) < "${flamegraph_tui_prefix}.in" > "${flamegraph_tui_prefix}.console" 2>&1 &
	flamegraph_tui_driver_pid=$!
	wait_until 10 0.1 test -s "${flamegraph_tui_prefix}.pid" \
		|| fatal "profiler PTY did not start: $(cat "${flamegraph_tui_prefix}.console")"
	read -r flamegraph_tui_pid flamegraph_tui_start_time < "${flamegraph_tui_prefix}.pid"
}

assert_flamegraph_tui_viewed_for() {
	local started=$1 duration=$2
	if process_exited "${flamegraph_tui_pid}" "${flamegraph_tui_start_time}"; then
		fatal "profiler TUI exited before ${duration}s: $(cat "${flamegraph_tui_prefix}.raw")"
	fi
	((SECONDS - started >= duration))
}

assert_flamegraph_tui_exit() {
	local status=0
	wait_until 10 0.1 process_exited "${flamegraph_tui_driver_pid}" \
		|| fatal "profiler TUI did not exit: $(cat "${flamegraph_tui_prefix}.raw")"
	wait "${flamegraph_tui_driver_pid}" || status=$?
	flamegraph_tui_driver_pid=""
	exec {flamegraph_tui_input_fd}>&-
	flamegraph_tui_input_fd=""
	assert_eq "${status}" 0 "profiler TUI exit code" \
		|| fatal "profiler TUI failed: $(cat "${flamegraph_tui_prefix}.raw" "${flamegraph_tui_prefix}.console")"
	assert_eq "$(process_exited "${flamegraph_tui_pid}" "${flamegraph_tui_start_time}" && echo 1 || echo 0)" 1 "profiler TUI process released" \
		|| fatal "profiler survived PTY driver exit: ${flamegraph_tui_pid}"
	flamegraph_tui_pid=""
	assert_eq "$(cat "${flamegraph_tui_prefix}.after")" "$(cat "${flamegraph_tui_prefix}.before")" "profiler TUI terminal settings restored" \
		|| fatal "profiler TUI left the terminal in raw mode"
	assert_eq "$(grep -Fq $'\033[?1049l' "${flamegraph_tui_prefix}.raw" && echo 1 || echo 0)" 1 "profiler TUI alternate screen restored" \
		|| fatal "profiler TUI did not leave the alternate screen"
	sed -E $'s/\033\\[[0-?]*[ -/]*[@-~]//g' "${flamegraph_tui_prefix}.raw" > "${flamegraph_tui_prefix}.log"
	assert_log_has_no_failure "${flamegraph_tui_prefix}.log" "profiler TUI"
}

mkdir -p "${FLAMEGRAPH_TOOL_DIR}/bin" "${FLAMEGRAPH_TOOL_DIR}/lib" "${FLAMEGRAPH_CLASSES_DIR}"
install -m 755 "${ROOT_DIR}/integration/testdata/test_profiler_pprof_asprof.sh" "${FLAMEGRAPH_TOOL_DIR}/bin/asprof"
# The sampler fixture does not load an agent, but the provider requires its file.
printf 'fixture agent\n' > "${FLAMEGRAPH_TOOL_DIR}/lib/libasyncProfiler.so"
javac -d "${FLAMEGRAPH_CLASSES_DIR}" "${ROOT_DIR}/integration/testdata/TestProfilerJavaMultiPID.java"

java -cp "${FLAMEGRAPH_CLASSES_DIR}" TestProfilerJavaMultiPID alpha \
	> "${FLAMEGRAPH_TARGET_LOG}" 2>&1 &
flamegraph_target_pid=$!
wait_until 30 1 grep -qx ready "${FLAMEGRAPH_TARGET_LOG}" \
	|| fatal "JVM fixture did not become ready: $(cat "${FLAMEGRAPH_TARGET_LOG}")"

for flamegraph_format in svg flamegraph; do
	flamegraph_output="${HUATUO_BAMAI_TEST_TMPDIR}/${flamegraph_format}.svg"
	flamegraph_log="${HUATUO_BAMAI_TEST_TMPDIR}/${flamegraph_format}.log"
	if ! timeout 30 "${ROOT_DIR}/_output/bin/profiler" \
		--type cpu --language java --pid "${flamegraph_target_pid}" \
		--tool-path "${FLAMEGRAPH_TOOL_DIR}" --duration 1 --freq 100 --aggr-interval 1 \
		--output-format "${flamegraph_format}" --output-path "${flamegraph_output}" \
		> "${flamegraph_log}" 2>&1; then
		fatal "profiler ${flamegraph_format} failed: $(cat "${flamegraph_log}")"
	fi
	assert_flamegraph_geometry "${flamegraph_output}" "${HUATUO_BAMAI_TEST_TMPDIR}/${flamegraph_format}.tsv"
	assert_log_has_no_failure "${flamegraph_log}" "profiler ${flamegraph_format}"
done

# User-paced viewing must survive the 30-second collection finalization budget.
start_flamegraph_tui tui 1
wait_until 10 0.1 grep -q 'HUATUO tui flamegraph' "${flamegraph_tui_prefix}.raw" \
	|| fatal "profiler did not open the terminal viewer: $(cat "${flamegraph_tui_prefix}.raw")"
printf v >&"${flamegraph_tui_input_fd}"
wait_until 5 0.1 grep -q 'view: all frames' "${flamegraph_tui_prefix}.raw" \
	|| fatal "profiler TUI did not expose the all-frames view"
# SECONDS has one-second resolution; 32 ticks ensure over 30 actual seconds.
wait_until 36 1 assert_flamegraph_tui_viewed_for "${SECONDS}" 32 \
	|| fatal "profiler TUI viewing window did not complete"
printf q >&"${flamegraph_tui_input_fd}"
assert_flamegraph_tui_exit
assert_eq "$(grep -q shared "${flamegraph_tui_prefix}.log" && echo 1 || echo 0)" 1 "profiler TUI sampled frames" \
	|| fatal "profiler TUI omitted sampled frames: $(cat "${flamegraph_tui_prefix}.log")"

# A sampling stop must preserve final samples and later permit external TUI exit.
start_flamegraph_tui tui-signals 60
wait_until 10 0.1 grep -q 'data reading loop started' "${flamegraph_tui_prefix}.raw" \
	|| fatal "profiler collection did not become ready: $(cat "${flamegraph_tui_prefix}.raw")"
kill -INT "${flamegraph_tui_pid}"
wait_until 10 0.1 grep -q 'HUATUO tui flamegraph' "${flamegraph_tui_prefix}.raw" \
	|| fatal "SIGINT did not publish the final TUI: $(cat "${flamegraph_tui_prefix}.raw")"
kill -TERM "${flamegraph_tui_pid}"
assert_flamegraph_tui_exit
assert_eq "$(grep -q shared "${flamegraph_tui_prefix}.log" && echo 1 || echo 0)" 1 "SIGINT preserved final samples" \
	|| fatal "SIGINT lost the final samples: $(cat "${flamegraph_tui_prefix}.log")"

# Appendix: regression fixed by https://github.com/ccfos/huatuo/pull/1457
# The reused sampler emits:
#   shared 2
#   shared_[k] 3
#   shared;shared_[k] 5
# Of 10 samples, shared includes 7: 2 self samples and 5 in its child.
# With DefaultStyle, usable width is 1200 - 2*10 = 1180 px.
#
# Frame                   | Before PR 1457    | After PR 1457
# ----------------------- | ----------------- | -----------------
# shared                  | 70.00% / 826.0 px | 70.00% / 826.0 px
# shared -> shared_[k]    | 70.00% / 826.0 px | 50.00% / 590.0 px
# independent shared_[k]  | 30.00% / 354.0 px | 30.00% / 354.0 px
#
# The old per-level normalization hides shared's 236 px of self samples.
# The global denominator preserves that gap and agrees with the sample weights.
# The PR's main;foo;bar 60 / main;foo 5 / main;baz 35 example has the same
# failure: bar changes from 65.00% / 767.0 px to 60.00% / 708.0 px.
