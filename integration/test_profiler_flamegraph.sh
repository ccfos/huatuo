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

# Exercise the built profiler from deterministic async-profiler output to SVG.
# JVM sampling and symbolization are outside this output-contract test.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

readonly FLAMEGRAPH_TOOL_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/tools"
readonly FLAMEGRAPH_CLASSES_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/classes"
readonly FLAMEGRAPH_TARGET_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/target.log"

require_commands java javac python3 timeout

flamegraph_target_pid=""
cleanup() {
	[[ -n ${flamegraph_target_pid} ]] && stop_by_pid "${flamegraph_target_pid}" 5 || true
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
