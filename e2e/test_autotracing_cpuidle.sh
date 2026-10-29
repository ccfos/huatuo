#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_cgroup.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/e2e/lib.sh"
source "${ROOT_DIR}/e2e/lib_cpu_load.sh"
source "${ROOT_DIR}/integration/lib_cpu_metrics.sh"
source "${ROOT_DIR}/integration/lib_cpu_reference.sh"

require_commands kubectl jq findmnt getconf docker timeout
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
[[ ${EUID} -eq 0 ]] || skip "requires root for container CPU profiling"

readonly CPUIDLE_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/cpuidle"
readonly CPUIDLE_PREFIX="cpuidle-${BASHPID}-${RANDOM}"
readonly CPUIDLE_NS="${CPUIDLE_PREFIX}"
readonly CPUIDLE_LABEL="app=${CPUIDLE_PREFIX}"
readonly CPUIDLE_INDEX="huatuo_e2e_${CPUIDLE_PREFIX}"
cpuidle_interval=2
cpuidle_duration=2
cpuidle_cooldown=30
cpuidle_disabled=false
cpuidle_thresholds=(100 100 40 100 100 10)
cpuidle_include_field=container_hostname
cpuidle_exclude_field=container_hostname
cpuidle_include="^${CPUIDLE_PREFIX}-(a|b)$"
cpuidle_exclude='^$'
cpuidle_generation=0
mkdir -p "${CPUIDLE_DIR}"
chmod 755 "${CPUIDLE_DIR}"
uname -a > "${CPUIDLE_DIR}/kernel.txt"
"${HUATUO_BAMAI_BIN}" --version > "${CPUIDLE_DIR}/bamai-version.txt"

cpuidle_cleanup() {
	local status=$?
	trap - EXIT
	cpu_metrics_stop_queries
	if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
		kubectl --request-timeout=10s describe pod -n "${CPUIDLE_NS}" -l "${CPUIDLE_LABEL}" \
			> "${CPUIDLE_DIR}/pods-failure.txt" 2>&1 || true
	fi
	kubectl --request-timeout=30s delete pod -n "${CPUIDLE_NS}" -l "${CPUIDLE_LABEL}" \
		--ignore-not-found --timeout=30s || status=1
	kubectl --request-timeout=30s delete namespace "${CPUIDLE_NS}" \
		--ignore-not-found --timeout=30s || status=1
	cpu_reference_stop || status=1
	huatuo_bamai_stop || status=1
	storage_dump_logs "${CPUIDLE_DIR}/elasticsearch.log" || status=1
	storage_stop || status=1
	log_info "cpuidle artifacts: ${CPUIDLE_DIR}; cleanup status=${status}"
	exit "${status}"
}
trap cpuidle_cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

cpu_load_prepare "${CPUIDLE_DIR}"
cpu_metrics_prepare
cpu_reference_start "${CPUIDLE_DIR}"

