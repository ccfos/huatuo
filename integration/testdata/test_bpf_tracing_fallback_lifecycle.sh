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

# Kernel-free lifecycle fault tests for integration/test_bpf_tracing_fallback.sh.
#
# Each scenario sources the real script for its functions - the entry guard
# keeps sourcing from running the suite - and drives run_fixture inside a
# child bash with a stub fixture, a stub ip and a stub curl. A scenario passes
# when the child exits with the expected status, no fixture process survives
# the run, and the recorded artifacts match the contract: no traffic before
# the ready marker, every request liveness-checked against a live fixture,
# the window ended by the stop request, and a summary that never claims a
# deadline- or cap-ended collection as covered.
#
# usage: bash integration/testdata/test_bpf_tracing_fallback_lifecycle.sh

set -uo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
MAIN_SCRIPT="${ROOT_DIR}/integration/test_bpf_tracing_fallback.sh"
WORK_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/rxlat-lifecycle.XXXXXXXX")
EVIDENCE_ROOT="${WORK_ROOT}/evidence"
FIXTURE_PID_FILE="${WORK_ROOT}/fixture.pid"
CURL_COUNT_FILE="${WORK_ROOT}/curl.count"
export FIXTURE_PID_FILE CURL_COUNT_FILE

scenarios_failed=()

# report <name> <want> <got> [detail]
report() {
	local name=$1 want=$2 got=$3 detail=${4:-}
	if [[ "${want}" == "${got}" ]]; then
		printf 'PASS %s\n' "${name}"
	else
		printf 'FAIL %s: want %s, got %s%s\n' \
			"${name}" "${want}" "${got}" "${detail:+ (${detail})}"
		scenarios_failed+=("${name}")
	fi
}

# report_eq <name> <want> <got>
report_eq() {
	local name=$1 want=$2 got=$3
	[[ "${want}" == "${got}" ]]
	report "${name}" "${want}" "${got}"
}

# The stub fixture ignores every argument the script passes and selects its
# behavior by SCENARIO. Long waits come in short increments so a KILL never
# leaves a child sleep behind.
STUB_BIN="${WORK_ROOT}/stubs"
mkdir -p "${STUB_BIN}"
cat > "${STUB_BIN}/stub-fixture" << 'STUB_EOF'
#!/usr/bin/env bash
echo "$$" > "${FIXTURE_PID_FILE}"
case "${SCENARIO}" in
happy)
	echo '{"ready":true,"mode":"kprobe","entry_point":"tcp_v4_rcv_prog"}'
	stopped=0
	trap 'stopped=1' TERM
	for ((i = 0; i < 600 && stopped == 0; i++)); do
		sleep 0.1
	done
	echo '{"event":"net_rx_latency","stage":"RX_STAGE_TCPV4","saddr":"10.200.1.1","daddr":"10.200.1.2"}'
	echo '{"summary":true,"entry_point":"tcp_v4_rcv_prog","mode":"kprobe","events":1,"duplicates":0,"lost_samples":0,"stages":["RX_STAGE_TCPV4"],"timeout":false}'
	;;
capability)
	echo "stub: the kernel cannot attach this tracing entry point" >&2
	exit 3
	;;
not-ready-error)
	echo "stub fixture boom" >&2
	exit 1
	;;
never-ready | curl-fail)
	[[ "${SCENARIO}" == "curl-fail" ]] && \
		echo '{"ready":true,"mode":"kprobe","entry_point":"tcp_v4_rcv_prog"}'
	for ((i = 0; i < 1200; i++)); do
		sleep 0.1
	done
	;;
early-exit)
	echo '{"ready":true,"mode":"kprobe","entry_point":"tcp_v4_rcv_prog"}'
	echo '{"event":"net_rx_latency","stage":"RX_STAGE_TCPV4","saddr":"10.200.1.1","daddr":"10.200.1.2"}'
	sleep 0.5
	echo '{"summary":true,"entry_point":"tcp_v4_rcv_prog","mode":"kprobe","events":1,"duplicates":0,"lost_samples":0,"stages":["RX_STAGE_TCPV4"],"timeout":true}'
	exit 0
	;;
