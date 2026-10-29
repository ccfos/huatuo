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
source "${ROOT_DIR}/e2e/lib_cpu_load.sh"
source "${ROOT_DIR}/integration/lib_cpu_metrics.sh"

runqlat_cleanup() {
	local status=$?
	trap - EXIT
	cpu_metrics_stop_queries
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

runqlat_prepare() {
	require_commands kubectl jq findmnt ss taskset
	require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
	kprobe_available free_fair_sched_group || skip "free_fair_sched_group kprobe is unavailable"
	for event in sched_wakeup sched_wakeup_new sched_switch sched_process_exit; do
		tracepoint_available sched "${event}" || skip "sched/${event} tracepoint is unavailable"
	done

	RUNQLAT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/runqlat"
	RUNQLAT_LOAD="${RUNQLAT_DIR}/cpu-load"
	RUNQLAT_NS="${BUSINESS_POD_NS}"
	RUNQLAT_PREFIX="runqlat-${BASHPID}-${RANDOM}"
	RUNQLAT_LABEL="app=${RUNQLAT_PREFIX}"
	runqlat_disabled=false
	runqlat_generation=0
	runqlat_daemon_generation=0
	runqlat_pods=()
	runqlat_host_pid=""
	trap runqlat_cleanup EXIT
	trap 'exit 143' TERM
	trap 'exit 130' INT
	mkdir -p "${RUNQLAT_DIR}"
	chmod 755 "${RUNQLAT_DIR}"
	uname -a > "${RUNQLAT_DIR}/kernel.txt"
	"${HUATUO_BAMAI_BIN}" --version > "${RUNQLAT_DIR}/bamai-version.txt"

	cpu_load_prepare "${RUNQLAT_DIR}"
	cpu_load_prepare_host "${RUNQLAT_DIR}"
	cpu_metrics_prepare

	RUNQLAT_NODE=$(kubelet_pods_json | jq -er '[.items[].spec.nodeName] | unique | if length == 1 then .[0] else error("cannot identify local node") end')
	mapfile -t runqlat_cpus < <(awk '/Cpus_allowed_list/ {
	n=split($2, ranges, ",")
	for (i=1; i<=n; i++) {
		m=split(ranges[i], ends, "-"); last=(m==2 ? ends[2] : ends[1])
		for (cpu=ends[1]; cpu<=last; cpu++) print cpu
	}
}' /proc/self/status)
	RUNQLAT_CPU=${runqlat_cpus[-1]}
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
	readonly RUNQLAT_DIR RUNQLAT_LOAD RUNQLAT_NS RUNQLAT_PREFIX RUNQLAT_LABEL
	readonly RUNQLAT_NODE RUNQLAT_CPU RUNQLAT_CPU_ROOT RUNQLAT_CPUSET_ROOT RUNQLAT_V2
	readonly RUNQLAT_PORT RUNQLAT_KUBELET_PORT
	log_info "runqlat: cgroup_v2=${RUNQLAT_V2} workload_cpu=${RUNQLAT_CPU}"

	if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log ]]; then
		huatuo_bamai_stop
		assert_huatuo_bamai_log_clean || fatal "runner baseline daemon has unexpected errors"
		cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${RUNQLAT_DIR}/baseline.log"
	fi
	HUATUO_BAMAI_ADDR="http://127.0.0.1:${RUNQLAT_PORT}"
	HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

}

runqlat_restart() {
	local signal=${1:-TERM}
	if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]]; then
		assert_huatuo_bamai_shutdown_with_signal "${signal}"
		assert_runqlat_logs
		cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${RUNQLAT_DIR}/daemon-${runqlat_daemon_generation}.log"
	fi
	runqlat_daemon_generation=$((runqlat_daemon_generation + 1))
	write_config_runqlat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	huatuo_bamai_start --config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" --config bamai.conf \
		--region e2e --disable-storage
}

