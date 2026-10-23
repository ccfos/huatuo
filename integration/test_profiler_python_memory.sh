#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

# A real attach, signed deltas across intervals, and Python/C SVG boundaries.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler_python_memory.sh"

readonly PYTHON_BIN="${PYTHON_MEMORY_BIN:-python3}"
readonly PROFILER_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/memory-profiler.log"
readonly PROFILE_SVG="${HUATUO_BAMAI_TEST_TMPDIR}/memory.svg"
readonly STARTUP_CONTROL="${HUATUO_BAMAI_TEST_TMPDIR}/memory-startup-control"
require_commands "${PYTHON_BIN}" python3 jq timeout gdb ss
memory_require_runtime "$("${PYTHON_BIN}" -c 'import sys; print("%d.%d" % sys.version_info[:2])')"

MEMORY_TARGET_PID=""
profiler_pid=""
cleanup() {
	if [[ -n "${profiler_pid}" ]]; then
		kill -CONT "${profiler_pid}" 2> /dev/null || true
		stop_and_wait_by_pid "${profiler_pid}" 3 || true
	fi
	if [[ -n "${MEMORY_TARGET_PID}" ]]; then
		kill -TERM "${MEMORY_TARGET_PID}" 2> /dev/null || true
		wait "${MEMORY_TARGET_PID}" 2> /dev/null || true
	fi
}
trap cleanup EXIT

memory_compile_fixture
"${PYTHON_BIN}" -u "${MEMORY_FIXTURE}" "${MEMORY_LIBRARY}" "${STARTUP_CONTROL}" \
	> "${MEMORY_FIXTURE_LOG}" 2> "${MEMORY_FIXTURE_ERR}" &
MEMORY_TARGET_PID=$!
memory_wait_for_ack ready

"${ROOT_DIR}/_output/bin/profiler" \
	--type memory --language python --pid "${MEMORY_TARGET_PID}" \
	--tool-path "${MEMORY_BUNDLE}" --duration 30 --aggr-interval 1 \
	--python-memory-stack hybrid --python-memory-merge-threads \
	--output-format flamegraph --output-path "${PROFILE_SVG}" --verbose \
	> "${PROFILER_LOG}" 2>&1 &
profiler_pid=$!
memory_wait_for_ack tracking
# Another profiler process must fail without ending the first capture.
duplicate_log="${HUATUO_BAMAI_TEST_TMPDIR}/duplicate-profiler.log"
duplicate_status=0
timeout 5 "${ROOT_DIR}/_output/bin/profiler" \
	--type memory --language python --pid "${MEMORY_TARGET_PID}" \
	--tool-path "${MEMORY_BUNDLE}" --duration 2 --aggr-interval 1 \
	--output-format flamegraph --output-path "${HUATUO_BAMAI_TEST_TMPDIR}/duplicate.svg" \
	> "${duplicate_log}" 2>&1 || duplicate_status=$?
[[ "${duplicate_status}" -ne 0 && "${duplicate_status}" -ne 124 ]] \
	|| fatal "duplicate profiler was not rejected promptly: $(cat "${duplicate_log}")"
grep -q 'Memray session already active' "${duplicate_log}" \
	|| fatal "duplicate profiler failed for an unrelated reason: $(cat "${duplicate_log}")"
memory_request_phase allocated

# Make the free a later delta, rather than letting alloc/free cancel in the decoder.
delivered_before=$(grep -c 'memray deltas flushed' "${PROFILER_LOG}" || true)
deltas_delivered() {
	kill -0 "${profiler_pid}" 2> /dev/null \
		|| fatal "profiler exited before collection: $(cat "${PROFILER_LOG}")"
	local delivered
	delivered=$(grep -c 'memray deltas flushed' "${PROFILER_LOG}" || true)
	[[ "${delivered}" -gt "${delivered_before}" ]]
}
wait_until 5 0.1 deltas_delivered || fatal "profiler did not deliver allocation deltas"
memory_request_phase released
delivered_before=$(grep -c 'memray deltas flushed' "${PROFILER_LOG}" || true)
wait_until 5 0.1 deltas_delivered || fatal "profiler did not deliver free deltas"

