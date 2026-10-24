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

# Control diskstats at actual read boundaries and assert public capture,
# metrics and event behavior without inferring internal sampling times.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

require_commands jq python3
CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
WAIT_HUATUO_BAMAI_TIMEOUT=20

readonly WINDOW_ROOT="${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-windows"
readonly WINDOW_EVENTS="${HUATUO_BAMAI_TEST_TMPDIR}/events/iotracing"
supplier_pid=""
window_command_id=0
window_allowed_capture=0
window_first_capture=""
window_second_capture=""
window_capture=""
window_events=0
window_supplier_id=0
window_closed_count=0
window_has_supplied=false
window_published_ns=0
window_deadline_ms=0

cleanup_windows() {
	local status=$? executable pid
	trap - EXIT
	trap '' INT TERM
	huatuo_bamai_stop || status=1
	for executable in /proc/[0-9]*/exe; do
		[[ "${executable}" -ef "${WINDOW_ROOT}/bin/iotracing" ]] || continue
		pid=${executable%/exe}
		pid=${pid##*/}
		stop_by_pid "${pid}" 2
		if ! wait_until 2 0.1 test ! "${executable}" -ef "${WINDOW_ROOT}/bin/iotracing"; then
			log_error "capture remains: pid=${pid}; inspect ${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-windows.resources"
			status=1
		fi
	done
	if [[ -n ${supplier_pid} ]]; then
		stop_by_pid "${supplier_pid}" 2
		if ! wait_until 2 0.1 test ! -e "/proc/${supplier_pid}/exe"; then
			log_error "diskstats supplier remains: pid=${supplier_pid}; inspect iotracing-windows.resources"
			status=1
		fi
		wait "${supplier_pid}" 2> /dev/null || true
	fi
	exit "${status}"
}

trap cleanup_windows EXIT
trap 'log_error "iotracing windows interrupted by INT"; exit 130' INT
trap 'log_error "iotracing windows interrupted by TERM or execution timeout"; exit 143' TERM

mkdir -p "${WINDOW_ROOT}"/{bin,bpf,proc,dev,sys/dev/block/8:0,sys/dev/block/8:16}
cp "${HUATUO_BAMAI_BIN}" "${WINDOW_ROOT}/bin/huatuo-bamai"
cp "${ROOT_DIR}/_output/bin/iotracing" "${WINDOW_ROOT}/bin/iotracing"
cp "${ROOT_DIR}/_output/bpf/iotracing.o" "${WINDOW_ROOT}/bpf/iotracing.o"
HUATUO_BAMAI_BIN="${WINDOW_ROOT}/bin/huatuo-bamai"

write_window_config() {
	write_iotracing_autotracing_config
	sed -e 's/BlackList = \[/BlackList = ["diskio", /' \
		-e 's/RunTracingToolTimeout = 1$/RunTracingToolTimeout = 25/' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf.next"
	mv "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf.next" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
}

window_clock() {
	local uptime fraction
	read -r uptime _ < /proc/uptime
	fraction=${uptime#*.}000
	window_now_ms=$((10#${uptime%.*} * 1000 + 10#${fraction:0:3}))
}

window_input() {
	local phase=$1 mode=$2 read_vda=$3 write_vda=$4 read_vdb=$5 repeat=${6:-false}
	window_command_id=$((window_command_id + 1))
	printf '{"id":%s,"phase":"%s","mode":"%s","repeat":%s,"vda":[%s,%s],"vdb":[%s,0]}\n' \
		"${window_command_id}" "${phase}" "${mode}" "${repeat}" \
		"${read_vda}" "${write_vda}" "${read_vdb}" > "${WINDOW_ROOT}/supplier-command.next"
	mv "${WINDOW_ROOT}/supplier-command.next" "${WINDOW_ROOT}/supplier-command.json"
}

assert_window_input_state() {
	local unmatched overflow
	read -r window_supplier_id window_closed_count window_published_ns unmatched overflow window_has_supplied < <(
		jq -r '[.id,.closed_count,.published_ns,.unmatched_closes,.overflow_count,.has_supplied] | @tsv' \
			"${WINDOW_ROOT}/supplier-state.json"
	) || fatal "cannot read diskstats delivery state; inspect iotracing-windows.log"
	((unmatched == 0 && overflow == 0)) \
		|| fatal "diskstats read boundaries were lost: unmatched=${unmatched}, overflow=${overflow}"
}

assert_window_observation() {
	local executable pid stat identity
	local fields=() captures=()
	window_clock
	((window_now_ms < window_deadline_ms)) \
		|| fatal "240-second input/diagnostic budget exhausted; inspect diskstats-reads.jsonl"
	kill -0 "${supplier_pid}" 2> /dev/null \
		|| fatal "diskstats supplier exited; inspect iotracing-windows.log"
	[[ -r ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]] \
		|| fatal "Huatuo PID record disappeared during window checks"
	kill -0 "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")" 2> /dev/null \
		|| fatal "Huatuo exited during window checks; inspect its log"
	for executable in /proc/[0-9]*/exe; do
		[[ ${executable} -ef ${WINDOW_ROOT}/bin/iotracing ]] || continue
		pid=${executable%/exe}
		pid=${pid##*/}
		if ! read -r stat < "/proc/${pid}/stat"; then
			continue
		fi
		read -r -a fields <<< "${stat##*) }"
		identity="${pid}:${fields[19]}"
		captures+=("${identity}")
	done
	((${#captures[@]} <= 1)) || fatal "concurrent automatic captures: ${captures[*]}"
	window_capture=${captures[0]:-}
	if [[ -n ${window_capture} ]]; then
		((window_allowed_capture > 0)) || fatal "automatic capture before qualifying consecutive intervals: ${window_capture}"
		if [[ -z ${window_first_capture} ]]; then
			assert_window_input_state
			((window_supplier_id == window_command_id)) && [[ ${window_has_supplied} == true ]] \
				|| fatal "automatic capture preceded delivery of the second consecutive high interval"
			window_first_capture=${window_capture}
			printf 'capture_pid=%s starttime=%s ordinal=1\n' "${window_capture%%:*}" "${window_capture#*:}" >> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-windows.resources"
		elif [[ ${window_capture} != "${window_first_capture}" ]]; then
			# Refresh after observing a new PID so a simultaneous second read-close
			# cannot be mistaken for a capture triggered by only one new interval.
			assert_window_input_state
			((window_allowed_capture == 2 && window_supplier_id == window_command_id && window_closed_count >= 2)) \
				|| fatal "queued or premature automatic capture: ${window_capture}"
			if [[ -z ${window_second_capture} ]]; then
				window_second_capture=${window_capture}
				printf 'capture_pid=%s starttime=%s ordinal=2\n' "${window_capture%%:*}" "${window_capture#*:}" >> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-windows.resources"
			fi
			[[ ${window_capture} == "${window_second_capture}" ]] || fatal "unexpected third automatic capture: ${window_capture}"
		fi
	fi
	if [[ -s ${WINDOW_EVENTS} ]]; then
		# A public storage record can be observed while its JSON is being written.
		if jq -s . "${WINDOW_EVENTS}" > "${WINDOW_ROOT}/events.next" 2> /dev/null; then
			mv "${WINDOW_ROOT}/events.next" "${WINDOW_ROOT}/events.json"
			window_events=$(jq 'length' "${WINDOW_ROOT}/events.json")
		fi
	fi
	((window_events <= 2)) || fatal "unexpected extra automatic diagnostics: ${window_events}"
	if ((window_events > 0)); then
		[[ -n ${window_first_capture} ]] || fatal "diagnostic saved before its real capture process was observed"
	fi
	if ((window_events > 1)); then
		[[ -n ${window_second_capture} ]] || fatal "second diagnostic appeared without a new qualifying capture"
	fi
}

assert_window_first_capture_active() {
	assert_window_observation
	[[ -n ${window_first_capture} && ${window_capture} == "${window_first_capture}" ]] \
		|| fatal "capture ended before the sustained anomaly was restored"
}

window_wait_delivery() {
	local kind=$1 goal=${2:-1} observation=${3:-assert_window_observation}
	while true; do
		"${observation}"
		assert_window_input_state
		if ((window_supplier_id == window_command_id)); then
			case ${kind} in
			closed) ((window_closed_count < goal)) || return 0 ;;
			published) ((window_published_ns == 0)) || return 0 ;;
			esac
		fi
		sleep 0.1
	done
}

window_wait_time() {
	local deadline=$1
	while true; do
		assert_window_first_capture_active
		((window_now_ms < deadline)) || return 0
		sleep 0.1
	done
}

assert_window_metrics() {
	local step=$1 expected=$2 file="${HUATUO_BAMAI_TEST_TMPDIR}/capture-metrics-${1}.txt"
	assert_window_first_capture_active
	huatuo_bamai_metrics > "${file}" || fatal "capture-time metrics request failed: step=${step}"
	assert_window_first_capture_active
	awk -v expected="${expected}" -v step="${step}" '
	/^huatuo_bamai_iotracing_read_await_ms\{/ && /disk="vdb"/ {
		count++
		if ($NF !~ /^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$/) invalid=1
		if (step == 4) { if ($NF < 0 || $NF > 2000) invalid=1 }
		else if ($NF != expected) invalid=1
	}
	END { exit count != 1 || invalid }
	' "${file}" || fatal "vdb capture-time read await mismatch: step=${step}, expected=${expected}; inspect ${file}"
}

assert_window_events() {
	jq -e '
		length == 2
		and ([.[].tracer_id] | unique | length) == 2
	' "${WINDOW_ROOT}/events.json" > /dev/null || fatal "automatic diagnostics did not have distinct tracer IDs"
	jq -e '
		.[0].tracer_data.reason_snapshot.device == "vda"
		and .[1].tracer_data.reason_snapshot.device == "vdb"
		and all(.[];
			.tracer_name == "iotracing" and .tracer_type == "autotracing"
			and (.tracer_data.failure_reason // "") == ""
			and .tracer_data.reason_snapshot.type == "read_await"
			and .tracer_data.reason_snapshot.iostatus.read_await == 2000)
	' "${WINDOW_ROOT}/events.json" > /dev/null || fatal "unexpected automatic diagnostic reason or device"
}

window_input startup fifo 0 0 0 true
python3 "${ROOT_DIR}/integration/testdata/test_iotracing_windows.py" \
	--root "${WINDOW_ROOT}" --workspace "${HUATUO_BAMAI_TEST_TMPDIR}" \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-windows.log" 2>&1 &
supplier_pid=$!
printf 'supplier_pid=%s fixture=%s\n' "${supplier_pid}" "${WINDOW_ROOT}" \
	>> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-windows.resources"
wait_until 5 0.1 test -f "${WINDOW_ROOT}/supplier.ready" \
	|| fatal "diskstats supplier did not create its FIFO; inspect iotracing-windows.log"

integration_huatuo_bamai_start write_window_config \
	--region dev --procfs-prefix "${WINDOW_ROOT}" --disable-kubelet \
	--disable-tracing blk_throtl,io_health,irqtracing,tcp_retransmit --log-debug
window_clock
window_deadline_ms=$((window_now_ms + 240000))

sequence_names=(baseline consecutive_high consecutive_high)
sequence_read=(0 2000 2000)
for index in "${!sequence_names[@]}"; do
	# The final high read may legitimately start the capture before close is seen.
	if ((index == ${#sequence_names[@]} - 1)); then
		window_allowed_capture=1
	fi
	window_input "${sequence_names[index]}" fifo "${sequence_read[index]}" 0 0
	window_wait_delivery closed
done
while [[ -z ${window_first_capture} ]]; do
	assert_window_observation
	sleep 0.1
done
log_info "PASS: consecutive high input started the first real capture"

window_input capture_metrics_0 regular 0 0 0
window_wait_delivery published 1 assert_window_first_capture_active
assert_window_metrics 0 0
window_input sustained_anomaly regular 0 0 2000
window_wait_delivery published 1 assert_window_first_capture_active
window_clock
high_started_ms=${window_now_ms}
window_wait_time $((high_started_ms + 1100))
assert_window_metrics 1 2000
window_wait_time $((high_started_ms + 8000))
assert_window_metrics 2 2000
window_wait_time $((high_started_ms + 17000))
assert_window_metrics 3 2000
window_clock
((window_now_ms - high_started_ms >= 16000)) || fatal "capture-time anomaly did not last at least 16 seconds"
printf 'sustained_anomaly_ms=%s first_capture=%s\n' "$((window_now_ms - high_started_ms))" "${window_first_capture}" \
	>> "${HUATUO_BAMAI_TEST_TMPDIR}/iotracing-windows.resources"
window_input capture_normal regular 0 0 0
window_wait_delivery published 1 assert_window_first_capture_active
window_clock
window_wait_time $((window_now_ms + 1100))
# The first normal scrape drains the final partial anomaly interval.
assert_window_metrics 4 0
window_clock
window_wait_time $((window_now_ms + 1100))
assert_window_metrics 5 0
while [[ -n ${window_capture} ]] || ((window_events != 1)); do
	assert_window_observation
	sleep 0.1
done

window_input post_capture regular 0 0 0
window_wait_delivery published
window_wait_delivery closed 3
log_info "PASS A05: transient capture-time anomaly was not queued"
window_input renewed_high regular 0 0 2000
window_allowed_capture=2
window_wait_delivery published
window_wait_delivery closed 2
while [[ -z ${window_second_capture} || -n ${window_capture} ]] || ((window_events != 2)); do
	assert_window_observation
	sleep 0.1
done
assert_window_events
log_info "PASS A05: new consecutive anomaly triggered a second real capture"
log_info "NOT_RUN A03: normal or invalid intervals between anomalies are not covered"
