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

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"

require_commands jq docker timeout getconf ss
require_commands "${ROOT_DIR}/_output/bin/perf"
require_readable /proc/stat "${ROOT_DIR}/_output/bpf/perf.o"

readonly CPUSYS_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/cpusys"
readonly CPUSYS_BIN="${CPUSYS_DIR}/reference"
readonly CPUSYS_PREFIX="huatuo_e2e_cpusys_${BASHPID}_${RANDOM}"
readonly CPUSYS_BPF_DIR=$(cd "${ROOT_DIR}/_output/bpf" && pwd)
readonly CPUSYS_STARTED=$(date +%s)
export CPUSYS_REAL_PERF="${ROOT_DIR}/_output/bin/perf"
CPUSYS_INTERVAL=2
CPUSYS_DURATION=2
CPUSYS_COOLDOWN=60
CPUSYS_DISABLED=false
CPUSYS_USAGE=0
CPUSYS_DELTA=0
CPUSYS_INDEX="${CPUSYS_PREFIX}_invalid"
cpusys_generation=0
cpusys_worker_pid=""
cpusys_queries_pid=""
mkdir -p "${CPUSYS_DIR}/tools"
uname -a > "${CPUSYS_DIR}/kernel.txt"
"${HUATUO_BAMAI_BIN}" --version > "${CPUSYS_DIR}/bamai-version.txt"

cpusys_perf_released() {
	local calls pid started args current
	while IFS= read -r calls; do
		while IFS=$'\t' read -r pid started args; do
			current=$(awk '{print $22}' "/proc/${pid}/stat" 2> /dev/null) || continue
			[[ ${current} != "${started}" ]] || return 1
		done < "${calls}"
	done < <(find "${CPUSYS_DIR}" -name perf.calls -type f)
}

cpusys_cleanup() {
	local status=$? calls pid started args current
	trap - EXIT
	stop_and_wait_by_pid "${cpusys_queries_pid}" || true
	stop_and_wait_by_pid "${cpusys_worker_pid}" || true
	huatuo_bamai_stop || status=1
	# A killed parent may leave its bounded perf subprocess running briefly.
	while IFS= read -r calls; do
		while IFS=$'\t' read -r pid started args; do
			current=$(awk '{print $22}' "/proc/${pid}/stat" 2> /dev/null) || continue
			if [[ ${current} == "${started}" ]]; then stop_by_pid "${pid}" || status=1; fi
		done < "${calls}"
	done < <(find "${CPUSYS_DIR}" -name perf.calls -type f)
	storage_dump_logs "${CPUSYS_DIR}/elasticsearch.log" || status=1
	storage_stop || status=1
	log_info "cpusys artifacts: ${CPUSYS_DIR}; elapsed=$(($(date +%s) - CPUSYS_STARTED))s cleanup status=${status}"
	exit "${status}"
}
trap cpusys_cleanup EXIT

if [[ -n ${CPUSYS_HELPER_BIN:-} ]]; then
	require_commands "${CPUSYS_HELPER_BIN}"
	cp "${CPUSYS_HELPER_BIN}" "${CPUSYS_BIN}"
else
	require_commands go
	CGO_ENABLED=0 go test -mod=vendor "${ROOT_DIR}/e2e/testdata/cpusys"
	CGO_ENABLED=0 go build -mod=vendor -o "${CPUSYS_BIN}" "${ROOT_DIR}/e2e/testdata/cpusys"
fi
if [[ -n ${CPU_UTIL_HELPER_BIN:-} ]]; then
	require_commands "${CPU_UTIL_HELPER_BIN}"
	cp "${CPU_UTIL_HELPER_BIN}" "${CPUSYS_DIR}/cpusys-load"
else
	require_commands go
	CGO_ENABLED=0 go build -mod=vendor -o "${CPUSYS_DIR}/cpusys-load" "${ROOT_DIR}/e2e/testdata/cpu_util"
