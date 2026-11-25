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

# Exercise the built profiler from deterministic async-profiler output across
# the SVG, Speedscope, Chrome Trace, and terminal flame graph formatters. JVM sampling and symbolization
# are outside this output-contract test.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

readonly OUTPUT_FORMATS_TOOL_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/tools"
readonly OUTPUT_FORMATS_CLASSES_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/classes"
readonly OUTPUT_FORMATS_TARGET_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/target.log"
readonly OUTPUT_FORMATS_TUI_REPORT="${HUATUO_BAMAI_TEST_TMPDIR}/profile.gob"

require_commands java javac jq python3 timeout script stty mkfifo

target_pid=""
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
	[[ -n ${target_pid} ]] && stop_by_pid "${target_pid}" 5 || true
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

assert_speedscope_output() {
	local output=$1
	jq -e '
		.["$schema"] == "https://www.speedscope.app/file-format-schema.json"
		and .exporter == "huatuo-profiler"
		and (.profiles | length) == 1
		and .profiles[0].type == "sampled"
		and .profiles[0].unit == "nanoseconds"
		and .profiles[0].endValue == 100000000
		and ((.profiles[0].weights | add) == 100000000)
		and ((.profiles[0].weights | sort) == [20000000, 30000000, 50000000])
		and ([.shared.frames[].name] | index("shared") != null)
		and ([.shared.frames[].name] | index("shared_[k]") != null)
	' "${output}" > /dev/null \
		|| fatal "unexpected Speedscope output: $(cat "${output}")"
}

assert_chrometrace_output() {
	local output=$1
	jq -e '
		[.traceEvents[] | select(.ph == "M" and .name == "thread_name")] | length == 1
	' "${output}" > /dev/null \
		|| fatal "Chrome Trace metadata is missing: $(cat "${output}")"
	jq -e '
		[.traceEvents[] | select(.ph == "X" and (.name == "shared" or .name == "shared_[k]"))] as $events
		| ($events | length) == 4
			and ([$events[].dur] | sort) == [20000, 30000, 50000, 50000]
			and ([$events[] | .ts + .dur] | max) == 100000
	' "${output}" > /dev/null \
		|| fatal "unexpected Chrome Trace output: $(cat "${output}")"
}

