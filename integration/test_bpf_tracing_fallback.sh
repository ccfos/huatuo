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

# Verify that net_rx_latency loads the entry point its kernel supports and
# still reports real latency stages:
#   - the kprobe-only object attaches exactly the kprobe entry point,
#   - the object carrying both entry points selects fentry on a kernel that
#     supports it and kprobe otherwise,
#   - both attach exactly one link per hook and report RX_STAGE_TCPV4 for real
#     veth traffic.
# The fixture sets nanosecond thresholds, so the test asserts the stage itself
# instead of waiting for a latency the virtual network may never reach.
#
# Every mode runs inside one explicit collection lifecycle: the fixture is
# registered for cleanup the moment it starts, traffic begins only after its
# ready marker, every request is checked while the fixture is still alive, and
# the window is ended by a stop request whose summary must prove the whole
# paced traffic was covered (timeout=false, under the event cap). The
# lifecycle functions are exercised without a kernel by
# integration/testdata/test_bpf_tracing_fallback_lifecycle.sh.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

# The slow server fixture binds 10.200.1.2, so this test uses the same pair as
# test_events_net_rx_latency.sh. Each test owns its own namespaces, so the
# sequential runner does not conflict.
SERVER_IP="10.200.1.2"
CLIENT_IP="10.200.1.1"
TEST_PORT=19876

# Worst case, the paced traffic needs 20 requests of up to 2s each plus 19
# gaps of 0.6s - 51.4s. The collection window must outlive that on a busy
# host: a deadline that lands inside the traffic ends the observation before
# the last requests, and the summary then reads as covered when it is not.
EVENT_TIMEOUT=60s
# The event cap belongs to the fixture alone. 4096 events can be spent in
# ~41s at the nominal 100/s rate limit, inside the worst-case traffic, so the
# cap has to stay far above anything the paced run can produce.
FIXTURE_EVENTS=8192

READY_WAIT_SECONDS=10
STOP_WAIT_SECONDS=10
FIXTURE_EXIT_UNSUPPORTED=3

FIXTURE_BIN=""
_server_pid=""
_fixture_pid=""
WORK_DIR=""
readonly GO_CACHE_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-cache"
readonly GO_TMP_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-tmp"

cleanup_all() {
	# The fixture first: whatever went wrong - a failing request, a ready
	# timeout, a stop that was ignored - a still-running collector must not
	# outlive the test. Both helpers below are safe on an empty or already
	# reaped PID, so repeated cleanup is harmless.
	if [[ -n "${_fixture_pid}" ]]; then
		stop_and_wait_by_pid "${_fixture_pid}" "${STOP_WAIT_SECONDS}" || true
		_fixture_pid=""
	fi
	[[ -n "${_server_pid}" ]] && stop_by_pid "${_server_pid}" 2 || true
	tcp_namespace_cleanup
	rm -rf -- "${GO_CACHE_DIR}" "${GO_TMP_DIR}"
}
trap cleanup_all EXIT

# record_run_event appends one line to the run timeline. The millisecond
# stamps are what lets a reviewer check the order of ready, every request and
# the stop against the fixture's own output.
record_run_event() {
	printf '%s %s\n' "$(date +%s%3N)" "$*" >> "${WORK_DIR}/timeline.log"
}

# assert_fixture_alive fails the run when the fixture exited before the stop
# request. A fixture that ends early closes the collection window before the
# traffic it must observe is over, so events it already reported prove
# nothing about the requests still to come.
assert_fixture_alive() {
	local when=$1

	kill -0 "${_fixture_pid}" 2> /dev/null \
		|| fatal "net_rx_tracing fixture exited on its own ${when}: the collection window ended early"
}

# start_fixture launches the fixture for one mode and registers its PID in
# the global _fixture_pid, so the EXIT trap reclaims it on every failure
# path - including a fatal inside traffic generation.
start_fixture() {
	local mode=$1
	local out="${WORK_DIR}/fixture-${mode}.json"
	local err="${WORK_DIR}/fixture-${mode}.err"

	"${FIXTURE_BIN}" \
		-bpf-dir "${ROOT_DIR}/_output/bpf" \
		-mode "${mode}" \
		-events "${FIXTURE_EVENTS}" \
		-timeout "${EVENT_TIMEOUT}" \
		> "${out}" 2> "${err}" &
	_fixture_pid=$!
}