fi
cp "${ROOT_DIR}/e2e/testdata/cpusys/perf" "${CPUSYS_DIR}/tools/perf"
chmod 755 "${CPUSYS_DIR}/tools/perf"
CPUSYS_PORT=$(allocate_available_port) || fatal "cannot allocate cpusys API port"
readonly CPUSYS_PORT

if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]]; then
	huatuo_bamai_stop
	huatuo_bamai_log_check || fatal "runner baseline daemon has unexpected errors"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${CPUSYS_DIR}/baseline.log"
fi
HUATUO_BAMAI_ADDR="http://127.0.0.1:${CPUSYS_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

cpusys_status() {
	local args=()
	[[ ${CPUSYS_DISABLED} != true ]] || args+=(--disabled)
	"${CPUSYS_BIN}" status --url "${HUATUO_BAMAI_METRICS_API}" "${args[@]}"
}

cpusys_worker_ready() {
	grep -Fxq "ready ${cpusys_generation}" "${CPUSYS_DIR}/workload.log"
}

cpusys_command() {
	cpusys_generation=$((cpusys_generation + 1))
	printf '%s 0 %s %s\n' "$1" "${CPUSYS_WORKERS}" "${cpusys_generation}" > "${CPUSYS_DIR}/command.next"
	mv "${CPUSYS_DIR}/command.next" "${CPUSYS_DIR}/command"
	wait_until 10 0.2 cpusys_worker_ready || fatal "worker did not accept $1 command"
}

cpusys_reference() {
	local name=$1 duration=${2:-4}
	"${CPUSYS_BIN}" sample --duration "${duration}s" > "${HUATUO_BAMAI_TEST_TMPDIR}/${name}.reference.jsonl"
	jq -r --arg phase "${cpusys_phase}" --arg name "${name}" \
		'"cpusys reference: phase=\($phase) sample=\($name) at=\(.at) system=\(.system)% delta=\(.delta)pp"' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/${name}.reference.jsonl"
	case ${name} in
	baseline-*)
		jq -es --argjson threshold "${CPUSYS_TRIGGER_USAGE}" \
			'.[-2:] | all(.[]; .system < $threshold)' "${HUATUO_BAMAI_TEST_TMPDIR}/${name}.reference.jsonl" \
			> /dev/null || fatal "${cpusys_phase}: host system CPU did not return below the trigger threshold"
		;;
	high* | suppressed)
		jq -es --argjson threshold "$((CPUSYS_TRIGGER_USAGE + CPUSYS_TRIGGER_DELTA))" \
			'.[-2:] | all(.[]; .system > $threshold)' "${HUATUO_BAMAI_TEST_TMPDIR}/${name}.reference.jsonl" \
			> /dev/null || fatal "${cpusys_phase}: workload did not produce sufficient host system CPU; inspect reference samples"
		;;
	esac
}

cpusys_local_events() {
	if [[ -s ${HUATUO_BAMAI_TEST_TMPDIR}/events/cpusys ]]; then
		jq -s '.' "${HUATUO_BAMAI_TEST_TMPDIR}/events/cpusys"
	else
		printf '[]\n'
	fi
}

cpusys_event_count() {
	cpusys_local_events | jq length
}

cpusys_ready_events() {
	[[ $(cpusys_event_count) -ge $1 ]]
}