# script supplies the PTY; all scenario control and cleanup remain in Shell.
start_flamegraph_tui() {
	local scenario=$1 command
	shift
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
	exec "$@"
) < /dev/tty &
flamegraph_child_pid=$!
status=0
wait "${flamegraph_child_pid}" || status=$?
flamegraph_child_pid=""
stty -g > "${FLAMEGRAPH_TUI_PREFIX}.after"
exit "${status}"
SHELL
	printf -v command '%q ' bash "${flamegraph_tui_prefix}.sh" "${ROOT_DIR}/_output/bin/profiler" "$@"
	mkfifo "${flamegraph_tui_prefix}.in"
	exec {flamegraph_tui_input_fd}<> "${flamegraph_tui_prefix}.in"
	(
		exec {flamegraph_tui_input_fd}>&-
		exec env TERM=xterm-256color NO_COLOR=1 SHELL=/bin/bash \
			FLAMEGRAPH_TUI_PREFIX="${flamegraph_tui_prefix}" \
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
	local expected_status=${1:-0} status=0
	wait_until 10 0.1 process_exited "${flamegraph_tui_driver_pid}" \
		|| fatal "profiler TUI did not exit: $(cat "${flamegraph_tui_prefix}.raw")"
	wait "${flamegraph_tui_driver_pid}" || status=$?
	flamegraph_tui_driver_pid=""
	exec {flamegraph_tui_input_fd}>&-
	flamegraph_tui_input_fd=""
	assert_eq "${status}" "${expected_status}" "profiler TUI exit code" \
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

assert_flamegraph_tui_samples() {
	grep -Fq 'shared' "${flamegraph_tui_prefix}.log" \
		&& grep -Fq 'value: 100000000 ' "${flamegraph_tui_prefix}.log" \
		|| fatal "profiler TUI lost fixture frames or sample weight: $(cat "${flamegraph_tui_prefix}.log")"
}

mkdir -p "${OUTPUT_FORMATS_TOOL_DIR}/bin" "${OUTPUT_FORMATS_TOOL_DIR}/lib" "${OUTPUT_FORMATS_CLASSES_DIR}"
install -m 755 "${ROOT_DIR}/integration/testdata/test_profiler_pprof_asprof.sh" "${OUTPUT_FORMATS_TOOL_DIR}/bin/asprof"
# The sampler fixture does not load an agent, but the provider requires its file.
printf 'fixture agent\n' > "${OUTPUT_FORMATS_TOOL_DIR}/lib/libasyncProfiler.so"
javac -d "${OUTPUT_FORMATS_CLASSES_DIR}" "${ROOT_DIR}/integration/testdata/TestProfilerJavaMultiPID.java"

java -cp "${OUTPUT_FORMATS_CLASSES_DIR}" TestProfilerJavaMultiPID alpha \
	> "${OUTPUT_FORMATS_TARGET_LOG}" 2>&1 &
target_pid=$!
wait_until 30 1 grep -qx ready "${OUTPUT_FORMATS_TARGET_LOG}" \
	|| fatal "JVM fixture did not become ready: $(cat "${OUTPUT_FORMATS_TARGET_LOG}")"

for flamegraph_format in svg flamegraph; do
	flamegraph_output="${HUATUO_BAMAI_TEST_TMPDIR}/${flamegraph_format}.svg"
	flamegraph_log="${HUATUO_BAMAI_TEST_TMPDIR}/${flamegraph_format}.log"
	if ! timeout 30 "${ROOT_DIR}/_output/bin/profiler" \
		--type cpu --language java --pid "${target_pid}" \
		--tool-path "${OUTPUT_FORMATS_TOOL_DIR}" --duration 1 --freq 100 --aggr-interval 1 \
		--output-format "${flamegraph_format}" --output-path "${flamegraph_output}" \
		> "${flamegraph_log}" 2>&1; then
		fatal "profiler ${flamegraph_format} failed: $(cat "${flamegraph_log}")"
	fi
	assert_flamegraph_geometry "${flamegraph_output}" "${HUATUO_BAMAI_TEST_TMPDIR}/${flamegraph_format}.tsv"
	assert_log_has_no_failure "${flamegraph_log}" "profiler ${flamegraph_format}"
done

for output_format in speedscope chrometrace; do
	extension=json
	output="${HUATUO_BAMAI_TEST_TMPDIR}/${output_format}.${extension}"
	output_log="${HUATUO_BAMAI_TEST_TMPDIR}/${output_format}.log"
	if ! timeout 30 "${ROOT_DIR}/_output/bin/profiler" \
		--type cpu --language java --pid "${target_pid}" \
		--tool-path "${OUTPUT_FORMATS_TOOL_DIR}" --duration 1 --freq 100 --aggr-interval 1 \
		--output-format "${output_format}" --output-path "${output}" \
		> "${output_log}" 2>&1; then
		fatal "profiler ${output_format} failed: $(cat "${output_log}")"
	fi
	"assert_${output_format}_output" "${output}"
	assert_log_has_no_failure "${output_log}" "profiler ${output_format}"
done

rejected_output="${HUATUO_BAMAI_TEST_TMPDIR}/memory-chrometrace.json"
rejected_log="${HUATUO_BAMAI_TEST_TMPDIR}/memory-chrometrace.log"
if "${ROOT_DIR}/_output/bin/profiler" \
	--type memory --language c --pid "$$" --memory-mode physical_alloc \
	--output-format chrometrace --output-path "${rejected_output}" \
	> "${rejected_log}" 2>&1; then
	fatal "profiler accepted --type=memory with --output-format=chrometrace"
fi
grep -Fq -- "--output-format=chrometrace needs a time-based profile" "${rejected_log}" \
	|| fatal "unexpected memory/chrometrace rejection: $(cat "${rejected_log}")"
[[ ! -e ${rejected_output} ]] \
	|| fatal "rejected memory/chrometrace run created ${rejected_output}"

# User-paced viewing must survive the 30-second collection finalization budget.
tui_capture_args=(
	--type cpu --language java --pid "${target_pid}"
	--tool-path "${OUTPUT_FORMATS_TOOL_DIR}"
	--freq 100 --aggr-interval 1 --log-level info --output-format tui
)
start_flamegraph_tui tui "${tui_capture_args[@]}" --duration 1 --output-path "${OUTPUT_FORMATS_TUI_REPORT}"
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
assert_flamegraph_tui_samples
[[ -s ${OUTPUT_FORMATS_TUI_REPORT} ]] || fatal "profiler did not save the TUI report after q"

start_flamegraph_tui tui-replay --tui-report "${OUTPUT_FORMATS_TUI_REPORT}"
wait_until 10 0.1 grep -q 'HUATUO tui flamegraph' "${flamegraph_tui_prefix}.raw" \
	|| fatal "profiler did not replay the saved TUI report: $(cat "${flamegraph_tui_prefix}.raw")"
printf q >&"${flamegraph_tui_input_fd}"
assert_flamegraph_tui_exit
assert_flamegraph_tui_samples

# A sampling stop must preserve final samples and later permit external TUI exit.
for exit_signal in TERM HUP QUIT; do
	start_flamegraph_tui "tui-exit-${exit_signal}" "${tui_capture_args[@]}" --duration 60
	wait_until 10 0.1 grep -q 'data reading loop started' "${flamegraph_tui_prefix}.raw" \
		|| fatal "profiler collection did not become ready: $(cat "${flamegraph_tui_prefix}.raw")"
	kill -INT "${flamegraph_tui_pid}"
	wait_until 10 0.1 grep -q 'HUATUO tui flamegraph' "${flamegraph_tui_prefix}.raw" \
		|| fatal "SIGINT did not publish the final TUI: $(cat "${flamegraph_tui_prefix}.raw")"
	kill "-${exit_signal}" "${flamegraph_tui_pid}"
	if [[ ${exit_signal} == TERM ]]; then
		assert_flamegraph_tui_exit
	else
		assert_flamegraph_tui_exit 1
		grep -Fq 'Error: context canceled' "${flamegraph_tui_prefix}.log" \
			|| fatal "SIG${exit_signal} did not cancel the TUI: $(cat "${flamegraph_tui_prefix}.log")"
	fi
	assert_flamegraph_tui_samples
done

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