CPUIDLE_PORT=$(allocate_available_port) || fatal "cannot allocate cpuidle API port"
CPUIDLE_KUBELET_PORT=${KUBELET_PODS_API##*:}
CPUIDLE_KUBELET_PORT=${CPUIDLE_KUBELET_PORT%%/*}
CPUIDLE_NODE=$(kubelet_pods_json | jq -er '[.items[].spec.nodeName] | unique | if length == 1 then .[0] else error("cannot identify local node") end')
CPUIDLE_CPU_ROOT=$(findmnt -rn -t cgroup -O cpu -o TARGET || true)
if [[ -n ${CPUIDLE_CPU_ROOT} ]]; then
	CPUIDLE_USAGE_ROOT=$(findmnt -rn -t cgroup -O cpuacct -o TARGET)
	CPUIDLE_CPUSET_ROOT=$(findmnt -rn -t cgroup -O cpuset -o TARGET)
else
	CPUIDLE_CPU_ROOT=$(findmnt -rn -t cgroup2 -o TARGET) || skip "CPU cgroup controller is not mounted"
	CPUIDLE_USAGE_ROOT=${CPUIDLE_CPU_ROOT}
	CPUIDLE_CPUSET_ROOT=${CPUIDLE_CPU_ROOT}
fi
readonly CPUIDLE_PORT CPUIDLE_KUBELET_PORT CPUIDLE_NODE CPUIDLE_CPU_ROOT CPUIDLE_USAGE_ROOT CPUIDLE_CPUSET_ROOT

if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]]; then
	huatuo_bamai_stop
	assert_huatuo_bamai_log_clean || fatal "runner baseline daemon has unexpected errors"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${CPUIDLE_DIR}/baseline.log"
fi
HUATUO_BAMAI_ADDR="http://127.0.0.1:${CPUIDLE_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

cpuidle_status() {
	local enabled=1
	[[ ${cpuidle_disabled} != true ]] || enabled=0
	assert_cpu_tracing_status cpuidle "${enabled}"
}

assert_cpuidle_query() {
	local enabled=1
	[[ ${cpuidle_disabled} != true ]] || enabled=0
	assert_cpu_tracing_metrics cpuidle "${enabled}" "$1"
}

cpuidle_command() {
	local suffix=$1 mode=$2 milli=$3
	cpuidle_generation=$((cpuidle_generation + 1))
	printf '%s %s 1 %s\n' "${mode}" "${milli}" "${cpuidle_generation}" > "${CPUIDLE_DIR}/${suffix}.command.next"
	mv "${CPUIDLE_DIR}/${suffix}.command.next" "${CPUIDLE_DIR}/${suffix}.command"
	if [[ -f ${CPUIDLE_DIR}/${suffix}.id ]]; then
		wait_until 10 0.2 cpuidle_worker_ready "${suffix}" "${cpuidle_generation}" \
			|| fatal "${suffix}: worker did not accept ${mode}/${milli}m command"
	fi
}

cpuidle_worker_ready() {
	kubectl --request-timeout=5s logs -n "${CPUIDLE_NS}" "${CPUIDLE_PREFIX}-$1" --tail=5 \
		| grep -Fxq "ready $2"
}

cpuidle_create_pod() {
	local suffix=$1 quota=$2
	cpuidle_command "${suffix}" idle 0
	# Distinct executable names identify each container in sampled stacks.
	jq -n --arg pod "${CPUIDLE_PREFIX}-${suffix}" --arg ns "${CPUIDLE_NS}" --arg app "${CPUIDLE_PREFIX}" \
		--arg node "${CPUIDLE_NODE}" --arg image "${CPU_LOAD_IMAGE}" --arg dir "${CPUIDLE_DIR}" \
		--arg suffix "${suffix}" --arg quota "${quota}" '
		{apiVersion: "v1", kind: "Pod", metadata: {name: $pod, namespace: $ns, labels: {app: $app}},
		 spec: {nodeName: $node, terminationGracePeriodSeconds: 2,
		   containers: [{name: "worker", image: $image, imagePullPolicy: "Never",
		     command: ["/bin/sh", "-ec",
		       "cp /usr/bin/stress-ng \"$STRESS_NG_BIN\"; exec /work/cpu-load load --control \"$1\"",
		       "_", ("/work/" + $suffix + ".command")],
		     env: [{name: "STRESS_NG_BIN", value: ("/tmp/cpuidle-" + $suffix)}],
		     resources: {requests: {cpu: "10m", memory: "16Mi"}, limits: {cpu: $quota, memory: "64Mi"}},
		     volumeMounts: [{name: "work", mountPath: "/work", readOnly: true}]}],
		   volumes: [{name: "work", hostPath: {path: $dir, type: "Directory"}}]}}
	' > "${CPUIDLE_DIR}/${suffix}.pod.json"
	kubectl --request-timeout=20s create -f "${CPUIDLE_DIR}/${suffix}.pod.json"
	kubectl wait --for=condition=Ready pod -n "${CPUIDLE_NS}" "${CPUIDLE_PREFIX}-${suffix}" --timeout=90s
}

cpuidle_target() {
	local suffix=$1 quota=$2 id cpu usage cpuset
	assert_huatuo_bamai_containers_present "${CPUIDLE_NS}" "^${CPUIDLE_PREFIX}-${suffix}$" 1
	id=$(kubelet_container_ids "${CPUIDLE_NS}" "^${CPUIDLE_PREFIX}-${suffix}$")
	printf '%s\n' "${id}" > "${CPUIDLE_DIR}/${suffix}.id"
	cpu=$(cgroup_find_container "${CPUIDLE_CPU_ROOT}" "${id}") || fatal "${suffix}: CPU cgroup is missing"
	usage=$(cgroup_find_container "${CPUIDLE_USAGE_ROOT}" "${id}") || fatal "${suffix}: CPU accounting cgroup is missing"
	cpuset=$(cgroup_find_container "${CPUIDLE_CPUSET_ROOT}" "${id}") || fatal "${suffix}: cpuset cgroup is missing"
	jq -n --arg pod "${CPUIDLE_PREFIX}-${suffix}" --arg usage "${usage}" --arg cpu "${cpu}" \
		--arg cpuset "${cpuset}" --argjson quota "${quota}" \
		'[{Name: $pod, Pod: $pod, UsagePath: $usage, CPUPath: $cpu, CpusetPath: $cpuset, QuotaMilli: $quota}]' \
		> "${CPUIDLE_DIR}/${suffix}.target.json"
	cpu_reference_capacity "$(jq -c '.[0]' "${CPUIDLE_DIR}/${suffix}.target.json")" | jq .Cores > "${CPUIDLE_DIR}/${suffix}.cores"
	printf '%s\n' "${usage}" > "${CPUIDLE_DIR}/${suffix}.usage"
	log_info "cpuidle target: pod=${CPUIDLE_PREFIX}-${suffix} id=${id} capacity=$(< "${CPUIDLE_DIR}/${suffix}.cores") cgroup=${cpu}"
}

cpuidle_local_events() {
	if [[ -s ${HUATUO_BAMAI_TEST_TMPDIR}/events/cpuidle ]]; then
		jq -s '.' "${HUATUO_BAMAI_TEST_TMPDIR}/events/cpuidle"
	else
		printf '[]\n'
	fi
}

assert_cpuidle_recreated() {
	local previous=$1 actual
	actual=$(< "${CPUIDLE_DIR}/a.id")
	[[ ${actual} != "${previous}" ]] \
		|| fatal "${cpuidle_phase}: recreated Pod container ID: expected different from ${previous}, actual=${actual}; inspect ${CPUIDLE_DIR}/a.id"
}

cpuidle_event_count() {
	cpuidle_local_events | jq --arg id "$(< "${CPUIDLE_DIR}/$1.id")" '[.[] | select(.container_id == $id)] | length'
}

cpuidle_ready_events() {
	[[ $(cpuidle_event_count "$1") -ge $2 ]]
}

assert_cpuidle_no_events() {
	local file="${HUATUO_BAMAI_TEST_TMPDIR}/local.json" actual
	cpuidle_local_events > "${file}"
	actual=$(jq length "${file}")
	[[ ${actual} -eq 0 ]] \
		|| fatal "${cpuidle_phase}: cpuidle event count: expected=0, actual=${actual}; inspect ${file}"
	cpuidle_status
}

cpuidle_reference() {
	local suffix=$1 name=$2 duration=${3:-4}
	cpu_reference_sample "$(< "${CPUIDLE_DIR}/${suffix}.usage")" \
		"$(< "${CPUIDLE_DIR}/${suffix}.cores")" "${duration}" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/${name}.reference.jsonl"
	assert_cpuidle_reference "${suffix}" "${name}"
	log_info "cpuidle reference: phase=${cpuidle_phase} pod=${CPUIDLE_PREFIX}-${suffix} $(jq -c '.percent' "${HUATUO_BAMAI_TEST_TMPDIR}/${name}.reference.jsonl")"
}

assert_cpuidle_reference() {
	local suffix=$1 name=$2 mode milli expected
	read -r mode milli _ < "${CPUIDLE_DIR}/${suffix}.command"
	expected=$(jq -n --argjson milli "${milli}" --argjson cores "$(< "${CPUIDLE_DIR}/${suffix}.cores")" '$milli / 10 / $cores')
	jq -e --arg mode "${mode}" --argjson expected "${expected}" \
		--argjson system_threshold "${cpuidle_thresholds[1]}" '
		.percent | if $mode == "idle" then .total < 10 else
			.total >= $expected - 15 and .total <= $expected + 15
			and (if $mode == "user" then .user > 30
					elif $mode == "system" then .system > $system_threshold
				else .user > 2 and .system > 2 end) end
	' "${HUATUO_BAMAI_TEST_TMPDIR}/${name}.reference.jsonl" > /dev/null || fatal "${cpuidle_phase}/${name}: worker did not produce the independently verified ${mode} load"
}

assert_cpuidle_logs() {
	assert_huatuo_bamai_log_clean || fatal "cpuidle daemon logged an unexpected failure"
	# Discovery races are retried; target metadata and flame graphs are verified.
	awk '/level="?warn|"level":"warn"/ &&
		!/creation is not yet reflected by kubelet/ &&
		!/msg="lost BPF perf event samples".*func="internal\/pod\.cgroupCssEventSyncHandler\.func1"/ {
			print; unexpected = 1
		} END {exit unexpected}' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" \
		|| fatal "cpuidle daemon logged an unexpected warning"
}

cpuidle_start() {
	write_config_cpuidle_autotracing > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	huatuo_bamai_start --config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" --config bamai.conf --region e2e \
		--tools-bin-dir "${ROOT_DIR}/_output/bin"
	wait_until 15 0.5 cpuidle_status || fatal "cpuidle lifecycle metrics did not become ready"
}

cpuidle_finish() {
	local signal=${1:-TERM}
	if [[ ${signal} == KILL ]]; then
		# SIGKILL tests recovery, not persistence of records still queued for upload.
		wait_until 15 1 cpuidle_compare_storage || fatal "cannot confirm completed cpuidle records before SIGKILL"
	fi
	assert_huatuo_bamai_shutdown_with_signal "${signal}"
	assert_cpuidle_logs
	wait_until 15 1 cpuidle_compare_storage || fatal "cpuidle local/ES events differ"
	log_info "cpuidle storage: phase=${cpuidle_phase} local=$(jq length "${HUATUO_BAMAI_TEST_TMPDIR}/local.json") ES=$(jq length "${HUATUO_BAMAI_TEST_TMPDIR}/remote.json")"
}

cpuidle_compare_storage() {
	cpuidle_local_events > "${HUATUO_BAMAI_TEST_TMPDIR}/local.json"
	storage_request "${HUATUO_BAMAI_TEST_TMPDIR}/refresh.json" -X POST "${STORAGE_ADDR}/${CPUIDLE_INDEX}/_refresh" || return 1
	local query
	query=$(jq '{size: 1000, query: {ids: {values: map(.tracer_id)}}}' "${HUATUO_BAMAI_TEST_TMPDIR}/local.json")
	storage_request "${HUATUO_BAMAI_TEST_TMPDIR}/search.json" -X POST "${STORAGE_ADDR}/${CPUIDLE_INDEX}/_search" \
		-H 'Content-Type: application/json' --data "${query}" || return 1
	jq -e '._shards.failed == 0 and .timed_out == false and .hits.total.relation == "eq"
		and .hits.total.value == (.hits.hits | length) and all(.hits.hits[]; ._id == ._source.tracer_id)' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/search.json" > /dev/null || return 1
	jq '[.hits.hits[]._source]' "${HUATUO_BAMAI_TEST_TMPDIR}/search.json" > "${HUATUO_BAMAI_TEST_TMPDIR}/remote.json"
	jq -e --slurpfile remote "${HUATUO_BAMAI_TEST_TMPDIR}/remote.json" \
		'(length == (map(.tracer_id) | unique | length)) and (sort_by(.tracer_id) == ($remote[0] | sort_by(.tracer_id)))' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/local.json" > /dev/null
}

cpuidle_begin() {
	cpuidle_phase=$1
	HUATUO_BAMAI_TEST_TMPDIR="${CPUIDLE_DIR}/${cpuidle_phase}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	cpuidle_command a idle 0
	cpuidle_command b idle 0
	log_info "cpuidle phase: ${cpuidle_phase} thresholds=${cpuidle_thresholds[*]} interval=${cpuidle_interval}s duration=${cpuidle_duration}s cooldown=${cpuidle_cooldown}s"
	cpuidle_start
	cpuidle_target a 500
	cpuidle_target b 1000
	cpuidle_reference a baseline 8
}

assert_cpuidle_events() {
	local suffix=$1 channel=$2 forbid=b kernel=false
	[[ ${suffix} != b ]] || forbid=a
	[[ ${channel} != system ]] || kernel=true
	cpuidle_local_events | jq --arg id "$(< "${CPUIDLE_DIR}/${suffix}.id")" '[.[] | select(.container_id == $id)]' \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/${suffix}.events.json"
	jq -e --arg pod "${CPUIDLE_PREFIX}-${suffix}" --arg ns "${CPUIDLE_NS}" --arg host "$(hostname)" \
		--arg channel "${channel}" --argjson thresholds "$(printf '%s\n' "${cpuidle_thresholds[@]}" | jq -s '.')" '
		def integer: type == "number" and floor == .;
		def timestamp: type == "string" and test("^[0-9]{4}-.*Z$");
		length > 0 and all(.[];
			.tracer_name == "cpuidle" and .tracer_type == "autotracing"
			and .container_hostname == $pod and .container_host_namespace == $ns
			and .container_type == "normal" and .container_qos == "burstable"
			and .hostname == $host and .region == "e2e"
			and (.tracer_id | type == "string" and length > 0)
			and (.started_timestamp | timestamp) and (.uploaded_timestamp | timestamp)
			and .uploaded_timestamp >= .started_timestamp
			and (.tracer_data |
				([.user_percent, .system_percent, .total_percent] | all(.[]; integer and . >= 0))
				and ([.user_percent_delta, .system_percent_delta, .total_percent_delta] | all(.[]; integer))
				and .total_percent <= 85 and .user_percent <= 85 and .system_percent <= 85
				and .user_percent_delta <= .user_percent
				and .system_percent_delta <= .system_percent
				and .total_percent_delta <= .total_percent
				and [.user_percent_threshold, .system_percent_threshold, .total_percent_threshold,
					.user_percent_delta_threshold, .system_percent_delta_threshold, .total_percent_delta_threshold] == $thresholds
				and .[$channel + "_percent"] > .[$channel + "_percent_threshold"]
				and .[$channel + "_percent_delta"] > .[$channel + "_percent_delta_threshold"]))
	' "${HUATUO_BAMAI_TEST_TMPDIR}/${suffix}.events.json" > /dev/null || fatal "${cpuidle_phase}: invalid metadata, thresholds or trigger data"
	jq -e --arg comm "cpuidle-${suffix}" --arg forbid "cpuidle-${forbid}" --argjson kernel "${kernel}" '
		all(.[]; .tracer_data.flamedata |
			length >= 2 and .[0].level == 0 and .[0].value > 0
			and all(.[]; (.level | type == "number" and . >= 0)
				and (.value | type == "number" and . > 0)
				and (.self | type == "number" and . >= 0) and .self <= .value
				and (.label | type == "string" and length > 0) and .label != $forbid)
			and any(.[]; .label == $comm)
			and (if $kernel then any(.[]; .label | endswith("_[k]")) else true end))
	' "${HUATUO_BAMAI_TEST_TMPDIR}/${suffix}.events.json" > /dev/null \
		|| fatal "${cpuidle_phase}: invalid flame graph or unexpected worker"
	jq -r '.[] | "cpuidle event: pod=\(.container_hostname) started_at=\(.started_timestamp) usr=\(.tracer_data.user_percent)% sys=\(.tracer_data.system_percent)% total=\(.tracer_data.total_percent)% delta_usr=\(.tracer_data.user_percent_delta) delta_sys=\(.tracer_data.system_percent_delta) delta_total=\(.tracer_data.total_percent_delta) samples=\(.tracer_data.flamedata[0].value)"' \
		"${HUATUO_BAMAI_TEST_TMPDIR}/${suffix}.events.json"
}

cpuidle_capture() {
	local suffix=$1 mode=$2 milli=$3 channel=$4 before expected
	before=$(cpuidle_event_count "${suffix}")
	cpuidle_command "${suffix}" "${mode}" "${milli}"
	cpuidle_reference "${suffix}" "${suffix}-high" 4
	expected=$((before + 1))
	wait_until 35 0.5 cpuidle_ready_events "${suffix}" "${expected}" \
		|| fatal "${cpuidle_phase}: ${channel} event count for container $(< "${CPUIDLE_DIR}/${suffix}.id"): expected>=${expected}, actual=$(cpuidle_event_count "${suffix}"); inspect ${HUATUO_BAMAI_TEST_TMPDIR}/events/cpuidle and ${HUATUO_BAMAI_TEST_TMPDIR}/${suffix}-high.reference.jsonl"
	assert_cpuidle_events "${suffix}" "${channel}"
	cpuidle_status
}

assert_cpuidle_cooldown() {
	cpuidle_local_events | jq -e --arg id "$(< "${CPUIDLE_DIR}/a.id")" --argjson cooldown "${cpuidle_cooldown}" '
		[.[] | select(.container_id == $id) | .started_timestamp | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601] | sort |
		length == 2 and .[1] - .[0] >= $cooldown
	' > /dev/null || fatal "cpuidle event timestamps violate per-container cooldown"
}

cpuidle_cooldown_elapsed() {
	cpuidle_local_events | jq -e --arg id "$(< "${CPUIDLE_DIR}/a.id")" --argjson cooldown "${cpuidle_cooldown}" '
		[.[] | select(.container_id == $id) | .started_timestamp |
		 sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601] | max as $started |
		$started != null and now >= $started + $cooldown + 1
	' > /dev/null
}

assert_cpuidle_invalid_config() (
	local name=$1 expected=$2 status=0
	HUATUO_BAMAI_TEST_TMPDIR="${CPUIDLE_DIR}/invalid-${name}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	case ${name} in
	interval) cpuidle_interval=0 ;;
	cooldown) cpuidle_cooldown=0 ;;
	duration) cpuidle_duration=0 ;;
	negative) cpuidle_thresholds[0]=-1 ;;
	percentage) cpuidle_thresholds[5]=101 ;;
	regex) cpuidle_include='[' ;;
	esac
	write_config_cpuidle_autotracing > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	timeout 15 "${HUATUO_BAMAI_BIN}" --region e2e --disable-kubelet --config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" \
		--config bamai.conf > "${HUATUO_BAMAI_TEST_TMPDIR}/expected-error.log" 2>&1 || status=$?
	((status != 0 && status != 124 && status != 137)) || fatal "${name}: invalid cpuidle config did not fail promptly"
	grep -Fq "${expected}" "${HUATUO_BAMAI_TEST_TMPDIR}/expected-error.log" \
		|| fatal "${name}: missing expected configuration error: ${expected}"
	log_info "cpuidle: rejected invalid ${name}"
)

assert_cpuidle_invalid_config interval 'sampling interval: timer duration must be positive'
assert_cpuidle_invalid_config cooldown 'minimum trace interval: timer duration must be positive'
assert_cpuidle_invalid_config duration 'profile duration must be positive'
assert_cpuidle_invalid_config negative 'user threshold: cpu percentage must be between 0 and 100'
assert_cpuidle_invalid_config percentage 'total delta threshold: cpu percentage must be between 0 and 100'
assert_cpuidle_invalid_config regex 'build container filter'

storage_start elasticsearch_v8
storage_request "${CPUIDLE_DIR}/index-created.json" -X PUT "${STORAGE_ADDR}/${CPUIDLE_INDEX}" -H 'Content-Type: application/json' \
	--data '{"settings":{"number_of_shards":1,"number_of_replicas":0}}'
assert_storage_index_created "${CPUIDLE_DIR}/index-created.json"

kubectl --request-timeout=20s create namespace "${CPUIDLE_NS}"
cpuidle_create_pod a 500m
cpuidle_create_pod b 1000m

# Leave margin for user-time accounting differences and split sampling windows.
cpuidle_thresholds=(30 100 100 7 100 100)
cpuidle_include="^${CPUIDLE_PREFIX}-a$"
cpuidle_begin user
cpuidle_command b user 600
cpuidle_capture a user 300 user
assert_eq "$(cpuidle_event_count b)" 0 "included filter allowed unrelated Pod"
cpuidle_finish

# getpid splits time between user and kernel; leave room for a split sampling step.
cpuidle_thresholds=(100 10 100 100 3 100)
cpuidle_include="^${CPUIDLE_PREFIX}-b$"
cpuidle_begin system
cpuidle_capture b system 600 system
cpuidle_finish

# Leave margin when the 60% step spans two sampling windows and is truncated.
cpuidle_thresholds=(100 100 40 100 100 10)
cpuidle_begin total
cpuidle_capture b mixed 600 total
cpuidle_finish

cpuidle_thresholds=(100 100 90 100 100 10)
cpuidle_include="^${CPUIDLE_PREFIX}-a$"
cpuidle_begin below-threshold
cpuidle_command a user 300
cpuidle_reference a high 8
assert_cpuidle_no_events
cpuidle_finish

# Starting at high utilization must establish a baseline, not invent a spike.
cpuidle_thresholds=(100 100 20 100 100 20)
cpuidle_phase=steady-high
HUATUO_BAMAI_TEST_TMPDIR="${CPUIDLE_DIR}/${cpuidle_phase}"
mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
cpuidle_command a user 300
cpuidle_reference a steady-ready-1 2
cpuidle_reference a steady-ready-2 2
cpuidle_start
cpuidle_target a 500
cpuidle_reference a steady 8
assert_cpuidle_no_events
cpuidle_finish

cpuidle_thresholds=(100 100 20 100 100 15)
cpuidle_include="^${CPUIDLE_PREFIX}-(a|b)$"
cpuidle_begin cooldown
cpuidle_capture a user 300 total
cpuidle_command a idle 0
cpuidle_reference a cooldown-idle 4
cpuidle_command a user 300
cpuidle_reference a cooldown-high 4
assert_eq "$(cpuidle_event_count a)" 1 "same container triggered inside cooldown"
cpuidle_capture b user 600 total
cpuidle_command a idle 0
cpuidle_reference a recovery-idle 8
wait_until "$((cpuidle_cooldown + cpuidle_interval))" 0.5 cpuidle_cooldown_elapsed \
	|| fatal "first cpuidle event did not reach its cooldown deadline"
cpuidle_capture a user 300 total
assert_cpuidle_cooldown
cpuidle_finish

cpuidle_include_field=container_host_namespace
cpuidle_include="^${CPUIDLE_NS}$"
cpuidle_exclude="^${CPUIDLE_PREFIX}-a$"
cpuidle_begin exclude-priority
cpuidle_command a user 300
cpuidle_reference a excluded 4
assert_cpuidle_no_events
cpuidle_capture b user 600 total
assert_eq "$(cpuidle_event_count a)" 0 "excluded Pod overrode exclusion with an include match"
cpuidle_finish

cpuidle_exclude_field=container_qos
cpuidle_exclude='^burstable$'
cpuidle_begin excluded-qos
cpuidle_command a user 300
cpuidle_reference a excluded 8
assert_cpuidle_no_events
cpuidle_finish

cpuidle_exclude_field=container_hostname
cpuidle_exclude='^$'
cpuidle_disabled=true
cpuidle_begin disabled
cpuidle_command a user 300
cpuidle_reference a disabled 8
assert_cpuidle_no_events
cpuidle_finish
cpuidle_disabled=false

# Normal restart and SIGKILL recovery both require a new low-utilization baseline.
cpuidle_include_field=container_hostname
cpuidle_include="^${CPUIDLE_PREFIX}-a$"
cpuidle_begin restart
cpuidle_capture a user 300 total
cpu_metrics_query_batch "${HUATUO_BAMAI_TEST_TMPDIR}" 3 3 assert_cpuidle_query \
	|| fatal "concurrent cpuidle metrics queries failed; inspect query files"
cpuidle_finish KILL
cpuidle_begin kill-recovery
cpuidle_capture a user 300 total
cpuidle_finish

cpuidle_begin pod-lifecycle
cpuidle_capture a user 300 total
cpuidle_old_id=$(< "${CPUIDLE_DIR}/a.id")
kubectl --request-timeout=30s delete pod -n "${CPUIDLE_NS}" "${CPUIDLE_PREFIX}-a" --timeout=30s
assert_huatuo_bamai_containers_absent 'old cpuidle container remains cached' "${cpuidle_old_id}"
rm -f "${CPUIDLE_DIR}/a.id"
cpuidle_create_pod a 500m
cpuidle_target a 500
assert_cpuidle_recreated "${cpuidle_old_id}"
cpuidle_reference a recreated-baseline 8
assert_eq "$(cpuidle_event_count a)" 0 "new container triggered before rebuilding a low-utilization baseline"
cpuidle_capture a user 300 total
cpuidle_finish

log_info "cpuidle E2E passed; artifacts: ${CPUIDLE_DIR}"
