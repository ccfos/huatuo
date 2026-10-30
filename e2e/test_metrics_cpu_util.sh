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
source "${ROOT_DIR}/integration/lib_cpu_reference.sh"

require_commands kubectl jq findmnt ss getconf
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}"
readonly CPU_UTIL_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/cpu-util"
readonly CPU_UTIL_LOAD="${CPU_UTIL_DIR}/cpu-load"
readonly CPU_UTIL_NS="${BUSINESS_POD_NS}"
readonly CPU_UTIL_PREFIX="cpu-util-${BASHPID}-${RANDOM}"
readonly CPU_UTIL_LABEL="app=${CPU_UTIL_PREFIX}"
mkdir -p "${CPU_UTIL_DIR}"
chmod 755 "${CPU_UTIL_DIR}"
"${HUATUO_BAMAI_BIN}" --version > "${CPU_UTIL_DIR}/bamai-version.txt"

cpu_util_host_pid=""
cpu_util_concurrent_pid=""
cpu_util_generation=0
cpu_util_daemon_generation=0
cpu_util_disabled=false

cpu_util_cleanup() {
	local status=$?
	trap - EXIT
	stop_and_wait_by_pid "${cpu_util_host_pid}" || true
	stop_and_wait_by_pid "${cpu_util_concurrent_pid}" || true
	if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
		kubectl --request-timeout=10s describe pod -n "${CPU_UTIL_NS}" -l "${CPU_UTIL_LABEL}" \
			> "${CPU_UTIL_DIR}/pods-failure.txt" 2>&1 || true
	fi
	kubectl --request-timeout=30s delete deployment,daemonset,pod -n "${CPU_UTIL_NS}" \
		-l "${CPU_UTIL_LABEL}" --ignore-not-found --timeout=30s || status=1
	cpu_reference_stop || status=1
	huatuo_bamai_stop || status=1
	exit "${status}"
}
trap cpu_util_cleanup EXIT

cpu_load_prepare "${CPU_UTIL_DIR}"
cpu_load_prepare_host "${CPU_UTIL_DIR}"
cpu_metrics_prepare
cpu_reference_start "${CPU_UTIL_DIR}"

# The runner changes the UTS hostname; kubelet identifies the actual local node.
CPU_UTIL_NODE=$(kubelet_pods_json | jq -er '[.items[].spec.nodeName] | unique | if length == 1 then .[0] else error("cannot identify local kubelet node") end')
readonly CPU_UTIL_NODE
kubelet_pods_json > "${CPU_UTIL_DIR}/kubelet-before.json"

CPU_UTIL_CPU_ROOT=$(findmnt -rn -t cgroup -O cpu -o TARGET || true)
if [[ -n ${CPU_UTIL_CPU_ROOT} ]]; then
	CPU_UTIL_USAGE_ROOT=$(findmnt -rn -t cgroup -O cpuacct -o TARGET)
	CPU_UTIL_CPUSET_ROOT=$(findmnt -rn -t cgroup -O cpuset -o TARGET)
else
	CPU_UTIL_CPU_ROOT=$(findmnt -rn -t cgroup2 -o TARGET) || skip "CPU cgroup controller is not mounted"
	CPU_UTIL_USAGE_ROOT=${CPU_UTIL_CPU_ROOT}
	CPU_UTIL_CPUSET_ROOT=${CPU_UTIL_CPU_ROOT}