cpusys_compare_storage() {
	cpusys_local_events > "${HUATUO_BAMAI_TEST_TMPDIR}/local.json" || return 1
	storage_curl --fail -X POST "${STORAGE_ADDR}/${CPUSYS_INDEX}/_refresh" > "${HUATUO_BAMAI_TEST_TMPDIR}/refresh.json" || return 1
	storage_curl --fail -X POST "${STORAGE_ADDR}/${CPUSYS_INDEX}/_search" -H 'Content-Type: application/json' \
		--data '{"size":1000,"track_total_hits":true,"query":{"match_all":{}}}' \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/search.json" || return 1
	jq -e '._shards.failed == 0 and .timed_out == false and .hits.total.relation == "eq"
		and .hits.total.value == (.hits.hits | length) and all(.hits.hits[]; ._id == ._source.tracer_id)' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/search.json" > /dev/null || return 1
	jq '[.hits.hits[]._source]' "${HUATUO_BAMAI_TEST_TMPDIR}/search.json" > "${HUATUO_BAMAI_TEST_TMPDIR}/remote.json"
	jq -e --slurpfile remote "${HUATUO_BAMAI_TEST_TMPDIR}/remote.json" \
		'(length == (map(.tracer_id) | unique | length)) and (sort_by(.tracer_id) == ($remote[0] | sort_by(.tracer_id)))' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/local.json" > /dev/null
}

cpusys_start() {
	export CPUSYS_CALLS="${HUATUO_BAMAI_TEST_TMPDIR}/perf.calls"
	integration_huatuo_bamai_start write_cpusys_e2e_config --region e2e --disable-kubelet \
		--tools-bin-dir "${CPUSYS_DIR}/tools" --bpf-dir "${CPUSYS_BPF_DIR}"
	wait_until 15 0.5 cpusys_status || fatal "${cpusys_phase}: cpusys status did not become ready"
}

cpusys_baseline() {
	cpusys_command idle
	cpusys_reference "baseline-${cpusys_generation}" 6
}

cpusys_begin() {
	cpusys_phase=$1
	HUATUO_BAMAI_TEST_TMPDIR="${CPUSYS_DIR}/${cpusys_phase}"
	CPUSYS_INDEX="${CPUSYS_PREFIX}_${cpusys_phase}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	storage_curl --fail -X PUT "${STORAGE_ADDR}/${CPUSYS_INDEX}" -H 'Content-Type: application/json' \
		--data '{"settings":{"number_of_shards":1,"number_of_replicas":0}}' > "${HUATUO_BAMAI_TEST_TMPDIR}/index.json"
	log_info "cpusys phase: ${cpusys_phase} usage=${CPUSYS_USAGE}% delta=${CPUSYS_DELTA}pp interval=${CPUSYS_INTERVAL}s duration=${CPUSYS_DURATION}s cooldown=${CPUSYS_COOLDOWN}s"
	cpusys_start
	cpusys_baseline
	[[ $(cpusys_event_count) -eq 0 && ! -s ${CPUSYS_CALLS} ]] \
		|| fatal "${cpusys_phase}: background CPU activity triggered tracing during the idle baseline; inspect references"
}

cpusys_assert_events() {
	local args=()
	[[ ${cpusys_phase} != cooldown ]] || args+=(--cooldown "${CPUSYS_COOLDOWN}s")
	cpusys_local_events > "${HUATUO_BAMAI_TEST_TMPDIR}/events.json"
	"${CPUSYS_BIN}" events --file "${HUATUO_BAMAI_TEST_TMPDIR}/events.json" \
		--usage "${CPUSYS_USAGE}" --delta "${CPUSYS_DELTA}" "${args[@]}"
	jq -r '.[] | "cpusys event: started_at=\(.started_timestamp) system=\(.tracer_data.system_percent)% delta=\(.tracer_data.system_percent_delta)pp samples=\(.tracer_data.flamedata[0].value)"' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/events.json"
}

cpusys_capture() {
	local before
	before=$(cpusys_event_count)
	cpusys_command system
	cpusys_reference "high-${cpusys_generation}"
	wait_until 15 0.5 cpusys_ready_events "$((before + 1))" || fatal "${cpusys_phase}: real system spike produced no cpusys event"
	cpusys_assert_events
	cpusys_status
}