# SIGINT is normal completion. The large duration is only a failure ceiling.
kill -INT "${profiler_pid}"
profiler_finished() { ! kill -0 "${profiler_pid}" 2> /dev/null; }
wait_until 8 0.1 profiler_finished || fatal "profiler did not stop after SIGINT"
wait "${profiler_pid}" || fatal "profiler failed: $(cat "${PROFILER_LOG}")"
profiler_pid=""
memory_wait_for_ack deactivated
svg_frames="${HUATUO_BAMAI_TEST_TMPDIR}/memory-frames.json"
# Extract XML attributes only. Shell owns all profile and geometry assertions.
python3 - "${PROFILE_SVG}" > "${svg_frames}" << 'PY'
import json
import sys
import xml.etree.ElementTree as ET

namespace = {"s": "http://www.w3.org/2000/svg"}
frames = []
for group in ET.parse(sys.argv[1]).getroot().findall("s:g", namespace):
    title = group.find("s:title", namespace)
    rect = group.find("s:rect", namespace)
    if title is not None and rect is not None:
        frames.append({"title": title.text, "x": rect.get("x"),
                       "y": rect.get("y"), "width": rect.get("width")})
json.dump(frames, sys.stdout)
PY
assert_eq "$(jq 'length > 0 and all(.[];
	.title | test("^.+ \\([0-9]+ bytes, [0-9.]+%\\)$"))' "${svg_frames}")" true \
	"SVG frames must have nonnegative byte values"
normalized_frames="${HUATUO_BAMAI_TEST_TMPDIR}/memory-frame-values.json"
jq 'map(. + (.title | capture("^(?<name>.*) \\((?<bytes>[0-9]+) bytes, [0-9.]+%\\)$")) |
	.bytes |= tonumber | .x |= tonumber | .y |= tonumber | .width |= tonumber)' \
	"${svg_frames}" > "${normalized_frames}"
assert_eq "$(jq --arg name "pid=\"${MEMORY_TARGET_PID}\"" \
	'any(.[]; .name == $name)' "${normalized_frames}")" true "SVG host PID label"