fi
readonly CPU_UTIL_CPU_ROOT CPU_UTIL_USAGE_ROOT CPU_UTIL_CPUSET_ROOT
CPU_UTIL_PORT=$(allocate_available_port) || fatal "cannot allocate cpu_util metrics port"
CPU_UTIL_KUBELET_PORT=${KUBELET_PODS_API##*:}
CPU_UTIL_KUBELET_PORT=${CPU_UTIL_KUBELET_PORT%%/*}
readonly CPU_UTIL_PORT CPU_UTIL_KUBELET_PORT

# Only the baseline PID recorded by the runner is stopped, not an existing service.
if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log ]]; then
	huatuo_bamai_stop
	assert_huatuo_bamai_log_clean || fatal "runner baseline daemon has unexpected errors"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${CPU_UTIL_DIR}/baseline.log"
fi
HUATUO_BAMAI_ADDR="http://127.0.0.1:${CPU_UTIL_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

cpu_util_restart_daemon() {
	local signal=${1:-TERM}
	if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]]; then
		assert_huatuo_bamai_shutdown_with_signal "${signal}"
		assert_huatuo_bamai_log_clean || fatal "cpu_util daemon has unexpected errors"
		cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${CPU_UTIL_DIR}/daemon-${cpu_util_daemon_generation}.log"
	fi
	cpu_util_daemon_generation=$((cpu_util_daemon_generation + 1))
	write_config_cpu_util > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	huatuo_bamai_start --config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" --config bamai.conf \
		--region e2e --disable-storage
}

cpu_util_command() {
	local container=$1 mode=$2 milli=$3 workers=${4:-1}
	cpu_util_generation=$((cpu_util_generation + 1))
	printf '%s %s %s %s\n' "${mode}" "${milli}" "${workers}" "${cpu_util_generation}" \
		> "${CPU_UTIL_DIR}/${container}.command.next"
	mv "${CPU_UTIL_DIR}/${container}.command.next" "${CPU_UTIL_DIR}/${container}.command"
	if [[ ${container} == host && -n ${cpu_util_host_pid} ]]; then
		wait_until 10 1 cpu_load_ready "${CPU_UTIL_DIR}/host-workload.log" "${cpu_util_generation}" \
			|| fatal "host stress-ng workload did not acknowledge its command"
	fi
}

cpu_util_create() {
	local kind=$1 name=$2 quota=$3
	shift 3
	local containers=() container
	for container in "$@"; do
		cpu_util_command "${container}" idle 0
		containers+=("${container}")
	done
	jq -n --arg kind "${kind}" --arg name "${name}" --arg ns "${CPU_UTIL_NS}" \
		--arg app "${CPU_UTIL_PREFIX}" --arg node "${CPU_UTIL_NODE}" --arg image "${CPU_LOAD_IMAGE}" \
		--arg dir "${CPU_UTIL_DIR}" --arg quota "${quota}" --args '
		$ARGS.positional | map({
			name: ., image: $image, imagePullPolicy: "Never",
			command: ["/work/cpu-load", "load", "--control", ("/work/" + . + ".command")],
			resources: {requests: {cpu: "10m", memory: "16Mi"}, limits: {memory: "64Mi"}},
			volumeMounts: [{name: "work", mountPath: "/work", readOnly: true}]
			} | if $quota != "unlimited" then .resources.limits.cpu = $quota else . end) as $containers |
		{metadata: {labels: {app: $app, "cpu-util-case": $name}},
		 spec: {nodeName: $node, terminationGracePeriodSeconds: 2,
			containers: $containers, volumes: [{name: "work", hostPath: {path: $dir, type: "Directory"}}]}} as $template |
		{apiVersion: (if $kind == "Pod" then "v1" else "apps/v1" end), kind: $kind,
		 metadata: {name: $name, namespace: $ns, labels: {app: $app, "cpu-util-case": $name}}} +
		(if $kind == "Pod" then {spec: $template.spec} else
		 {spec: {selector: {matchLabels: {"cpu-util-case": $name}}, template: $template}}
		 end)
		| if $kind == "DaemonSet" then
			.spec.template.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution =
			{nodeSelectorTerms: [{matchFields: [{key: "metadata.name", operator: "In", values: [$node]}]}]}
		  else . end
	' "${containers[@]}" > "${CPU_UTIL_DIR}/${name}.json"
	kubectl --request-timeout=20s create -f "${CPU_UTIL_DIR}/${name}.json"
	wait_until 90 1 cpu_util_pod "${name}" || fatal "controller did not create the local test Pod: ${name}"
	kubectl wait --for=condition=Ready pod -n "${CPU_UTIL_NS}" -l "cpu-util-case=${name}" --timeout=90s
}

cpu_util_pod() {
	kubectl --request-timeout=10s get pod -n "${CPU_UTIL_NS}" -l "cpu-util-case=$1" -o json \
		| jq -er '.items | if length == 1 then .[0].metadata.name else error("expected one test Pod") end'
}

cpu_util_target() {
	local pod=$1 container=$2 quota=$3 mode=$4 expected=$5 type=${6:-normal}
	local id usage cpu cpuset metric_host=${pod}
	assert_huatuo_bamai_containers_present "${CPU_UTIL_NS}" "^${pod}$" \
		"$(kubectl get pod -n "${CPU_UTIL_NS}" "${pod}" -o json | jq '.spec.containers | length')" >&2
	id=$(kubectl get pod -n "${CPU_UTIL_NS}" "${pod}" -o json \
		| jq -er --arg name "${container}" '.status.containerStatuses[] | select(.name == $name) | .containerID | sub("^[^:]+://"; "")')
	usage=$(cgroup_find_container "${CPU_UTIL_USAGE_ROOT}" "${id}") || fatal "CPU accounting cgroup missing for ${id}"
	cpu=$(cgroup_find_container "${CPU_UTIL_CPU_ROOT}" "${id}") || fatal "CPU quota cgroup missing for ${id}"
	cpuset=$(cgroup_find_container "${CPU_UTIL_CPUSET_ROOT}" "${id}") || fatal "cpuset cgroup missing for ${id}"
	[[ ${type} != daemonSet ]] || metric_host=$(hostname)
	jq -n --arg name "${pod}/${container}" --arg pod "${metric_host}" --arg container "${container}" \
		--arg ns "${CPU_UTIL_NS}" --arg type "${type}" --arg mode "${mode}" \
		--arg usage "${usage}" --arg cpu "${cpu}" --arg cpuset "${cpuset}" \
		--argjson quota "${quota}" --argjson expected "${expected}" \
		'{Name: $name, Pod: $pod, Container: $container, Namespace: $ns, Type: $type,
		  QoS: "burstable", UsagePath: $usage, CPUPath: $cpu, CpusetPath: $cpuset,
		  QuotaMilli: $quota, ExpectedMilli: $expected, Mode: $mode}'
}

cpu_util_host_target() {
	local pids='[]'
	if [[ -n ${cpu_util_host_pid} ]]; then
		if [[ $1 == idle ]]; then
			pids="[${cpu_util_host_pid}]"
		else
			pids=$(cpu_load_worker_pids "${CPU_UTIL_DIR}/host-workload.log" "${cpu_util_generation}" | jq -s '.') || return 1
		fi
	fi
	jq -n --arg path "${CPU_UTIL_USAGE_ROOT}" --arg mode "$1" --argjson expected "$2" \
		--argjson pids "${pids}" \
		'{Name: "host", Pod: "", UsagePath: $path, ExpectedMilli: -1, Mode: "", WorkerPIDs: $pids, WorkerMode: $mode, WorkerExpectedMilli: $expected}'
}

assert_cpu_util_logs() {
	local start=$1
	tail -n "+${start}" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" > "${CPU_UTIL_DIR}/steady.log"
	! grep -E 'cpu (quota|capacity|usage|cache):|host cpu usage:|level="?(error|panic|fatal)"?|panic:' "${CPU_UTIL_DIR}/steady.log" \
		|| fatal "unexpected cpu_util/daemon diagnostic during a stable measurement"
}

assert_cpu_util_samples() {
	local file=$1
	jq -e --arg host "$(hostname)" "${CPU_METRICS_JQ}"'
		.Targets as $targets | ($targets | length) > 0 and (.Samples | length) >= 2
		and all(.Samples[]; . as $s | collector_ok(.Families; "cpu_util")
   and all(range(0; $targets | length); . as $i | $targets[$i] as $t |
    (if $t.Pod == "" then {host: $host, region: "e2e"} else container_labels($t; $host) end) as $labels |
    ("huatuo_bamai_cpu_util_" + (if $t.Pod == "" then "" else "container_" end)) as $prefix |
    all(["usr","sys","total"][]; . as $field | cpu_metric($s.Families[$prefix + $field]; $labels) <= 100)
    and (if $t.Pod == "" then true else
     (cpu_metric($s.Families[$prefix + "cores"]; $labels) - $s.Cores[$i] | fabs) < 0.000001
     and ($s.QuotaMilli[$i] - $t.QuotaMilli | fabs) <= 0.01 end)))
 ' "${file}" > /dev/null || fatal "cpu_util metric type, labels, capacity or 0..100 range is invalid; inspect ${file}"
}

assert_cpu_util_result() {
	local file=$1
	jq -r '.[] | "\(.Target.Name) cores=\(.Cores) seconds=\(.Seconds) metrics=\(.Metrics) reference=\(.Reference)",
  (if (.Target.WorkerPIDs | length) > 0 then "host worker pids=\(.Target.WorkerPIDs) usage=\(.Worker) theoretical-increment=\(.WorkerIncrement) percentage points" else empty end)' "${file}"
	jq -e '
  def workload($mode; $expected; $r; $cores):
   ($r.User + $r.Sys) as $accounted |
   ($expected < 0 or (($r.Total * $cores * 10 - $expected | fabs) <= ([10, $expected * 0.15] | max)))
   and (if $mode == "user" then $accounted > 0 and $r.User >= $accounted * 0.7
    elif $mode == "system" then $accounted > 0 and $r.Sys >= $accounted * 0.1
    elif $mode == "mixed" then $accounted > 0 and $r.User >= $accounted * 0.1 and $r.Sys >= $accounted * 0.1
    else true end);
  # Host-wide background activity amplifies differences between sampling windows.
  all(.[]; . as $r |
   (if .Target.Pod == "" then 2 else 1 end) as $min_pp |
   all(["User","Sys","Total"][]; . as $field |
    ($r.Metrics[$field] - $r.Reference[$field] | fabs) <= ([$min_pp, $r.Reference[$field] * 0.05] | max))
   and workload(.Target.Mode; .Target.ExpectedMilli; .Reference; .Cores)
   and (if (.Target.WorkerPIDs | length) > 0 then workload(.Target.WorkerMode; .Target.WorkerExpectedMilli; .Worker; 1) else true end))
 ' "${file}" > /dev/null || fatal "cpu_util metrics/reference or workload classification differs; inspect ${file}"
}

cpu_util_absent() {
	assert_cpu_metrics_absent cpu_util "$1"
}

cpu_util_measure() {
	local phase=$1 start
	shift
	local targets="${CPU_UTIL_DIR}/${phase}-targets.json"
	printf '%s\n' "$@" | jq -se --argjson count "$#" \
		'if length == $count then . else error("CPU reference target missing; check container discovery") end' > "${targets}"
	start=$(($(wc -l < "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log") + 1))
	log_info "cpu_util phase: ${phase}"
	cpu_util_collect "${targets}" "${CPU_UTIL_DIR}/${phase}" 2>&1 | tee "${CPU_UTIL_DIR}/${phase}.log"
	assert_cpu_util_samples "${CPU_UTIL_DIR}/${phase}/samples.json"
	assert_cpu_util_result "${CPU_UTIL_DIR}/${phase}/result.json"
	assert_cpu_util_logs "${start}"
}

cpu_util_container_phase() {
	local phase=$1 pod=$2 container=$3 quota=$4 mode=$5 requested=$6 expected=$7 workers=${8:-1}
	cpu_util_command "${container}" "${mode}" "${requested}" "${workers}"
	cpu_util_measure "${phase}" "$(cpu_util_host_target idle -1)" \
		"$(cpu_util_target "${pod}" "${container}" "${quota}" "${mode}" "${expected}")"
}

cpu_util_restart_daemon
cpu_util_create Pod "${CPU_UTIL_PREFIX}-half" 500m half
cpu_util_half_pod=$(cpu_util_pod "${CPU_UTIL_PREFIX}-half")
cpu_util_container_phase idle "${cpu_util_half_pod}" half 500 idle 0 0
cpu_util_container_phase fractional-user "${cpu_util_half_pod}" half 500 user 250 250
cpu_util_container_phase fractional-system "${cpu_util_half_pod}" half 500 system 250 250
cpu_util_container_phase fractional-mixed "${cpu_util_half_pod}" half 500 mixed 250 250
cpu_util_container_phase saturation "${cpu_util_half_pod}" half 500 user 1000 500
cpu_util_container_phase recovery-idle "${cpu_util_half_pod}" half 500 idle 0 0

cpu_util_create Pod "${CPU_UTIL_PREFIX}-one" 1000m one
cpu_util_one_pod=$(cpu_util_pod "${CPU_UTIL_PREFIX}-one")
cpu_util_command half user 250
cpu_util_command one user 250
cpu_util_measure isolation "$(cpu_util_host_target idle -1)" \
	"$(cpu_util_target "${cpu_util_half_pod}" half 500 user 250)" \
	"$(cpu_util_target "${cpu_util_one_pod}" one 1000 user 250)"
cpu_util_command half idle 0
cpu_util_command one idle 0

if [[ $(getconf _NPROCESSORS_ONLN) -ge 2 ]]; then
	cpu_util_create Pod "${CPU_UTIL_PREFIX}-two" 2000m two
	cpu_util_two_pod=$(cpu_util_pod "${CPU_UTIL_PREFIX}-two")
	cpu_util_target "${cpu_util_two_pod}" two 2000 user 1000 | jq -s '.' > "${CPU_UTIL_DIR}/two-capacity.json"
	cpu_util_two_capacity=$(cpu_reference_capacity "$(jq -c '.[0]' "${CPU_UTIL_DIR}/two-capacity.json")" | jq .Cores)
	if awk -v cores="${cpu_util_two_capacity}" 'BEGIN { exit !(cores >= 2) }'; then
		cpu_util_container_phase two-cores-one-worker "${cpu_util_two_pod}" two 2000 user 1000 1000
		cpu_util_container_phase two-cores-two-workers "${cpu_util_two_pod}" two 2000 user 2000 2000 2
	else
		log_warn "SKIP subcase: two-core quota is constrained by cpuset to ${cpu_util_two_capacity} cores"
	fi
	cpu_util_command two idle 0
else
	log_warn "SKIP subcase: two-core quota requires at least two online CPUs"
fi

cpu_util_create Pod "${CPU_UTIL_PREFIX}-unlimited" unlimited unlimited
cpu_util_unlimited_pod=$(cpu_util_pod "${CPU_UTIL_PREFIX}-unlimited")
cpu_util_container_phase unlimited "${cpu_util_unlimited_pod}" unlimited -1 user 1000 1000
cpu_util_command unlimited idle 0

cpu_util_create Deployment "${CPU_UTIL_PREFIX}-sidecar" 1000m app istio-proxy
cpu_util_sidecar_pod=$(cpu_util_pod "${CPU_UTIL_PREFIX}-sidecar")
cpu_util_command app user 250
cpu_util_command istio-proxy system 250
cpu_util_measure sidecar "$(cpu_util_host_target idle -1)" \
	"$(cpu_util_target "${cpu_util_sidecar_pod}" app 1000 user 250)" \
	"$(cpu_util_target "${cpu_util_sidecar_pod}" istio-proxy 1000 system 250 sidecar)"
cpu_util_command app idle 0
cpu_util_command istio-proxy idle 0
cpu_util_create DaemonSet "${CPU_UTIL_PREFIX}-daemonset" 1000m "ds-${CPU_UTIL_PREFIX}"
cpu_util_daemonset_pod=$(cpu_util_pod "${CPU_UTIL_PREFIX}-daemonset")
cpu_util_target "${cpu_util_daemonset_pod}" "ds-${CPU_UTIL_PREFIX}" 1000 idle 0 daemonSet | jq -s '.' > "${CPU_UTIL_DIR}/excluded.json"
cpu_util_absent "${CPU_UTIL_DIR}/excluded.json"

cpu_util_command host idle 0
"${CPU_UTIL_LOAD}" host --control "${CPU_UTIL_DIR}/host.command" > "${CPU_UTIL_DIR}/host-workload.log" 2>&1 &
cpu_util_host_pid=$!
for cpu_util_mode in idle user system mixed idle; do
	cpu_util_requested=1000
	[[ ${cpu_util_mode} != idle ]] || cpu_util_requested=0
	cpu_util_command host "${cpu_util_mode}" "${cpu_util_requested}"
	cpu_util_measure "host-${cpu_util_mode}-${cpu_util_generation}" "$(cpu_util_host_target "${cpu_util_mode}" "${cpu_util_requested}")"
done
stop_and_wait_by_pid "${cpu_util_host_pid}" || fatal "host workload did not exit cleanly"
cpu_util_host_pid=""

cpu_util_command half mixed 250
cpu_util_target "${cpu_util_half_pod}" half 500 mixed 250 | jq -s '.' > "${CPU_UTIL_DIR}/concurrent-targets.json"
cpu_util_concurrent_start=$(($(wc -l < "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log") + 1))
cpu_reference_concurrent util "${CPU_UTIL_DIR}/concurrent-targets.json" "${CPU_UTIL_DIR}/concurrent-responses" 40 \
	> "${CPU_UTIL_DIR}/concurrent-samples.json" 2> "${CPU_UTIL_DIR}/concurrent-requests.log" &
cpu_util_concurrent_pid=$!
wait "${cpu_util_concurrent_pid}" || fatal "concurrent metrics queries failed"
cpu_util_concurrent_pid=""
assert_cpu_util_samples "${CPU_UTIL_DIR}/concurrent-samples.json"
assert_cpu_util_logs "${cpu_util_concurrent_start}"
# Parallel scrapes refresh the cache between samples, invalidating the reference window.
cpu_util_measure concurrent "$(cpu_util_host_target idle -1)" "$(cpu_util_target "${cpu_util_half_pod}" half 500 mixed 250)"

for cpu_util_signal in TERM KILL; do
	cpu_util_restart_daemon "${cpu_util_signal}"
	cpu_util_container_phase "daemon-restart-${cpu_util_signal}" "${cpu_util_half_pod}" half 500 mixed 250 250
done

cpu_util_old_id=$(kubelet_container_ids "${CPU_UTIL_NS}" "^${cpu_util_half_pod}$")
cpu_util_cgroup=$(cgroup_find_container "${CPU_UTIL_CPU_ROOT}" "${cpu_util_old_id}") || fatal "test container CPU cgroup disappeared before SIGKILL"
mapfile -t cpu_util_container_pids < "${cpu_util_cgroup}/cgroup.procs"
cpu_util_init_pid=
for cpu_util_pid in "${cpu_util_container_pids[@]}"; do
	if awk '$1=="NSpid:" && $NF==1 { found=1 } END { exit !found }' "/proc/${cpu_util_pid}/status"; then
		cpu_util_init_pid=${cpu_util_pid}
		break
	fi
done
[[ -n ${cpu_util_init_pid} ]] || fatal "cannot find the test container namespace init PID before SIGKILL"
# Namespace init processes may ignore SIGKILL sent from within their namespace.
grep -Fq "${cpu_util_old_id}" "/proc/${cpu_util_init_pid}/cgroup" || fatal "container init PID no longer belongs to the test cgroup"
kill -KILL "${cpu_util_init_pid}"
cpu_util_container_restarted() {
	local new_id
	new_id=$(kubelet_container_ids "${CPU_UTIL_NS}" "^${cpu_util_half_pod}$") || return 1
	[[ -n ${new_id} && ${new_id} != "${cpu_util_old_id}" ]] || return 1
	huatuo_bamai_containers_present "${CPU_UTIL_NS}" "^${cpu_util_half_pod}$" 1
}
wait_until 90 2 cpu_util_container_restarted || fatal "test container did not restart with a new ID"
assert_huatuo_bamai_containers_absent "old container after restart" "${cpu_util_old_id}"
cpu_util_container_phase container-restart "${cpu_util_half_pod}" half 500 mixed 250 250

cpu_util_old_id=$(kubelet_container_ids "${CPU_UTIL_NS}" "^${cpu_util_half_pod}$")
cpu_util_target "${cpu_util_half_pod}" half 500 mixed 250 | jq -s '.' > "${CPU_UTIL_DIR}/deleted-target.json"
kubectl delete pod -n "${CPU_UTIL_NS}" "${cpu_util_half_pod}" --timeout=30s
assert_huatuo_bamai_containers_absent "deleted test Pod" "${cpu_util_old_id}"
wait_until 30 2 cpu_util_absent "${CPU_UTIL_DIR}/deleted-target.json" \
	|| fatal "deleted Pod still exports CPU metrics"
cpu_util_create Pod "${CPU_UTIL_PREFIX}-half" 500m half
cpu_util_half_pod=$(cpu_util_pod "${CPU_UTIL_PREFIX}-half")
cpu_util_container_phase pod-recreated "${cpu_util_half_pod}" half 500 user 250 250

cpu_util_disabled=true
cpu_util_restart_daemon
assert_cpu_collector_disabled cpu_util || fatal "blacklisted cpu_util still exports metrics"
cpu_util_disabled=false
cpu_util_restart_daemon
cpu_util_container_phase blacklist-reenabled "${cpu_util_half_pod}" half 500 user 250 250
log_info "cpu_util E2E passed; artifacts: ${CPU_UTIL_DIR}"