# await_ready waits, bounded, for the ready marker of this mode's output file.
#
# A fixed sleep cannot cover the load and probe time a kernel needs: traffic
# sent before the attach is traffic the hook never sees. Only the fixture's
# own exit 3 - the capability answer of a demanding entry point - passes
# through to the caller; any other early exit and a ready that never arrives
# fail the run instead of reading as "unsupported".
await_ready() {
	local mode=$1
	local out="${WORK_DIR}/fixture-${mode}.json"
	local err="${WORK_DIR}/fixture-${mode}.err"
	local deadline=$(($(date +%s) + READY_WAIT_SECONDS))

	while :; do
		if grep -q '"ready":true' "${out}" 2> /dev/null; then
			record_run_event "ready-confirmed mode=${mode}"
			return 0
		fi
		if ! kill -0 "${_fixture_pid}" 2> /dev/null; then
			local status=0
			wait "${_fixture_pid}" || status=$?
			_fixture_pid=""
			cat "${err}" >&2 || true
			if [[ "${status}" -eq "${FIXTURE_EXIT_UNSUPPORTED}" ]]; then
				log_error "net_rx_tracing mode ${mode} exited ${status}: the kernel cannot attach this entry point"
				record_run_event "capability-refused mode=${mode} rc=${status}"
				return "${FIXTURE_EXIT_UNSUPPORTED}"
			fi
			fatal "net_rx_tracing mode ${mode} exited with ${status} before it reported ready"
		fi
		(($(date +%s) < deadline)) \
			|| fatal "net_rx_tracing mode ${mode} did not report ready within ${READY_WAIT_SECONDS}s"
		sleep 0.2
	done
}

generate_traffic() {
	# --noproxy: an http_proxy in the environment sends curl to the proxy
	# instead of the peer, which fails instantly and generates no packet at all.
	#
	# The requests spread over the collection window instead of arriving as one
	# burst: the object rate-limits its events for the whole system, and on a
	# busy host (etcd, apiserver) a burst races that budget in a single window
	# and can lose all of it at once, which reads as an unobserved entry point.
	# Requests landing in later windows still reach the hook whenever the host
	# goes quiet between its own traffic. Every request is verified: a failed
	# request is a traffic-generation failure and must not be reported as a
	# missing BPF event.
	local i body status
	for i in $(seq 1 20); do
		assert_fixture_alive "before controlled request ${i}"
		record_run_event "request=${i} start"
		status=0
		body=$(ip netns exec "${TCP_NS_CLIENT}" curl -s --noproxy '*' \
			--connect-timeout 1 --max-time 2 \
			"http://${TCP_NS_SERVER_ADDR}:${TEST_PORT}/") || status=$?
		[[ ${status} -eq 0 && "${body}" == "OK" ]] \
			|| fatal "controlled request ${i} to ${TCP_NS_SERVER_ADDR}:${TEST_PORT} failed (rc=${status}, body=${body:-empty})"
		record_run_event "request=${i} done rc=${status}"
		assert_fixture_alive "after controlled request ${i}"
		if [[ ${i} -lt 20 ]]; then
			sleep 0.6
		fi
	done
}

# stop_fixture ends the collection window the way the fixture documents: the
# stop request keeps the events already read and still prints the summary.
# The wait is bounded; a fixture that ignores the request is killed and its
# run fails instead of hanging the test.
stop_fixture() {
	local status=0

	record_run_event "stop-sent"
	stop_and_wait_by_pid "${_fixture_pid}" "${STOP_WAIT_SECONDS}" || status=$?
	_fixture_pid=""
	record_run_event "fixture-exited rc=${status}"
	[[ "${status}" -eq 0 ]] \
		|| fatal "net_rx_tracing fixture exited with ${status} on the stop request, want 0"
}