summary-timeout)
	echo '{"ready":true,"mode":"kprobe","entry_point":"tcp_v4_rcv_prog"}'
	stopped=0
	trap 'stopped=1' TERM
	for ((i = 0; i < 600 && stopped == 0; i++)); do
		sleep 0.1
	done
	echo '{"event":"net_rx_latency","stage":"RX_STAGE_TCPV4","saddr":"10.200.1.1","daddr":"10.200.1.2"}'
	echo '{"summary":true,"entry_point":"tcp_v4_rcv_prog","mode":"kprobe","events":1,"duplicates":0,"lost_samples":0,"stages":["RX_STAGE_TCPV4"],"timeout":true}'
	;;
summary-capped)
	echo '{"ready":true,"mode":"kprobe","entry_point":"tcp_v4_rcv_prog"}'
	stopped=0
	trap 'stopped=1' TERM
	for ((i = 0; i < 600 && stopped == 0; i++)); do
		sleep 0.1
	done
	echo '{"event":"net_rx_latency","stage":"RX_STAGE_TCPV4","saddr":"10.200.1.1","daddr":"10.200.1.2"}'
	echo '{"summary":true,"entry_point":"tcp_v4_rcv_prog","mode":"kprobe","events":8192,"duplicates":0,"lost_samples":0,"stages":["RX_STAGE_TCPV4"],"timeout":false}'
	;;
term-ignored)
	echo '{"ready":true,"mode":"kprobe","entry_point":"tcp_v4_rcv_prog"}'
	trap '' TERM
	for ((i = 0; i < 1200; i++)); do
		sleep 0.1
	done
	;;
esac
exit 0
STUB_EOF

cat > "${STUB_BIN}/curl" << 'STUB_EOF'
#!/usr/bin/env bash
# Stub curl: records every request, delays like a short real request, and can
# fail on a chosen request number (CURL_FAIL_AT).
sleep 0.15
n=$(cat "${CURL_COUNT_FILE}" 2> /dev/null || echo 0)
n=$((n + 1))
printf '%s\n' "${n}" > "${CURL_COUNT_FILE}"
if [[ -n "${CURL_FAIL_AT:-}" && "${n}" -eq "${CURL_FAIL_AT}" ]]; then
	exit 7
fi
printf 'OK'
STUB_EOF

cat > "${STUB_BIN}/ip" << 'STUB_EOF'
#!/usr/bin/env bash
# Stub ip: "netns exec <ns> <cmd>" runs <cmd> directly; nothing else exists.
if [[ "${1}" == "netns" && "${2}" == "exec" ]]; then
	shift 3
	exec "$@"
fi
echo "unexpected ip invocation: $*" >&2
exit 1
STUB_EOF
chmod +x "${STUB_BIN}/stub-fixture" "${STUB_BIN}/curl" "${STUB_BIN}/ip"

# The child sources the real script and runs one scenario. Fault-injection
# waits shrink to seconds; everything else is the script under test.
CHILD="${WORK_ROOT}/child.sh"
cat > "${CHILD}" << 'CHILD_EOF'
set -euo pipefail
source "${MAIN_SCRIPT}"

READY_WAIT_SECONDS=2
STOP_WAIT_SECONDS=2
FIXTURE_BIN="${STUB_BIN}/stub-fixture"
WORK_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/run"
mkdir -p "${WORK_DIR}"
export PATH="${STUB_BIN}:${PATH}"

TCP_NS_CLIENT="stub-client"
TCP_NS_CLIENT_ADDR="10.200.1.1"
TCP_NS_SERVER_ADDR="10.200.1.2"

case "${SCENARIO}" in
happy)
	run_fixture kprobe tcp_v4_rcv_prog
	save_run_evidence
	;;
capability)
	status=0
	run_fixture fentry tcp_v4_rcv_fentry_prog || status=$?
	[[ "${status}" -eq 3 ]] || {
		echo "capability scenario: want run_fixture rc 3, got ${status}" >&2
		exit 9
	}
	requests=$(cat "${CURL_COUNT_FILE}" 2> /dev/null || echo 0)
	[[ "${requests}" -eq 0 ]] || {
		echo "capability scenario generated ${requests} requests" >&2
		exit 9
	}
	;;