cpusys_finish() {
	local signal=${1:-TERM} pid calls=0
	cpusys_command idle
	wait_until 10 0.5 cpusys_perf_released || fatal "${cpusys_phase}: perf subprocess did not exit"
	cpusys_status
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	if [[ ${signal} == KILL ]]; then
		wait_until 15 1 cpusys_compare_storage || fatal "completed cpusys records were not uploaded before SIGKILL"
		kill -KILL "${pid}"
		if wait "${pid}"; then fatal "SIGKILL did not terminate owned bamai"; else
			[[ $? -eq 137 ]] || fatal "owned bamai did not exit from SIGKILL"
		fi
	fi
	huatuo_bamai_stop
	if [[ ${signal} == TERM ]]; then wait "${pid}" || fatal "cpusys daemon did not exit gracefully"; fi
	huatuo_bamai_log_check || fatal "${cpusys_phase}: unexpected bamai error"
	! grep -Ei 'level="?warn|"level":"warn"' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "${cpusys_phase}: unexpected bamai warning"
	wait_until 15 1 cpusys_compare_storage || fatal "${cpusys_phase}: local and ES events differ"
	if [[ -f ${CPUSYS_CALLS} ]]; then
		calls=$(wc -l < "${CPUSYS_CALLS}")
		awk -F '\t' -v expected="--bpf-path ${CPUSYS_BPF_DIR}/perf.o --duration ${CPUSYS_DURATION}" \
			'NF != 3 || $3 != expected {exit 1}' "${CPUSYS_CALLS}" || fatal "${cpusys_phase}: unexpected perf arguments"
	fi
	[[ ${calls} -eq $(cpusys_event_count) ]] || fatal "${cpusys_phase}: perf invocation and completed event counts differ"
	log_info "cpusys storage: phase=${cpusys_phase} perf=${calls} local=$(jq length "${HUATUO_BAMAI_TEST_TMPDIR}/local.json") ES=$(jq length "${HUATUO_BAMAI_TEST_TMPDIR}/remote.json")"
}

cpusys_invalid_config() (
	local name=$1 expected=$2 status=0
	HUATUO_BAMAI_TEST_TMPDIR="${CPUSYS_DIR}/invalid-${name}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	case ${name} in
	interval) CPUSYS_INTERVAL=0 ;;
	cooldown) CPUSYS_COOLDOWN=0 ;;
	duration) CPUSYS_DURATION=0 ;;
	negative) CPUSYS_USAGE=-1 ;;
	percentage) CPUSYS_DELTA=101 ;;
	esac
	write_cpusys_e2e_config
	timeout 15 "${HUATUO_BAMAI_BIN}" --region e2e --disable-kubelet --config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" \
		--config bamai.conf > "${HUATUO_BAMAI_TEST_TMPDIR}/expected-error.log" 2>&1 || status=$?
	((status != 0 && status != 124 && status != 137)) || fatal "${name}: invalid cpusys config did not fail promptly"
	grep -Fq "${expected}" "${HUATUO_BAMAI_TEST_TMPDIR}/expected-error.log" || fatal "${name}: missing expected error: ${expected}"
	log_info "cpusys: rejected invalid ${name}"
)

cpusys_invalid_config interval 'sampling interval: timer duration must be positive'
cpusys_invalid_config cooldown 'minimum trace interval: timer duration must be positive'
cpusys_invalid_config duration 'perf duration must be positive'
cpusys_invalid_config negative 'system threshold: cpu percentage must be between 0 and 100'
cpusys_invalid_config percentage 'system delta threshold: cpu percentage must be between 0 and 100'

storage_start elasticsearch_v8
CPUSYS_WORKERS=$(getconf _NPROCESSORS_ONLN)
CPUSYS_WORKERS=$((CPUSYS_WORKERS / 2))
if ((CPUSYS_WORKERS < 1)); then CPUSYS_WORKERS=1; fi
if ((CPUSYS_WORKERS > 3)); then CPUSYS_WORKERS=3; fi
readonly CPUSYS_WORKERS
printf 'idle 0 %s 0\n' "${CPUSYS_WORKERS}" > "${CPUSYS_DIR}/command"
"${CPUSYS_DIR}/cpusys-load" load --control "${CPUSYS_DIR}/command" > "${CPUSYS_DIR}/workload.log" 2>&1 &
cpusys_worker_pid=$!
cpusys_phase=calibration
HUATUO_BAMAI_TEST_TMPDIR="${CPUSYS_DIR}"
cpusys_command idle
cpusys_reference idle 4
cpusys_command system
cpusys_reference busy 5
cpusys_command idle
"${CPUSYS_BIN}" thresholds --baseline "${CPUSYS_DIR}/idle.reference.jsonl" --busy "${CPUSYS_DIR}/busy.reference.jsonl" \
	> "${CPUSYS_DIR}/thresholds.json" || fatal "cannot construct a distinguishable system CPU spike; inspect calibration references"