# check_summary verifies the one summary the fixture prints for this mode.
#
# The window counts only if it ended on the stop request (timeout=false) and
# the event cap never cut it short: a deadline- or cap-ended summary means the
# last requests may have fallen outside the collection, and early events must
# not be read as end-to-end coverage.
check_summary() {
	local mode=$1 expected=$2
	local out="${WORK_DIR}/fixture-${mode}.json"

	local summaries
	summaries=$(jq -s '[.[] | select(.summary == true)] | length' "${out}")
	[[ "${summaries}" -eq 1 ]] \
		|| fatal "mode ${mode} printed ${summaries} summary lines, want exactly one"

	local entry_point
	entry_point=$(jq -r 'select(.summary == true) | .entry_point' "${out}")
	[[ "${entry_point}" == "${expected}" ]] \
		|| fatal "mode ${mode} selected ${entry_point}, want ${expected}"

	# Repeated packet keys are a diagnostic, not a verdict: two connections can
	# reuse a client port and an initial sequence number, and the object
	# rate-limits its events. Exactly one entry point in the loaded object is
	# what rules out a double attach.
	local duplicates
	duplicates=$(jq -r 'select(.summary == true) | .duplicates' "${out}")
	if [[ "${duplicates}" != "0" ]]; then
		log_warn "mode ${mode} repeated ${duplicates} packet keys; raw events in ${out}"
	fi

	local tcpv4_events
	tcpv4_events=$(jq -s --arg saddr "${TCP_NS_CLIENT_ADDR}" --arg daddr "${TCP_NS_SERVER_ADDR}" \
		'[.[] | select(.summary != true)
			| select(.stage == "RX_STAGE_TCPV4")
			| select((.saddr == $saddr and .daddr == $daddr) or (.saddr == $daddr and .daddr == $saddr))] | length' \
		"${out}")
	[[ "${tcpv4_events}" -gt 0 ]] \
		|| fatal "mode ${mode} reported no RX_STAGE_TCPV4 event for ${TCP_NS_CLIENT_ADDR} <-> ${TCP_NS_SERVER_ADDR}"

	local summary_events timed_out
	summary_events=$(jq -r 'select(.summary == true) | .events' "${out}")
	timed_out=$(jq -r 'select(.summary == true) | .timeout' "${out}")
	[[ "${timed_out}" == "false" ]] \
		|| fatal "mode ${mode} collection window ended on its own ${EVENT_TIMEOUT} deadline with ${summary_events} events; the traffic was not observed end to end"
	[[ "${summary_events}" -lt "${FIXTURE_EVENTS}" ]] \
		|| fatal "mode ${mode} collection hit the ${FIXTURE_EVENTS} event cap before the traffic ended"

	log_info "mode ${mode}: entry_point=${entry_point} tcpv4_events=${tcpv4_events} events=${summary_events} duplicates=${duplicates}"
	record_run_event "mode=${mode} entry_point=${entry_point} tcpv4_events=${tcpv4_events} events=${summary_events} duplicates=${duplicates} timeout=${timed_out}"
}

# run_fixture <mode> <expected-entry-point>
#
# Returns 0 when the fixture observed the full paced traffic through a
# stop-ended window and selected the entry point it was told to expect, 1 for
# any other failure, and 3 when the kernel cannot attach the entry point a
# demanding mode asked for.
run_fixture() {
	local mode=$1 expected=$2

	log_info "running fixture in mode ${mode}, expecting ${expected}"
	start_fixture "${mode}"

	if ! await_ready "${mode}"; then
		# The fixture's own capability answer: the caller decides whether a
		# fallback entry point exists to verify instead.
		return "${FIXTURE_EXIT_UNSUPPORTED}"
	fi

	assert_fixture_alive "after ready, before the first controlled request"
	generate_traffic
	assert_fixture_alive "after the last controlled request"

	stop_fixture
	check_summary "${mode}" "${expected}"
}

# fentry_capable reports whether this kernel can attach the fentry entry point.
#
# The attempt is the answer. A kernel version or a readable BTF file only
# suggests it, so predicting from those would turn a correct fallback into a
# test failure; and a failure that is not a missing capability stops the test
# instead of being read as one.
fentry_capable() {
	local status=0

	run_fixture fentry tcp_v4_rcv_fentry_prog || status=$?
	case "${status}" in
	0) return 0 ;;
	3) return 1 ;;
	*) fatal "the fentry entry point failed with ${status}, which is not a missing capability" ;;
	esac
}