evidence-refusal)
	RXLAT_EVIDENCE_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/inside" save_run_evidence
	echo "save_run_evidence accepted a directory inside the test workspace" >&2
	exit 9
	;;
not-ready-error | never-ready | curl-fail | early-exit | summary-timeout | summary-capped | term-ignored)
	run_fixture kprobe tcp_v4_rcv_prog
	;;
*)
	echo "unknown scenario ${SCENARIO}" >&2
	exit 9
	;;
esac
CHILD_EOF

# child_run <scenario> <workspace> [extra env as NAME=VALUE...]
child_run() {
	local scenario=$1 workspace=$2
	shift 2
	# Per-run counters: leftovers from an earlier scenario would otherwise
	# masquerade as traffic this scenario never generated.
	rm -f -- "${CURL_COUNT_FILE}" "${FIXTURE_PID_FILE}"
	env SCENARIO="${scenario}" \
		MAIN_SCRIPT="${MAIN_SCRIPT}" \
		STUB_BIN="${STUB_BIN}" \
		ROOT_DIR="${ROOT_DIR}" \
		HUATUO_BAMAI_TEST_TMPDIR="${workspace}" \
		RXLAT_EVIDENCE_DIR="${EVIDENCE_ROOT}" \
		"$@" \
		bash "${CHILD}" > "${workspace}/child.log" 2>&1
}

# curl_requests reports the request count the stub curl recorded.
curl_requests() {
	cat "${CURL_COUNT_FILE}" 2> /dev/null || echo 0
}

# assert_order <file> <pattern>... asserts the first match of each pattern
# appears in the given order.
assert_order() {
	local file=$1
	shift
	local prev=0 line pattern
	for pattern in "$@"; do
		line=$(grep -n "${pattern}" "${file}" 2> /dev/null | head -1 | cut -d: -f1)
		if [[ -z "${line}" ]]; then
			report "order in $(basename "${file}")" "found ${pattern}" "missing"
			return
		fi
		if ((line <= prev)); then
			report "order in $(basename "${file}")" \
				"${pattern} after line ${prev}" "found at line ${line}"
			return
		fi
		prev=${line}
	done
}

# assert_no_orphan <label> fails when the stub fixture outlived the scenario.
assert_no_orphan() {
	local label=$1 pid
	pid=$(cat "${FIXTURE_PID_FILE}" 2> /dev/null || echo "")
	if [[ -n "${pid}" ]] && kill -0 "${pid}" 2> /dev/null; then
		report "no orphan fixture after ${label}" "gone" "pid ${pid} alive"
		kill -9 "${pid}" 2> /dev/null || true
	else
		report "no orphan fixture after ${label}" "gone" "gone"
	fi
}

run_dir_of() {
	echo "$1/run"
}

echo "=== happy path: ready, 20 requests, stop, summary ==="
ws=$(mktemp -d "${WORK_ROOT}/ws.XXXXXXX")
child_run happy "${ws}"
report "happy: run_fixture passes" 0 "$?"
timeline="$(run_dir_of "${ws}")/timeline.log"
assert_order "${timeline}" \
	'ready-confirmed mode=kprobe' \
	'request=1 start' \
	'request=20 done rc=0' \
	'stop-sent' \
	'fixture-exited rc=0'
report_eq "happy: 20 controlled requests" 20 "$(curl_requests)"
evidence_dir=$(ls -d "${EVIDENCE_ROOT}"/tracing-fallback.* 2> /dev/null | head -1)
if [[ -d "${evidence_dir}" ]]; then
	report "happy: evidence saved in a unique directory" ok ok
	for file in fixture-kprobe.json fixture-kprobe.err timeline.log; do
		[[ -e "${evidence_dir}/${file}" ]] \
			&& report "happy: evidence holds ${file}" ok ok \
			|| report "happy: evidence holds ${file}" "present" "missing"
	done
else
	report "happy: evidence saved in a unique directory" "created" "missing"
fi
assert_no_orphan "happy"