CPUSYS_USAGE=$(jq .usage "${CPUSYS_DIR}/thresholds.json")
CPUSYS_DELTA=$(jq .delta "${CPUSYS_DIR}/thresholds.json")
readonly CPUSYS_TRIGGER_USAGE=${CPUSYS_USAGE} CPUSYS_TRIGGER_DELTA=${CPUSYS_DELTA}
log_info "cpusys workload: workers=${CPUSYS_WORKERS} thresholds=$(jq -c . "${CPUSYS_DIR}/thresholds.json")"

cpusys_begin positive
"${CPUSYS_BIN}" concurrent --url "${HUATUO_BAMAI_METRICS_API}" --duration 6s \
	> "${HUATUO_BAMAI_TEST_TMPDIR}/concurrent.log" 2>&1 &
cpusys_queries_pid=$!
cpusys_capture
wait "${cpusys_queries_pid}" || fatal "concurrent metrics queries failed"
cpusys_queries_pid=""
cpusys_finish

# Reuse the same config and event files to exercise an actual daemon restart.
cpusys_phase=restart
log_info "cpusys phase: restart"
cpusys_start
cpusys_baseline
cpusys_capture
[[ $(cpusys_event_count) -eq 2 ]] || fatal "restart: expected one new event"
cpusys_finish KILL

cpusys_begin kill-recovery
cpusys_capture
cpusys_finish

CPUSYS_USAGE=100
CPUSYS_DELTA=0
cpusys_begin usage-threshold
cpusys_command system
cpusys_reference high
cpusys_finish
[[ $(cpusys_event_count) -eq 0 ]] || fatal "system percentage equal/below 100 triggered tracing"

CPUSYS_USAGE=0
CPUSYS_DELTA=100
cpusys_begin delta-threshold
cpusys_command system
cpusys_reference high
cpusys_finish
[[ $(cpusys_event_count) -eq 0 ]] || fatal "system percentage delta equal/below 100 triggered tracing"

CPUSYS_USAGE=${CPUSYS_TRIGGER_USAGE}
CPUSYS_DELTA=${CPUSYS_TRIGGER_DELTA}
CPUSYS_COOLDOWN=30
cpusys_begin cooldown
cpusys_capture
first_started=$(cpusys_local_events | jq -r '.[0].started_timestamp')
cooldown_deadline=$(($(date -d "${first_started}" +%s) + CPUSYS_COOLDOWN + 1))
cpusys_baseline
cpusys_command system
cpusys_reference suppressed
(($(date +%s) < cooldown_deadline - CPUSYS_INTERVAL)) || fatal "VM too slow to exercise an in-cooldown spike"
[[ $(cpusys_event_count) -eq 1 ]] || fatal "cooldown: repeated tracing before interval elapsed"
cpusys_baseline
remaining=$((cooldown_deadline - $(date +%s)))
if ((remaining > 0)); then sleep "${remaining}"; fi
cpusys_capture
[[ $(cpusys_event_count) -eq 2 ]] || fatal "cooldown: expected tracing to resume after interval"
cpusys_finish

CPUSYS_DISABLED=true
cpusys_begin disabled
cpusys_command system
cpusys_reference high
cpusys_finish
[[ $(cpusys_event_count) -eq 0 ]] || fatal "blacklisted cpusys produced events"