# save_run_evidence copies the run's raw records out of the test workspace
# before the runner deletes it - but only on request: RXLAT_EVIDENCE_DIR is a
# manual-verification convenience, and left unset it changes nothing.
save_run_evidence() {
	local dest_root=${RXLAT_EVIDENCE_DIR:-}
	[[ -n "${dest_root}" ]] || return 0

	case "${dest_root}" in
	"${HUATUO_BAMAI_TEST_TMPDIR}" | "${HUATUO_BAMAI_TEST_TMPDIR}"/*)
		fatal "RXLAT_EVIDENCE_DIR must live outside ${HUATUO_BAMAI_TEST_TMPDIR}: ${dest_root}"
		;;
	esac

	mkdir -p "${dest_root}" \
		|| fatal "cannot create RXLAT_EVIDENCE_DIR ${dest_root}"
	local dest
	dest=$(mktemp -d "${dest_root}/tracing-fallback.XXXXXXXX") \
		|| fatal "cannot create a run directory under RXLAT_EVIDENCE_DIR ${dest_root}"

	local file
	for file in "${WORK_DIR}"/fixture-*.json "${WORK_DIR}"/fixture-*.err "${WORK_DIR}"/timeline.log; do
		[[ -e "${file}" ]] || continue
		cp -- "${file}" "${dest}/" \
			|| fatal "cannot save ${file} into ${dest}"
	done
	log_info "run evidence saved to ${dest}"
}

compile_go_fixture() {
	local build_log="${WORK_DIR}/fixture.build.log"

	mkdir -p "${GO_CACHE_DIR}" "${GO_TMP_DIR}"
	log_info "building fixture: test_net_rx_tracing.go"
	GOCACHE="${GO_CACHE_DIR}" \
		GOTMPDIR="${GO_TMP_DIR}" \
		go build -mod=vendor -tags=integration -o "${FIXTURE_BIN}" \
		"${ROOT_DIR}/integration/testdata/test_net_rx_tracing.go" \
		> "${build_log}" 2>&1 \
		|| fatal "failed to build net_rx_tracing:"$'\n'"$(< "${build_log}")"
}

# A stop request during a read is the other half of the window this fixture
# reports: without the events that read already had, the summary disappears
# exactly when a caller that measured traffic needs it. Covered here, next to
# the fixture it belongs to, with a reader that cancels on demand.
run_collect_tests() {
	local test_log="${WORK_DIR}/collect.log"

	mkdir -p "${GO_CACHE_DIR}" "${GO_TMP_DIR}"
	log_info "testing the collection window: cancel during a read"
	GOCACHE="${GO_CACHE_DIR}" \
		GOTMPDIR="${GO_TMP_DIR}" \
		go test -mod=vendor -tags=integration -count=1 -v \
		-run '^TestCollectEvents' \
		"${ROOT_DIR}/integration/testdata/test_net_rx_tracing.go" \
		"${ROOT_DIR}/integration/testdata/test_net_rx_tracing_collect_test.go" \
		> "${test_log}" 2>&1 \
		|| fatal "the collection window tests failed:"$'\n'"$(< "${test_log}")"
}

main() {
	WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/net-rx-tracing.XXXXXX")

	tcp_namespace_setup rxlatfb "${SERVER_IP}" "${CLIENT_IP}"
	sleep 0.5

	FIXTURE_BIN="${WORK_DIR}/net_rx_tracing"
	compile_go_fixture

	SLOW_TCP_SERVER="${WORK_DIR}/slow-tcp-server"
	compile_user_fixture \
		"${ROOT_DIR}/integration/testdata/test_net_rx_latency_user.c" \
		"${SLOW_TCP_SERVER}"

	ip netns exec "${TCP_NS_SERVER}" "${SLOW_TCP_SERVER}" \
		> "${WORK_DIR}/testserver.log" 2>&1 &
	server_pid=$!
	_server_pid="${server_pid}"
	sleep 0.5

	run_collect_tests

	# The kprobe-only object never carries an fentry program: this is the path
	# kernels without fentry support take, and it must keep working.
	run_fixture kprobe tcp_v4_rcv_prog

	# Whatever the kernel can do, the automatic selection has to agree with it,
	# and the fallback path has to report the stage just like the preferred one.
	if fentry_capable; then
		log_info "the kernel attaches fentry to tcp_v4_rcv, the automatic selection must use it"
		run_fixture auto tcp_v4_rcv_fentry_prog
	else
		log_info "the kernel cannot attach fentry to tcp_v4_rcv, the automatic selection must fall back"
		run_fixture auto tcp_v4_rcv_prog
	fi

	save_run_evidence

	log_info "net_rx_latency tracing variant test passed"
}

# The lifecycle fault tests source this file for the functions above and drive
# them with stub fixtures and stub traffic; run the real suite only when the
# script itself is executed.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
	main "$@"
fi