echo "=== capability: fixture exits 3 before ready, no traffic ==="
ws=$(mktemp -d "${WORK_ROOT}/ws.XXXXXXX")
child_run capability "${ws}"
report "capability: scenario passes" 0 "$?"
report_eq "capability: no traffic generated" 0 "$(curl_requests)"
err_file="$(run_dir_of "${ws}")/fixture-fentry.err"
[[ -s "${err_file}" ]] \
	&& report "capability: stderr preserved" ok ok \
	|| report "capability: stderr preserved" "present" "missing or empty"
assert_no_orphan "capability"

echo "=== evidence: a directory inside the workspace is refused ==="
ws=$(mktemp -d "${WORK_ROOT}/ws.XXXXXXX")
child_run evidence-refusal "${ws}"
report "evidence-refusal: save_run_evidence fails" 1 "$?"

echo "=== not-ready-error: ordinary early exit fails the run ==="
ws=$(mktemp -d "${WORK_ROOT}/ws.XXXXXXX")
child_run not-ready-error "${ws}"
report "not-ready-error: run_fixture fails" 1 "$?"
report_eq "not-ready-error: no traffic generated" 0 "$(curl_requests)"
grep -q "stub fixture boom" "${ws}/child.log" \
	&& report "not-ready-error: fixture stderr surfaced" ok ok \
	|| report "not-ready-error: fixture stderr surfaced" "present" "missing"
assert_no_orphan "not-ready-error"

echo "=== never-ready: bounded failure with cleanup ==="
ws=$(mktemp -d "${WORK_ROOT}/ws.XXXXXXX")
child_run never-ready "${ws}"
report "never-ready: run_fixture fails" 1 "$?"
report_eq "never-ready: no traffic generated" 0 "$(curl_requests)"
assert_no_orphan "never-ready"

echo "=== curl-fail: a failing request stops the traffic ==="
ws=$(mktemp -d "${WORK_ROOT}/ws.XXXXXXX")
child_run curl-fail "${ws}" CURL_FAIL_AT=3
report "curl-fail: run_fixture fails" 1 "$?"
report_eq "curl-fail: stopped at the failing request" 3 "$(curl_requests)"
assert_no_orphan "curl-fail"

echo "=== early-exit: an early exit 0 must not pass on early events ==="
ws=$(mktemp -d "${WORK_ROOT}/ws.XXXXXXX")
child_run early-exit "${ws}"
report "early-exit: run_fixture fails" 1 "$?"
grep -q "collection window ended early" "${ws}/child.log" \
	&& report "early-exit: premature exit reported" ok ok \
	|| report "early-exit: premature exit reported" "present" "missing"
assert_no_orphan "early-exit"

echo "=== summary-timeout: a deadline-ended window is not covered ==="
ws=$(mktemp -d "${WORK_ROOT}/ws.XXXXXXX")
child_run summary-timeout "${ws}"
report "summary-timeout: run_fixture fails" 1 "$?"
report_eq "summary-timeout: all requests ran" 20 "$(curl_requests)"
grep -q "not observed end to end" "${ws}/child.log" \
	&& report "summary-timeout: deadline reported" ok ok \
	|| report "summary-timeout: deadline reported" "present" "missing"
assert_no_orphan "summary-timeout"

echo "=== summary-capped: a cap-ended window is not covered ==="
ws=$(mktemp -d "${WORK_ROOT}/ws.XXXXXXX")
child_run summary-capped "${ws}"
report "summary-capped: run_fixture fails" 1 "$?"
grep -q "event cap" "${ws}/child.log" \
	&& report "summary-capped: cap reported" ok ok \
	|| report "summary-capped: cap reported" "present" "missing"
assert_no_orphan "summary-capped"

echo "=== term-ignored: bounded KILL, no residue ==="
ws=$(mktemp -d "${WORK_ROOT}/ws.XXXXXXX")
child_run term-ignored "${ws}"
report "term-ignored: run_fixture fails" 1 "$?"
grep -q "on the stop request" "${ws}/child.log" \
	&& report "term-ignored: stop failure reported" ok ok \
	|| report "term-ignored: stop failure reported" "present" "missing"
assert_no_orphan "term-ignored"

rm -rf -- "${WORK_ROOT}"
if ((${#scenarios_failed[@]} > 0)); then
	printf 'lifecycle scenarios FAILED: %s\n' "${scenarios_failed[*]}"
	exit 1
fi
echo "all lifecycle scenarios passed"