assert_runqlat_logs() {
	assert_huatuo_bamai_log_clean || fatal "runqlat daemon has unexpected errors"
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
		--arg node "${RUNQLAT_NODE}" --arg image "${CPU_LOAD_IMAGE}" --arg dir "${RUNQLAT_DIR}" '
		{apiVersion: "v1", kind: "Pod", metadata: {name: $name, namespace: $ns, labels: {app: $app}},
		 spec: {nodeName: $node, terminationGracePeriodSeconds: 2,
		   containers: [{name: "worker", image: $image, imagePullPolicy: "Never",
		     command: ["/work/cpu-load", "load", "--control", ("/work/" + $name + ".command")],
		     resources: {requests: {cpu: "100m", memory: "16Mi"}, limits: {cpu: "1", memory: "512Mi"}},
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
	printf '%s\n' "${cpuset}" > "${RUNQLAT_DIR}/${name}.cpuset"
	runqlat_pin "${name}" "${RUNQLAT_CPU}"
	wait_until 10 1 runqlat_ready "${name}" "${runqlat_generation}" || fatal "Pod workload did not initialize"
}

runqlat_pin() {
	local name=$1 cpus=$2 path
	path=$(< "${RUNQLAT_DIR}/${name}.cpuset")
	echo "${cpus}" > "${path}/cpuset.cpus"
	# Save the kernel's canonical list, which may render adjacent CPUs as a range.
	cp "${path}/cpuset.cpus" "${RUNQLAT_DIR}/${name}.pinned-cpus"
	assert_runqlat_cpuset "${name}"
}

assert_runqlat_cpuset() {
	local name=$1 path file=cpuset.cpus actual expected
	path=$(< "${RUNQLAT_DIR}/${name}.cpuset")
	[[ ${RUNQLAT_V2} != true ]] || file=cpuset.cpus.effective
	actual=$(< "${path}/${file}")
	expected=$(< "${RUNQLAT_DIR}/${name}.pinned-cpus")
	[[ ${actual} == "${expected}" ]] || fatal "Pod ${name} cpuset changed: expected ${expected}, actual ${actual}"
}

runqlat_target() {
	jq -n --arg pod "$1" --arg ns "${RUNQLAT_NS}" '{Pod: $pod, Container: "worker", Namespace: $ns}'
}

runqlat_targets() {
	local name
	for name in "${runqlat_pods[@]}"; do runqlat_target "${name}"; done | jq -s '.'
}

runqlat_snapshot() {
	local output=$1
	cpu_metrics_read "${output}.metrics.json" || return 1
	assert_runqlat_metrics "${output}.metrics.json" > "${output}.json"
}

assert_runqlat_metrics() {
	local file=$1
	jq -e --arg host "$(hostname)" --argjson targets "$(runqlat_targets)" '
  def zones($family; $labels):
   $family as $f | [$f.samples[]? | select(.labels | contains($labels))] as $rows |
   if $f.type == "COUNTER" and ($f.help | length > 0)
    and ($rows | length) == 4 and ([$rows[].labels.zone] | sort) == ["0","1","2","3"]
    and all($rows[]; .labels == ($labels + {zone: .labels.zone})
     and (.value | type == "number" and . >= 0 and . <= 9007199254740992 and floor == .))
   then $rows | sort_by(.labels.zone) | map(.value)
   else error("missing or invalid runqlat counters for " + ($labels | tostring)) end;
  . as $metrics |
  [.huatuo_bamai_scrape_collector_success.samples[]? | select(.labels.collector == "runqlat")] as $success |
  if ($success | length) == 1 and $success[0].value == 1
   and (.huatuo_bamai_tracing_status_running.samples | length) == 1
   and .huatuo_bamai_tracing_status_running.samples[0].value == 1
   and ([.huatuo_bamai_tracing_status_hitcount.samples[]? | select(.labels.tracing == "runqlat")] |
    length == 1 and .[0].value == 0)
  then {Host: zones(.huatuo_bamai_runqlat_latency; {host: $host, region: "e2e"}),
   Containers: (reduce $targets[] as $t ({}; .[$t.Pod] =
    ($metrics | zones(.huatuo_bamai_runqlat_container_latency;
     {host: $host, region: "e2e", container_host: $t.Pod, container_name: $t.Container,
      container_hostnamespace: $t.Namespace, container_type: "normal", container_level: "burstable"}))))}
  else error("runqlat collector or tracing status is unhealthy") end
 ' "${file}"
}

runqlat_sample() {
	local phase=$1 name=$2
	assert_runqlat_cpuset "${name}"
	runqlat_sample_index=$((runqlat_sample_index + 1))
	runqlat_snapshot "${RUNQLAT_DIR}/${phase}-sample-${runqlat_sample_index}"
}

runqlat_phase() {
	local phase=$1 name=$2 zone=${3:--1}
	assert_runqlat_cpuset "${name}"
	log_info "runqlat phase: ${phase} (10-second measurement window)"
	wait_until 15 1 runqlat_snapshot "${RUNQLAT_DIR}/${phase}-before" || fatal "runqlat metrics did not become ready"
	runqlat_sample_index=0
	cpu_metrics_observe 10 1 runqlat_sample "${phase}" "${name}" \
		|| fatal "${phase}: invalid runqlat metrics during measurement"
	cp "${RUNQLAT_DIR}/${phase}-sample-${runqlat_sample_index}.json" "${RUNQLAT_DIR}/${phase}-after.json"
	assert_runqlat_growth "${phase}" "${name}" "${zone}"
}

assert_runqlat_growth() {
	local phase=$1 name=$2 zone=$3
	local file="${RUNQLAT_DIR}/${phase}-growth.json" expected="host/container deltas >=0 and totals >0"
	if ((zone >= 0)); then expected+=", zone-${zone} deltas >0"; fi
	jq -en --slurpfile before "${RUNQLAT_DIR}/${phase}-before.json" --slurpfile after "${RUNQLAT_DIR}/${phase}-after.json" \
		--arg pod "${name}" '
  def delta($a; $b): [range(0; 4) as $i | $a[$i] - $b[$i]];
  {host: delta($after[0].Host; $before[0].Host),
   container: delta($after[0].Containers[$pod]; $before[0].Containers[$pod])}
 ' > "${file}"
	jq -e --argjson zone "${zone}" '
  all(.[]; all(.[]; . >= 0) and add > 0 and (if $zone >= 0 then .[$zone] > 0 else true end))
 ' "${file}" > /dev/null \
		|| fatal "${phase}: expected ${expected}; actual=$(jq -c . "${file}"); inspect ${RUNQLAT_DIR}/${phase}-{before,after}.json and ${file}"
	cat "${file}"
}

assert_runqlat_zones() {
	local zone
	for zone in 1 2 3; do
		[[ ${runqlat_zones_seen[zone]} == true ]] \
			|| fatal "runqlat contention: expected zone-${zone} growth in both host and container, actual zones_seen=[${runqlat_zones_seen[*]}]; inspect ${RUNQLAT_DIR}/workers-*-growth.json"
	done
}

runqlat_absent() {
	local pod=$1
	cpu_metrics_read "${RUNQLAT_DIR}/absent.metrics.json" || return 1
	jq -e --arg pod "${pod}" '
  [.huatuo_bamai_runqlat_container_latency.samples[]? | select(.labels.container_host == $pod)] | length == 0
 ' "${RUNQLAT_DIR}/absent.metrics.json" > /dev/null
}

runqlat_prepare
runqlat_restart
readonly RUNQLAT_POD_A="${RUNQLAT_PREFIX}-a" RUNQLAT_POD_B="${RUNQLAT_PREFIX}-b"
runqlat_create "${RUNQLAT_POD_A}"
runqlat_create "${RUNQLAT_POD_B}"
runqlat_phase wakeup "${RUNQLAT_POD_A}" 0
runqlat_command "${RUNQLAT_POD_A}" user 0 2
wait_until 10 1 runqlat_ready "${RUNQLAT_POD_A}" "${runqlat_generation}" || fatal "busy workers did not initialize"
runqlat_phase preemption "${RUNQLAT_POD_A}"

runqlat_zones_seen=(false false false false)
for runqlat_workers in 4 8 16 32 64 128; do
	runqlat_command "${RUNQLAT_POD_A}" user 0 "${runqlat_workers}"
	wait_until 10 1 runqlat_ready "${RUNQLAT_POD_A}" "${runqlat_generation}" || fatal "${runqlat_workers} workers did not initialize"
	runqlat_phase="workers-${runqlat_workers}"
	log_info "runqlat contention: Pod=${RUNQLAT_POD_A} CPU=${RUNQLAT_CPU} workers=${runqlat_workers} quota=1C"
	runqlat_phase "${runqlat_phase}" "${RUNQLAT_POD_A}"
	for runqlat_zone in 1 2 3; do
		if jq -e --argjson zone "${runqlat_zone}" '.host[$zone] > 0 and .container[$zone] > 0' \
			"${RUNQLAT_DIR}/${runqlat_phase}-growth.json" > /dev/null; then runqlat_zones_seen[runqlat_zone]=true; fi
	done
done
assert_runqlat_zones
runqlat_command "${RUNQLAT_POD_A}" user 0 2
wait_until 10 1 runqlat_ready "${RUNQLAT_POD_A}" "${runqlat_generation}" || fatal "workers did not reset"

if ((${#runqlat_cpus[@]} >= 3)); then
	runqlat_pin "${RUNQLAT_POD_A}" "${runqlat_cpus[-2]},${RUNQLAT_CPU}"
	runqlat_phase multi-cpu "${RUNQLAT_POD_A}"
	runqlat_pin "${RUNQLAT_POD_A}" "${RUNQLAT_CPU}"
fi

cpu_metrics_query_batch "${RUNQLAT_DIR}" 4 8 assert_runqlat_metrics \
	> "${RUNQLAT_DIR}/concurrent-zones.json" || fatal "concurrent runqlat metrics queries failed; inspect query files"

runqlat_command host user 1000 1
taskset -c "${RUNQLAT_CPU}" "${RUNQLAT_LOAD}" host --control "${RUNQLAT_DIR}/host.command" > "${RUNQLAT_DIR}/host-worker.log" 2>&1 &
runqlat_host_pid=$!
runqlat_phase host-contention "${RUNQLAT_POD_A}"
stop_and_wait_by_pid "${runqlat_host_pid}" || fatal "host competitor did not exit cleanly"
runqlat_host_pid=""
for runqlat_signal in TERM KILL; do
	runqlat_restart "${runqlat_signal}"
	runqlat_phase "after-${runqlat_signal}" "${RUNQLAT_POD_A}"
done

runqlat_old_id=$(< "${RUNQLAT_DIR}/${RUNQLAT_POD_A}.id")
kubectl delete pod -n "${RUNQLAT_NS}" "${RUNQLAT_POD_A}" --timeout=30s
runqlat_pods=("${RUNQLAT_POD_B}")
assert_huatuo_bamai_containers_absent "deleted runqlat Pod" "${runqlat_old_id}"
wait_until 30 1 runqlat_absent "${RUNQLAT_POD_A}" || fatal "deleted Pod still exports runqlat metrics"
runqlat_create "${RUNQLAT_POD_A}"
runqlat_phase recreated "${RUNQLAT_POD_A}" 0

runqlat_disabled=true
runqlat_restart
assert_cpu_collector_disabled runqlat || fatal "blacklisted runqlat still exports metrics"
assert_cpu_tracing_status runqlat 0 || fatal "blacklisted runqlat is still running"
runqlat_disabled=false
runqlat_restart
runqlat_phase reenabled "${RUNQLAT_POD_A}" 0
assert_runqlat_logs
assert_huatuo_bamai_shutdown_with_signal TERM
assert_runqlat_logs
log_info "runqlat E2E passed; artifacts: ${RUNQLAT_DIR}"
