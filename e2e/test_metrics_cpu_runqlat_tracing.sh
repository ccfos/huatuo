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
source "${ROOT_DIR}/e2e/lib.sh"

require_commands kubectl jq findmnt ss taskset
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
kprobe_available free_fair_sched_group || skip "free_fair_sched_group kprobe is unavailable"
for event in sched_wakeup sched_wakeup_new sched_switch sched_process_exit; do
	tracepoint_available sched "${event}" || skip "sched/${event} tracepoint is unavailable"
done

readonly RUNQLAT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/runqlat"
readonly RUNQLAT_BIN="${RUNQLAT_DIR}/runqlat"
readonly RUNQLAT_LOAD="${RUNQLAT_DIR}/cpu-load"
readonly RUNQLAT_NS="${BUSINESS_POD_NS}"
readonly RUNQLAT_PREFIX="runqlat-${BASHPID}-${RANDOM}"
readonly RUNQLAT_LABEL="app=${RUNQLAT_PREFIX}"
RUNQLAT_DISABLED=false
runqlat_generation=0
runqlat_daemon_generation=0
runqlat_pods=()
runqlat_paused=()
runqlat_host_pid=""
mkdir -p "${RUNQLAT_DIR}"
chmod 755 "${RUNQLAT_DIR}"
uname -a > "${RUNQLAT_DIR}/kernel.txt"
"${HUATUO_BAMAI_BIN}" --version > "${RUNQLAT_DIR}/bamai-version.txt"

runqlat_resume() {
	local pid
	for pid in "${runqlat_paused[@]}"; do
		kill -CONT "${pid}" || return 1
	done
	runqlat_paused=()
}

runqlat_cleanup() {
	local status=$?
	trap - EXIT
	runqlat_resume || status=1
	stop_and_wait_by_pid "${runqlat_host_pid}" || status=1
	if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
		kubectl --request-timeout=10s describe pod -n "${RUNQLAT_NS}" -l "${RUNQLAT_LABEL}" \
			> "${RUNQLAT_DIR}/pods-failure.txt" 2>&1 || true
	fi
	kubectl --request-timeout=30s delete pod -n "${RUNQLAT_NS}" -l "${RUNQLAT_LABEL}" \
		--ignore-not-found --timeout=30s || status=1
	huatuo_bamai_stop || status=1
	exit "${status}"
}
trap runqlat_cleanup EXIT

if [[ -n ${RUNQLAT_HELPER_BIN:-} ]]; then
	require_commands "${RUNQLAT_HELPER_BIN}"
	cp "${RUNQLAT_HELPER_BIN}" "${RUNQLAT_BIN}"
else
	require_commands go
	CGO_ENABLED=0 go test -mod=vendor "${ROOT_DIR}/e2e/testdata/runqlat"
	CGO_ENABLED=0 go build -mod=vendor -o "${RUNQLAT_BIN}" "${ROOT_DIR}/e2e/testdata/runqlat"
fi
if [[ -n ${CPU_UTIL_HELPER_BIN:-} ]]; then
	require_commands "${CPU_UTIL_HELPER_BIN}"
	cp "${CPU_UTIL_HELPER_BIN}" "${RUNQLAT_LOAD}"
else
	require_commands go
	CGO_ENABLED=0 go build -mod=vendor -o "${RUNQLAT_LOAD}" "${ROOT_DIR}/e2e/testdata/cpu_util"
fi

RUNQLAT_NODE=$(kubelet_pods_json | jq -er '[.items[].spec.nodeName] | unique | if length == 1 then .[0] else error("cannot identify local node") end')
mapfile -t runqlat_cpus < <(awk '/Cpus_allowed_list/ {
	n=split($2, ranges, ",")
	for (i=1; i<=n; i++) {
		m=split(ranges[i], ends, "-"); last=(m==2 ? ends[2] : ends[1])
		for (cpu=ends[1]; cpu<=last; cpu++) print cpu
	}
}' /proc/self/status)
readonly RUNQLAT_CPU=${runqlat_cpus[-1]}
RUNQLAT_CPU_ROOT=$(findmnt -rn -t cgroup -O cpu -o TARGET || true)
RUNQLAT_V2=false
if [[ -n ${RUNQLAT_CPU_ROOT} ]]; then
	RUNQLAT_CPUSET_ROOT=$(findmnt -rn -t cgroup -O cpuset -o TARGET)