assert_memory_bytes() {
	local name=$1 expected=$2 tolerance=$3 actual
	actual=$(jq --arg name "${name}" '[.[] | select(.name == $name) | .bytes] | add // 0' "${normalized_frames}")
	[[ "${actual}" -ge "${expected}" && "${actual}" -le $((expected + tolerance)) ]] \
		|| fatal "${name}: ${actual} bytes, expected ${expected} (+${tolerance})"
	log_info "${name}: ${actual} bytes"
}
# Python/ctypes bookkeeping may contribute small allocations at the same frame.
assert_memory_bytes retain_memory 16777216 65536
assert_memory_bytes temporary_memory 0 65536
assert_memory_bytes mapped_memory 8388608 65536
assert_memory_bytes memray_test_retain 16777216 0
assert_memory_bytes memray_test_map 8388608 0
for pair in retain_memory:memray_test_retain mapped_memory:memray_test_map; do
	assert_eq "$(jq --arg caller "${pair%%:*}" --arg callee "${pair#*:}" '
		. as $frames | all(.[] | select(.name == $callee);
			. as $child | any($frames[];
				.name == $caller and .y > $child.y and .x <= ($child.x + 0.2) and
				(.x + .width + 0.2) >= ($child.x + $child.width)))' "${normalized_frames}")" \
		true "native frame must be below its Python caller: ${pair}"
done
assert_log_has_no_failure "${PROFILER_LOG}" profiler

# Cancel after the real pinned runtime starts accept(), before its data reader
# can connect. No production flags or replacement Memray implementation.
startup_log="${HUATUO_BAMAI_TEST_TMPDIR}/startup-profiler.log"
startup_svg="${HUATUO_BAMAI_TEST_TMPDIR}/startup.svg"
target_threads_before=$(find "/proc/${MEMORY_TARGET_PID}/task" -mindepth 1 -maxdepth 1 -type d | wc -l)
kill -USR2 "${MEMORY_TARGET_PID}"
memory_wait_for_ack startup-cancel-armed
(
	printf '%s\n' "${BASHPID}" > "${STARTUP_CONTROL}"
	exec "${ROOT_DIR}/_output/bin/profiler" \
		--type memory --language python --pid "${MEMORY_TARGET_PID}" \
		--tool-path "${MEMORY_BUNDLE}" --duration 30 --aggr-interval 1 \
		--output-format flamegraph --output-path "${startup_svg}" --verbose
) > "${startup_log}" 2>&1 &
profiler_pid=$!
memory_wait_for_ack startup-waiting
startup_port=$(jq -er '.port' "${STARTUP_CONTROL}.waiting")
cancel_path=$(jq -er '.cancel_path' "${STARTUP_CONTROL}.waiting")
[[ "${cancel_path}" == /tmp/.huatuo-memray-cancel-"${MEMORY_TARGET_PID}"-* ]] \
	|| fatal "unexpected cancellation path: ${cancel_path}"
startup_listening() { [[ -n "$(ss -H -lnt "sport = :${startup_port}")" ]]; }
wait_until 3 0.05 startup_listening || fatal "runtime did not start its data listener"
touch "${cancel_path}"
startup_released() {
	local threads
	kill -0 "${MEMORY_TARGET_PID}" 2> /dev/null || fatal "target exited during startup cancellation"
	threads=$(find "/proc/${MEMORY_TARGET_PID}/task" -mindepth 1 -maxdepth 1 -type d | wc -l)
	! startup_listening && [[ "${threads}" -le "${target_threads_before}" ]]
}
wait_until 3 0.05 startup_released || fatal "canceled startup leaked its listener or threads"
kill -INT "${profiler_pid}"
kill -CONT "${profiler_pid}"
wait_until 8 0.1 profiler_finished || fatal "profiler did not finish canceled startup"
if wait "${profiler_pid}"; then
	fatal "startup cancellation reported a successful capture"
fi
profiler_pid=""
grep -q 'collect Memray session' "${startup_log}" \
	|| fatal "profiler failed for an unrelated reason: $(cat "${startup_log}")"
[[ ! -s "${startup_svg}" ]] || fatal "canceled startup published a profile"
rm -f -- "${cancel_path}"

# A fresh attach must succeed on the same Python process, without restarting it.
retry_log="${HUATUO_BAMAI_TEST_TMPDIR}/retry-profiler.log"
"${ROOT_DIR}/_output/bin/profiler" \
	--type memory --language python --pid "${MEMORY_TARGET_PID}" \
	--tool-path "${MEMORY_BUNDLE}" --duration 30 --aggr-interval 1 \
	--output-format json --output-path "${HUATUO_BAMAI_TEST_TMPDIR}/retry.json" --verbose \
	> "${retry_log}" 2>&1 &
profiler_pid=$!
retry_tracking() { [[ "$(grep -cx tracking "${MEMORY_FIXTURE_LOG}" || true)" -eq 2 ]]; }
wait_until 10 0.1 retry_tracking || fatal "next attach failed: $(cat "${retry_log}")"
kill -INT "${profiler_pid}"
wait_until 8 0.1 profiler_finished || fatal "next capture did not stop"
wait "${profiler_pid}" || fatal "next capture failed: $(cat "${retry_log}")"
profiler_pid=""
assert_log_has_no_failure "${retry_log}" profiler
! grep -q 'Traceback\|Failed to write output' "${MEMORY_FIXTURE_ERR}" \
	|| fatal "target tracking failed: $(cat "${MEMORY_FIXTURE_ERR}")"