else
	RUNQLAT_V2=true
	RUNQLAT_CPU_ROOT=$(findmnt -rn -t cgroup2 -o TARGET) || skip "CPU cgroup controller is not mounted"
	RUNQLAT_CPUSET_ROOT=${RUNQLAT_CPU_ROOT}
fi
RUNQLAT_PORT=$(allocate_available_port) || fatal "cannot allocate runqlat metrics port"
RUNQLAT_KUBELET_PORT=${KUBELET_PODS_API##*:}
RUNQLAT_KUBELET_PORT=${RUNQLAT_KUBELET_PORT%%/*}
readonly RUNQLAT_NODE RUNQLAT_CPU_ROOT RUNQLAT_CPUSET_ROOT RUNQLAT_V2 RUNQLAT_PORT RUNQLAT_KUBELET_PORT
log_info "runqlat: cgroup_v2=${RUNQLAT_V2} workload_cpu=${RUNQLAT_CPU}"

if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log ]]; then
	huatuo_bamai_stop
	huatuo_bamai_log_check || fatal "runner baseline daemon has unexpected errors"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${RUNQLAT_DIR}/baseline.log"
fi
HUATUO_BAMAI_ADDR="http://127.0.0.1:${RUNQLAT_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

runqlat_restart() {
	local signal=${1:-TERM} pid
	if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]]; then
		pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
		if [[ -f ${RUNQLAT_DIR}/objects-${runqlat_daemon_generation}.json ]]; then
			"${RUNQLAT_BIN}" inspect --pid "${pid}" --output "${RUNQLAT_DIR}/objects-${runqlat_daemon_generation}.json"
		fi
		if [[ ${signal} == KILL ]]; then
			kill -KILL "${pid}"
			if wait "${pid}"; then
				fatal "SIGKILL did not terminate the owned daemon"
			else
				[[ $? -eq 137 ]] || fatal "owned daemon did not exit from SIGKILL"
			fi
		fi
		huatuo_bamai_stop
		runqlat_log_check
		cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${RUNQLAT_DIR}/daemon-${runqlat_daemon_generation}.log"
		if [[ -f ${RUNQLAT_DIR}/objects-${runqlat_daemon_generation}.json ]]; then
			wait_until 15 1 "${RUNQLAT_BIN}" released --before "${RUNQLAT_DIR}/objects-${runqlat_daemon_generation}.json" \
				|| fatal "stopped runqlat daemon leaked BPF objects"
		fi
	fi
	runqlat_daemon_generation=$((runqlat_daemon_generation + 1))
	integration_huatuo_bamai_start write_runqlat_config --region e2e --disable-storage
	if [[ ${RUNQLAT_DISABLED} != true ]]; then
		wait_until 20 1 "${RUNQLAT_BIN}" inspect --pid "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")" \
			--output "${RUNQLAT_DIR}/objects-${runqlat_daemon_generation}.json" \
			|| fatal "runqlat BPF programs/maps did not become ready"
	fi
}

runqlat_log_check() {
	huatuo_bamai_log_check || fatal "runqlat daemon has unexpected errors"
	if grep -q 'runqlat: update container cache' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"; then
		fatal "runqlat failed to refresh container counters; inspect huatuo.log"
	fi
}

runqlat_ready() {
	kubectl --request-timeout=5s logs -n "${RUNQLAT_NS}" "$1" -c worker --tail=5 | grep -Fxq "ready $2"
}

runqlat_command() {
	local name=$1 mode=$2 milli=$3 workers=${4:-1}
	runqlat_generation=$((runqlat_generation + 1))
	printf '%s %s %s %s\n' "${mode}" "${milli}" "${workers}" "${runqlat_generation}" > "${RUNQLAT_DIR}/${name}.command.next"
	mv "${RUNQLAT_DIR}/${name}.command.next" "${RUNQLAT_DIR}/${name}.command"
}

runqlat_create() {
	local name=$1 id cpuset
	runqlat_command "${name}" idle 0
	jq -n --arg name "${name}" --arg ns "${RUNQLAT_NS}" --arg app "${RUNQLAT_PREFIX}" \
		--arg node "${RUNQLAT_NODE}" --arg image "${BUSINESS_POD_IMAGE}" --arg dir "${RUNQLAT_DIR}" '
		{apiVersion: "v1", kind: "Pod", metadata: {name: $name, namespace: $ns, labels: {app: $app}},
		 spec: {nodeName: $node, terminationGracePeriodSeconds: 2,
		   containers: [{name: "worker", image: $image, imagePullPolicy: "Never",
		     command: ["/work/cpu-load", "load", "--control", ("/work/" + $name + ".command")],
		     resources: {requests: {cpu: "100m", memory: "16Mi"}, limits: {cpu: "2", memory: "64Mi"}},
		     volumeMounts: [{name: "work", mountPath: "/work", readOnly: true}]}],
		   volumes: [{name: "work", hostPath: {path: $dir, type: "Directory"}}]}}
	' > "${RUNQLAT_DIR}/${name}.json"
	kubectl --request-timeout=20s create -f "${RUNQLAT_DIR}/${name}.json"
	runqlat_pods+=("${name}")
	kubectl wait --for=condition=Ready pod -n "${RUNQLAT_NS}" "${name}" --timeout=90s
	assert_huatuo_bamai_containers_present "${RUNQLAT_NS}" "^${name}$" 1
	id=$(kubelet_container_ids "${RUNQLAT_NS}" "^${name}$")
	printf '%s\n' "${id}" > "${RUNQLAT_DIR}/${name}.id"
	cgroup_find_container "${RUNQLAT_CPU_ROOT}" "${id}" > "${RUNQLAT_DIR}/${name}.cgroup"
	cpuset=$(cgroup_find_container "${RUNQLAT_CPUSET_ROOT}" "${id}")
	echo "${RUNQLAT_CPU}" > "${cpuset}/cpuset.cpus"
	printf '%s\n' "${cpuset}" > "${RUNQLAT_DIR}/${name}.cpuset"
	wait_until 10 1 runqlat_ready "${name}" "${runqlat_generation}" || fatal "Pod workload did not initialize"
}

runqlat_target() {
	local name=$1 css cpu tids=tasks
	css=$(curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_ADDR}/v1/containers/$(< "${RUNQLAT_DIR}/${name}.id")" | jq -er '.data.cgroup_css.cpu')
	cpu=$(< "${RUNQLAT_DIR}/${name}.cgroup")
	[[ ${RUNQLAT_V2} != true ]] || tids=cgroup.threads
	jq -n --arg pod "${name}" --arg ns "${RUNQLAT_NS}" --arg css "${css}" --rawfile tids "${cpu}/${tids}" \
		'{Pod: $pod, Container: "worker", Namespace: $ns, CSS: $css,
		  TIDs: ($tids | split("\n") | map(select(length > 0) | tonumber))}'
}

runqlat_metadata_ready() {
	curl -sf "${CURL_TIMEOUT[@]}" "${HUATUO_BAMAI_ADDR}/v1/containers/$(< "${RUNQLAT_DIR}/$1.id")" \
		| jq -e '.data.cgroup_css.cpu | strings | test("^0x[0-9a-f]{16}$")' > /dev/null
}

runqlat_targets() {
	local name
	for name in "${runqlat_pods[@]}"; do
		wait_until 20 1 runqlat_metadata_ready "${name}" >&2 || fatal "Pod ${name} CPU CSS did not become available"
		runqlat_target "${name}"
	done | jq -s '.' > "${RUNQLAT_DIR}/targets.json"
}

runqlat_pause() {
	local name pid
	for name in "${runqlat_pods[@]}"; do
		while read -r pid; do
			[[ -n ${pid} ]] || continue
			kill -STOP "${pid}"
			runqlat_paused+=("${pid}")
		done < "$(< "${RUNQLAT_DIR}/${name}.cgroup")/cgroup.procs"
	done
	# SIGSTOP is asynchronous; wait for the last switch-out to finish.
	sleep 0.2
}

runqlat_check() {
	local phase=$1
	runqlat_targets
	runqlat_pause
	wait_until 5 1 "${RUNQLAT_BIN}" check --pid "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")" \
		--url "${HUATUO_BAMAI_METRICS_API}" --targets "${RUNQLAT_DIR}/targets.json" --stable \
		--output "${RUNQLAT_DIR}/${phase}" || fatal "${phase}: map and metric values differ"
	runqlat_resume
}

runqlat_phase() {
	local phase=$1 name=$2 zone=$3 switches=${4:-} cpus=${5:-1} pid
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	wait_until 20 1 runqlat_metadata_ready "${name}" || fatal "Pod ${name} CPU CSS did not become available"
	runqlat_target "${name}" | jq -s '.' > "${RUNQLAT_DIR}/phase-target.json"
	log_info "runqlat phase: ${phase} (10 seconds)"
	"${RUNQLAT_BIN}" snapshot --pid "${pid}" --targets "${RUNQLAT_DIR}/phase-target.json" --output "${RUNQLAT_DIR}/${phase}-before.json"
	"${RUNQLAT_BIN}" check --pid "${pid}" --url "${HUATUO_BAMAI_METRICS_API}" --targets "${RUNQLAT_DIR}/phase-target.json" \
		--output "${RUNQLAT_DIR}/${phase}-active"
	sleep 10
	"${RUNQLAT_BIN}" snapshot --pid "${pid}" --targets "${RUNQLAT_DIR}/phase-target.json" --output "${RUNQLAT_DIR}/${phase}-after.json"
	"${RUNQLAT_BIN}" growth --before "${RUNQLAT_DIR}/${phase}-before.json" --after "${RUNQLAT_DIR}/${phase}-after.json" \
		--targets "${RUNQLAT_DIR}/phase-target.json" --zone "${zone}" --switches "${switches}" --cpus "${cpus}" | tee "${RUNQLAT_DIR}/${phase}-growth.log"
	runqlat_check "${phase}-settled"
}

runqlat_restart
readonly a="${RUNQLAT_PREFIX}-a" b="${RUNQLAT_PREFIX}-b"
runqlat_create "${a}"
runqlat_create "${b}"
runqlat_phase wakeup "${a}" 0 voluntary

runqlat_command "${a}" user 0 2
wait_until 10 1 runqlat_ready "${a}" "${runqlat_generation}" || fatal "busy workers did not initialize"
runqlat_phase preemption "${a}" -1 involuntary

cpu=$(< "${RUNQLAT_DIR}/${a}.cgroup")
# A small bandwidth slice makes long runnable waits without RT scheduling.
for zone in 1 2 3; do
	case ${zone} in
	1) period=20000 ;;
	2) period=40000 ;;
	3) period=85000 ;;
	esac
	if [[ ${RUNQLAT_V2} == true ]]; then
		echo "5000 ${period}" > "${cpu}/cpu.max"
	else
		# Lower quota first, so the intermediate ratio fits the Pod parent.
		echo 5000 > "${cpu}/cpu.cfs_quota_us"
		echo "${period}" > "${cpu}/cpu.cfs_period_us"
	fi
	log_info "runqlat quota workload: Pod=${a} quota_us=5000 period_us=${period} CPU=${RUNQLAT_CPU}"
	throttled_before=$(awk '$1=="nr_throttled" {print $2}' "${cpu}/cpu.stat")
	runqlat_phase "zone-${zone}" "${a}" "${zone}" involuntary
	throttled_after=$(awk '$1=="nr_throttled" {print $2}' "${cpu}/cpu.stat")
	[[ ${throttled_after} -gt ${throttled_before} ]] || fatal "zone-${zone}: quota workload did not throttle"
	if [[ ${RUNQLAT_V2} == true ]]; then
		[[ $(< "${cpu}/cpu.max") == "5000 ${period}" ]] || fatal "test Pod quota was overwritten"
	else
		[[ $(< "${cpu}/cpu.cfs_quota_us") == 5000 && $(< "${cpu}/cpu.cfs_period_us") == "${period}" ]] \
			|| fatal "test Pod quota/period was overwritten"
	fi
	log_info "zone-${zone}: nr_throttled delta=$((throttled_after - throttled_before))"
done
if [[ ${RUNQLAT_V2} == true ]]; then
	echo "200000 100000" > "${cpu}/cpu.max"
else
	echo 100000 > "${cpu}/cpu.cfs_period_us"
	echo 200000 > "${cpu}/cpu.cfs_quota_us"
fi

if ((${#runqlat_cpus[@]} >= 3)); then
	second=${runqlat_cpus[-2]}
	echo "${second},${RUNQLAT_CPU}" > "$(< "${RUNQLAT_DIR}/${a}.cpuset")/cpuset.cpus"
	runqlat_phase multi-cpu "${a}" 0 "" 2
else
	log_warn "SKIP subcase: multi-CPU load needs at least three allowed CPUs to leave one free"
fi

runqlat_targets
"${RUNQLAT_BIN}" concurrent --pid "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")" \
	--url "${HUATUO_BAMAI_METRICS_API}" --targets "${RUNQLAT_DIR}/targets.json" | tee "${RUNQLAT_DIR}/concurrent.log"
runqlat_check after-concurrent

runqlat_command host user 1000 1
taskset -c "${RUNQLAT_CPU}" "${RUNQLAT_LOAD}" load --control "${RUNQLAT_DIR}/host.command" > "${RUNQLAT_DIR}/host-worker.log" 2>&1 &
runqlat_host_pid=$!
runqlat_phase host-contention "${a}" -1 involuntary
stop_and_wait_by_pid "${runqlat_host_pid}"
runqlat_host_pid=""

runqlat_restart TERM
runqlat_check after-term
runqlat_restart KILL
runqlat_check after-kill

runqlat_target "${a}" | jq -s '.' > "${RUNQLAT_DIR}/deleted-target.json"
old_id=$(< "${RUNQLAT_DIR}/${a}.id")
kubectl delete pod -n "${RUNQLAT_NS}" "${a}" --timeout=30s
runqlat_pods=("${b}")
assert_huatuo_bamai_containers_absent "deleted runqlat Pod" "${old_id}"
wait_until 60 2 "${RUNQLAT_BIN}" deleted --pid "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")" \
	--url "${HUATUO_BAMAI_METRICS_API}" --targets "${RUNQLAT_DIR}/deleted-target.json" \
	|| fatal "deleted container metrics or BPF records were not cleaned"
runqlat_create "${a}"
runqlat_phase recreated "${a}" 0 voluntary

RUNQLAT_DISABLED=true
runqlat_restart
"${RUNQLAT_BIN}" disabled --pid "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")" --url "${HUATUO_BAMAI_METRICS_API}"
RUNQLAT_DISABLED=false
runqlat_restart
runqlat_phase reenabled "${a}" 0 voluntary
runqlat_log_check
log_info "runqlat E2E passed; artifacts: ${RUNQLAT_DIR}"
