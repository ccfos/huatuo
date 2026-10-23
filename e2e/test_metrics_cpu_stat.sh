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

require_commands kubectl jq findmnt ss sysctl taskset getconf
require_readable "${KUBELET_CERT}" "${KUBELET_KEY}" /proc/sys/kernel/sched_schedstats
readonly CPU_STAT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/cpu-stat"
readonly CPU_STAT_BIN="${CPU_STAT_DIR}/cpu-stat"
readonly CPU_STAT_LOAD="${CPU_STAT_DIR}/cpu-load"
readonly CPU_STAT_NS="${BUSINESS_POD_NS}"
readonly CPU_STAT_PREFIX="cpu-stat-${BASHPID}-${RANDOM}"
readonly CPU_STAT_LABEL="app=${CPU_STAT_PREFIX}"
readonly CPU_STAT_SCHEDSTATS=$(sysctl -n kernel.sched_schedstats)
readonly CPU_STAT_TICKS=$(getconf CLK_TCK)
CPU_STAT_DISABLED=false
cpu_stat_wait=false
cpu_stat_host_pid=""
cpu_stat_host_cgroup=""
cpu_stat_generation=0
cpu_stat_daemon_generation=0
mkdir -p "${CPU_STAT_DIR}"
chmod 755 "${CPU_STAT_DIR}"
"${HUATUO_BAMAI_BIN}" --version > "${CPU_STAT_DIR}/bamai-version.txt"
uname -a > "${CPU_STAT_DIR}/kernel.txt"

cpu_stat_cleanup() {
	local status=$?
	trap - EXIT
	stop_and_wait_by_pid "${cpu_stat_host_pid}" || true
	if [[ -n ${cpu_stat_host_cgroup} ]]; then
		rmdir "${cpu_stat_host_cgroup}" || status=1
	fi
	if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
		kubectl --request-timeout=10s describe pod -n "${CPU_STAT_NS}" -l "${CPU_STAT_LABEL}" \
			> "${CPU_STAT_DIR}/pods-failure.txt" 2>&1 || true
	fi
	kubectl --request-timeout=30s delete pod -n "${CPU_STAT_NS}" -l "${CPU_STAT_LABEL}" \
		--ignore-not-found --timeout=30s || status=1
	huatuo_bamai_stop || status=1
	sysctl -q -w "kernel.sched_schedstats=${CPU_STAT_SCHEDSTATS}" || status=1
	[[ $(sysctl -n kernel.sched_schedstats) == "${CPU_STAT_SCHEDSTATS}" ]] || status=1
	exit "${status}"
}
trap cpu_stat_cleanup EXIT

if [[ -n ${CPU_STAT_HELPER_BIN:-} ]]; then
	require_commands "${CPU_STAT_HELPER_BIN}"
	cp "${CPU_STAT_HELPER_BIN}" "${CPU_STAT_BIN}"
else
	require_commands go
	CGO_ENABLED=0 go build -mod=vendor -o "${CPU_STAT_BIN}" "${ROOT_DIR}/e2e/testdata/test_cpu_stat.go" \
		"${ROOT_DIR}/e2e/testdata/metrics_helpers.go" "${ROOT_DIR}/e2e/testdata/proc_helpers.go" "${ROOT_DIR}/e2e/testdata/fixture_helpers.go"
fi
cpu_load_prepare "${CPU_STAT_DIR}"

CPU_STAT_NODE=$(kubelet_pods_json | jq -er '[.items[].spec.nodeName] | unique | if length == 1 then .[0] else error("cannot identify local kubelet node") end')
CPU_STAT_CPU=$(awk '/Cpus_allowed_list/ { n=split($2, cpus, /[-,]/); print cpus[n] }' /proc/self/status)
CPU_STAT_CPUS=$(awk '/Cpus_allowed_list/ {
	n=split($2, ranges, ","); count=0
	for (i=1; i<=n; i++) {
		m=split(ranges[i], ends, "-"); last=(m==2 ? ends[2] : ends[1])
		for (cpu=ends[1]; cpu<=last; cpu++) cpus[++count]=cpu
	}
	if (count<3) exit
	a=cpus[count-2]; b=cpus[count-1]; c=cpus[count]
	if (b==a+1 && c==b+1) print a "-" c
	else if (b==a+1) print a "-" b "," c
	else if (c==b+1) print a "," b "-" c
	else print a "," b "," c
}' /proc/self/status)
CPU_STAT_CPU_ROOT=$(findmnt -rn -t cgroup -O cpu -o TARGET || true)
CPU_STAT_V2=false
if [[ -n ${CPU_STAT_CPU_ROOT} ]]; then
	CPU_STAT_USAGE_ROOT=$(findmnt -rn -t cgroup -O cpuacct -o TARGET)
	CPU_STAT_CPUSET_ROOT=$(findmnt -rn -t cgroup -O cpuset -o TARGET)
	CPU_STAT_WEIGHT_FILE=cpu.shares
else
	CPU_STAT_V2=true
	CPU_STAT_CPU_ROOT=$(findmnt -rn -t cgroup2 -o TARGET) || skip "CPU cgroup controller is not mounted"
	CPU_STAT_USAGE_ROOT=${CPU_STAT_CPU_ROOT}
	CPU_STAT_CPUSET_ROOT=${CPU_STAT_CPU_ROOT}
	CPU_STAT_WEIGHT_FILE=cpu.weight
fi
CPU_STAT_PORT=$(allocate_available_port) || fatal "cannot allocate cpu_stat metrics port"
CPU_STAT_KUBELET_PORT=${KUBELET_PODS_API##*:}
CPU_STAT_KUBELET_PORT=${CPU_STAT_KUBELET_PORT%%/*}
readonly CPU_STAT_NODE CPU_STAT_CPU CPU_STAT_CPUS CPU_STAT_V2 CPU_STAT_WEIGHT_FILE
readonly CPU_STAT_CPU_ROOT CPU_STAT_USAGE_ROOT CPU_STAT_CPUSET_ROOT CPU_STAT_PORT CPU_STAT_KUBELET_PORT
log_info "cpu_stat: cgroup_v2=${CPU_STAT_V2} CPU=${CPU_STAT_CPU} schedstats_original=${CPU_STAT_SCHEDSTATS}"

if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log ]]; then
	huatuo_bamai_stop
	huatuo_bamai_log_check || fatal "runner baseline daemon has unexpected errors"
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${CPU_STAT_DIR}/baseline.log"
fi
HUATUO_BAMAI_ADDR="http://127.0.0.1:${CPU_STAT_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

cpu_stat_restart_daemon() {
	local signal=${1:-TERM}
	if [[ -f ${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid ]]; then
		huatuo_bamai_stop_with_signal "${signal}"
		huatuo_bamai_log_check || fatal "cpu_stat daemon has unexpected errors"
		cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${CPU_STAT_DIR}/daemon-${cpu_stat_daemon_generation}.log"
	fi
	cpu_stat_daemon_generation=$((cpu_stat_daemon_generation + 1))
	integration_huatuo_bamai_start write_cpu_stat_config --region e2e --disable-storage
}

cpu_stat_command() {
	local name=$1 mode=$2 milli=$3 workers=${4:-1}
	cpu_stat_generation=$((cpu_stat_generation + 1))
	printf '%s %s %s %s\n' "${mode}" "${milli}" "${workers}" "${cpu_stat_generation}" > "${CPU_STAT_DIR}/${name}.command.next"
	mv "${CPU_STAT_DIR}/${name}.command.next" "${CPU_STAT_DIR}/${name}.command"
	if [[ ${name} == host && -n ${cpu_stat_host_pid} ]]; then
		wait_until 10 1 cpu_load_ready "${CPU_STAT_DIR}/host-workload.log" "${cpu_stat_generation}" \
			|| fatal "host stress-ng workload did not acknowledge its command"
	fi
}

cpu_stat_command_ready() {
	kubectl --request-timeout=5s logs -n "${CPU_STAT_NS}" "$1" -c worker --tail=5 | grep -Fxq "ready $2"
}

cpu_stat_settled_phase() {
	local name=$1 phase=$2
	cpu_stat_command "${name}" idle 0
	# Acknowledge idle only after stress-ng has reaped its workers.
	wait_until 10 1 cpu_stat_command_ready "${name}" "${cpu_stat_generation}" \
		|| fatal "workers did not stop in Pod ${name}; inspect its worker logs"
	cpu_stat_target "${name}" 0 -1 | jq -s '.' > "${CPU_STAT_DIR}/quota-targets.json"
	cpu_stat_measure "${phase}" "${CPU_STAT_DIR}/quota-targets.json" --counter-mode settled --counter-only
}

cpu_stat_create() {
	local name=$1 quota=$2 cpus=${3:-${CPU_STAT_CPU}}
	cpu_stat_command "${name}" idle 0
	jq -n --arg name "${name}" --arg ns "${CPU_STAT_NS}" --arg app "${CPU_STAT_PREFIX}" \
		--arg node "${CPU_STAT_NODE}" --arg image "${CPU_LOAD_IMAGE}" --arg dir "${CPU_STAT_DIR}" --arg quota "${quota}" '
		{apiVersion: "v1", kind: "Pod", metadata: {name: $name, namespace: $ns, labels: {app: $app}},
		 spec: {nodeName: $node, terminationGracePeriodSeconds: 2,
		   containers: [{name: "worker", image: $image, imagePullPolicy: "Never",
		     command: ["/work/cpu-load", "load", "--control", ("/work/" + $name + ".command")],
		     resources: {requests: {cpu: "100m", memory: "16Mi"}, limits: {cpu: $quota, memory: "64Mi"}},
		     volumeMounts: [{name: "work", mountPath: "/work", readOnly: true}]}],
		   volumes: [{name: "work", hostPath: {path: $dir, type: "Directory"}}]}}
	' > "${CPU_STAT_DIR}/${name}.json"
	kubectl --request-timeout=20s create -f "${CPU_STAT_DIR}/${name}.json"
	kubectl wait --for=condition=Ready pod -n "${CPU_STAT_NS}" "${name}" --timeout=90s
	assert_huatuo_bamai_containers_present "${CPU_STAT_NS}" "^${name}$" 1
	cpu_stat_target "${name}" -1 -1 > "${CPU_STAT_DIR}/${name}-target.json"
	local cpuset cpu quota_actual
	cpuset=$(cgroup_find_container "${CPU_STAT_CPUSET_ROOT}" "$(kubelet_container_ids "${CPU_STAT_NS}" "^${name}$")") \
		|| fatal "test Pod cpuset cgroup missing: ${name}"
	echo "${cpus}" > "${cpuset}/cpuset.cpus"
	local effective=cpuset.cpus
	[[ ${CPU_STAT_V2} != true ]] || effective=cpuset.cpus.effective
	assert_eq "$(< "${cpuset}/${effective}")" "${cpus}" "test Pod effective cpuset" || fatal "Pod was not pinned to the selected CPUs"
	cpu=$(jq -r '.CPUPath' "${CPU_STAT_DIR}/${name}-target.json")
	if [[ ${CPU_STAT_V2} == true ]]; then
		quota_actual=$(awk '{print $1/$2}' "${cpu}/cpu.max")
	else
		quota_actual=$(awk -v period="$(< "${cpu}/cpu.cfs_period_us")" '{print $1/period}' "${cpu}/cpu.cfs_quota_us")
	fi
	assert_eq "${quota_actual}" "${quota}" "test Pod quota" || fatal "kernel quota does not match Pod limit"
}

cpu_stat_quota_config() {
	local cpu=$1 burst_file=$2 quota period parent_quota parent_period
	if [[ ${CPU_STAT_V2} == true ]]; then
		read -r quota period < "${cpu}/cpu.max"
		read -r parent_quota parent_period < "${cpu}/../cpu.max"
	else
		quota=$(< "${cpu}/cpu.cfs_quota_us")
		period=$(< "${cpu}/cpu.cfs_period_us")
		parent_quota=$(< "${cpu}/../cpu.cfs_quota_us")
		parent_period=$(< "${cpu}/../cpu.cfs_period_us")
	fi
	log_info "quota Pod: cpuset=${CPU_STAT_CPUS} quota_us=${quota} period_us=${period} parent_quota_us=${parent_quota} parent_period_us=${parent_period}"
	[[ ${quota} -eq $((2 * period)) ]] || fatal "container 2C quota was overwritten"
	[[ ${parent_quota} -eq $((3 * parent_period)) ]] || fatal "owned Pod 3C parent quota was overwritten"
	if [[ -f ${cpu}/${burst_file} ]]; then
		log_info "quota Pod: ${burst_file}=$(< "${cpu}/${burst_file}")"
	fi
}

cpu_stat_quota_scenarios() {
	if [[ -z ${CPU_STAT_CPUS} ]]; then
		log_warn "SKIP subcases: 2C throttling/burst workloads require at least three allowed CPUs"
		return
	fi
	local name="${CPU_STAT_PREFIX}-quota" cpu parent period parent_period burst_file burst_time
	cpu_stat_create "${name}" 2 "${CPU_STAT_CPUS}"
	cpu=$(jq -r '.CPUPath' "${CPU_STAT_DIR}/${name}-target.json")
	parent=$(dirname "${cpu}")
	# Only widen this test Pod's parent: otherwise its 2C cap masks leaf bursts.
	if [[ ${CPU_STAT_V2} == true ]]; then
		read -r _ period < "${cpu}/cpu.max"
		read -r _ parent_period < "${parent}/cpu.max"
		echo "$((3 * parent_period)) ${parent_period}" > "${parent}/cpu.max"
		burst_file=cpu.max.burst
		burst_time=burst_usec
	else
		period=$(< "${cpu}/cpu.cfs_period_us")
		parent_period=$(< "${parent}/cpu.cfs_period_us")
		echo "$((3 * parent_period))" > "${parent}/cpu.cfs_quota_us"
		burst_file=cpu.cfs_burst_us
		burst_time=burst_time
	fi
	[[ ! -f ${cpu}/${burst_file} ]] || echo 0 > "${cpu}/${burst_file}"
	cpu_stat_quota_config "${cpu}" "${burst_file}" | tee "${CPU_STAT_DIR}/quota-config.log"
	cpu_stat_target "${name}" 100 -1 | jq -s '.' > "${CPU_STAT_DIR}/quota-targets.json"
	cpu_stat_command "${name}" user 1000
	cpu_stat_measure quota-below-limit "${CPU_STAT_DIR}/quota-targets.json" --counter-mode stable --counter-only

	cpu_stat_target "${name}" 200 -1 | jq -s '.' > "${CPU_STAT_DIR}/quota-targets.json"
	cpu_stat_command "${name}" user 0 3
	cpu_stat_measure quota-throttling "${CPU_STAT_DIR}/quota-targets.json" --counter-mode throttled --counter-only
	cpu_stat_quota_config "${cpu}" "${burst_file}" | tee -a "${CPU_STAT_DIR}/quota-config.log"
	cpu_stat_settled_phase "${name}" quota-throttling-settled

	if [[ -f ${cpu}/${burst_file} ]] && grep -q '^nr_bursts ' "${cpu}/cpu.stat" && grep -q "^${burst_time} " "${cpu}/cpu.stat"; then
		echo "$((2 * period))" > "${cpu}/${burst_file}"
		cpu_stat_quota_config "${cpu}" "${burst_file}" | tee -a "${CPU_STAT_DIR}/quota-config.log"
		cpu_stat_target "${name}" -1 -1 | jq -s '.' > "${CPU_STAT_DIR}/quota-targets.json"
		# Each pulse runs 3 workers for 200ms, then idles 300ms to refill.
		cpu_stat_command "${name}" burst 0 3
		cpu_stat_measure quota-burst "${CPU_STAT_DIR}/quota-targets.json" --counter-mode burst --counter-only
		assert_eq "$(< "${cpu}/${burst_file}")" "$((2 * period))" "container burst budget" || fatal "burst budget was overwritten"
		cpu_stat_settled_phase "${name}" quota-burst-settled
	else
		log_warn "SKIP subcase: kernel does not expose ${burst_file} and both burst counters"
	fi
	cpu_stat_target "${name}" 0 -1 | jq -s '.' > "${CPU_STAT_DIR}/quota-targets.json"
	cpu_stat_measure quota-idle-recovery "${CPU_STAT_DIR}/quota-targets.json" --counter-mode stable --counter-only
	cpu_stat_quota_config "${cpu}" "${burst_file}" | tee -a "${CPU_STAT_DIR}/quota-config.log"
}

cpu_stat_target() {
	local name=$1 expected_cpu=$2 expected_wait=$3 id cpu usage
	id=$(kubelet_container_ids "${CPU_STAT_NS}" "^${name}$")
	cpu=$(cgroup_find_container "${CPU_STAT_CPU_ROOT}" "${id}") || fatal "CPU cgroup missing for ${name}"
	usage=$(cgroup_find_container "${CPU_STAT_USAGE_ROOT}" "${id}") || fatal "CPU accounting cgroup missing for ${name}"
	jq -n --arg pod "${name}" --arg ns "${CPU_STAT_NS}" --arg cpu "${cpu}" --arg usage "${usage}" \
		--argjson expected_cpu "${expected_cpu}" --argjson expected_wait "${expected_wait}" \
		'{Pod: $pod, Container: "worker", Namespace: $ns, CPUPath: $cpu, UsagePath: $usage,
		  ExpectedCPU: $expected_cpu, ExpectedWait: $expected_wait}'
}

cpu_stat_measure() {
	local phase=$1 targets=$2 start
	shift 2
	start=$(($(wc -l < "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log") + 1))
	log_info "cpu_stat phase: ${phase}"
	"${CPU_STAT_BIN}" measure --url "${HUATUO_BAMAI_METRICS_API}" --targets "${targets}" --wait="${cpu_stat_wait}" \
		--ticks "${CPU_STAT_TICKS}" --output "${CPU_STAT_DIR}/${phase}" "$@" 2>&1 | tee "${CPU_STAT_DIR}/${phase}.log"
	tail -n "+${start}" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" > "${CPU_STAT_DIR}/steady.log"
	! grep -E 'failed to update cpu info|cpu_stat:|level="?(error|panic|fatal)"?|panic:' "${CPU_STAT_DIR}/steady.log" \
		|| fatal "unexpected cpu_stat/daemon diagnostic during stable measurement"
}

cpu_stat_single_phase() {
	local phase=$1 expected_cpu=$2 expected_wait=$3
	shift 3
	cpu_stat_target "${a}" "${expected_cpu}" "${expected_wait}" | jq -s '.' > "${CPU_STAT_DIR}/targets.json"
	cpu_stat_measure "${phase}" "${CPU_STAT_DIR}/targets.json" "$@"
}

cpu_stat_restart_daemon
readonly a="${CPU_STAT_PREFIX}-a" b="${CPU_STAT_PREFIX}-b"
cpu_stat_create "${a}" 2
cpu_stat_command "${a}" user 1000
sysctl -q -w kernel.sched_schedstats=0
[[ $(sysctl -n kernel.sched_schedstats) == 0 ]] || fatal "cannot disable schedstats"
cpu_stat_single_phase schedstats-disabled 100 -1

sysctl -q -w kernel.sched_schedstats=1
[[ $(sysctl -n kernel.sched_schedstats) == 1 ]] || fatal "cannot enable schedstats"
a_cpu=$(jq -r '.CPUPath' "${CPU_STAT_DIR}/${a}-target.json")
cp "${a_cpu}/cpu.stat" "${CPU_STAT_DIR}/schedstats-enabled.stat"
if grep -q '^wait_sum ' "${a_cpu}/cpu.stat"; then
	cpu_stat_wait=true
else
	log_warn "SKIP subcases: kernel cpu.stat does not export wait_sum with schedstats enabled; contention percentages cannot be tested"
fi
cpu_stat_single_phase schedstats-enabled 100 0

if [[ ${cpu_stat_wait} == true ]]; then
	cpu_stat_command host idle 0
	# user.slice and kubepods may have unequal weights. The owned host group
	# shares the Pod's parent; its default weight leaves room for a 40% load.
	a_parent=$(dirname "${a_cpu}")
	cpu_stat_host_cgroup="$(dirname "${a_parent}")/${CPU_STAT_PREFIX}-host"
	mkdir "${cpu_stat_host_cgroup}"
	log_info "host weight=$(< "${cpu_stat_host_cgroup}/${CPU_STAT_WEIGHT_FILE}") Pod weight=$(< "${a_parent}/${CPU_STAT_WEIGHT_FILE}")"
	taskset -c "${CPU_STAT_CPU}" bash -c 'set -e; echo "$BASHPID" > "$1/cgroup.procs"; shift; exec "$@"' \
		_ "${cpu_stat_host_cgroup}" "${CPU_STAT_LOAD}" host --control "${CPU_STAT_DIR}/host.command" \
		> "${CPU_STAT_DIR}/host-workload.log" 2>&1 &
	cpu_stat_host_pid=$!
	for percent in 20 40; do
		cpu_stat_command host user "$((percent * 10))"
		cpu_stat_single_phase "host-contention-${percent}" "$((100 - percent))" "${percent}" \
			--worker-pid "$(cpu_load_worker_pid "${CPU_STAT_DIR}/host-workload.log" "${cpu_stat_generation}")" --worker-cpu "${percent}"
	done
	stop_and_wait_by_pid "${cpu_stat_host_pid}" || fatal "host competitor did not exit cleanly"
	cpu_stat_host_pid=""
	rmdir "${cpu_stat_host_cgroup}"
	cpu_stat_host_cgroup=""
	cpu_stat_single_phase host-contention-recovery 100 0

	cpu_stat_create "${b}" 2
	cpu_stat_command "${b}" user 1000
	a_parent=$(dirname "${a_cpu}")
	b_parent=$(dirname "$(jq -r '.CPUPath' "${CPU_STAT_DIR}/${b}-target.json")")
	[[ $(dirname "${a_parent}") == "$(dirname "${b_parent}")" ]] || fatal "test Pods are not sibling CPU cgroups"
	for ratio in '1 1' '1 3' '3 1'; do
		read -r a_ratio b_ratio <<< "${ratio}"
		base=1024
		[[ ${CPU_STAT_V2} != true ]] || base=100
		echo "$((base * a_ratio))" > "${a_parent}/${CPU_STAT_WEIGHT_FILE}"
		echo "$((base * b_ratio))" > "${b_parent}/${CPU_STAT_WEIGHT_FILE}"
		phase="pod-weights-${a_ratio}-${b_ratio}"
		{
			log_info "${phase} before: Pod=${a} file=${a_parent}/${CPU_STAT_WEIGHT_FILE} configured=$((base * a_ratio)) actual=$(< "${a_parent}/${CPU_STAT_WEIGHT_FILE}")"
			log_info "${phase} before: Pod=${b} file=${b_parent}/${CPU_STAT_WEIGHT_FILE} configured=$((base * b_ratio)) actual=$(< "${b_parent}/${CPU_STAT_WEIGHT_FILE}")"
		} | tee "${CPU_STAT_DIR}/${phase}-weights.log"
		{
			cpu_stat_target "${a}" "$((100 * a_ratio / (a_ratio + b_ratio)))" "$((100 * b_ratio / (a_ratio + b_ratio)))"
			cpu_stat_target "${b}" "$((100 * b_ratio / (a_ratio + b_ratio)))" "$((100 * a_ratio / (a_ratio + b_ratio)))"
		} | jq -s '.' > "${CPU_STAT_DIR}/targets.json"
		cpu_stat_measure "${phase}" "${CPU_STAT_DIR}/targets.json"
		{
			log_info "${phase} after: Pod=${a} file=${a_parent}/${CPU_STAT_WEIGHT_FILE} actual=$(< "${a_parent}/${CPU_STAT_WEIGHT_FILE}")"
			log_info "${phase} after: Pod=${b} file=${b_parent}/${CPU_STAT_WEIGHT_FILE} actual=$(< "${b_parent}/${CPU_STAT_WEIGHT_FILE}")"
		} | tee -a "${CPU_STAT_DIR}/${phase}-weights.log"
		assert_eq "$(< "${a_parent}/${CPU_STAT_WEIGHT_FILE}")" "$((base * a_ratio))" "Pod A weight" || fatal "Pod A weight was overwritten"
		assert_eq "$(< "${b_parent}/${CPU_STAT_WEIGHT_FILE}")" "$((base * b_ratio))" "Pod B weight" || fatal "Pod B weight was overwritten"
	done
	cpu_stat_command "${b}" idle 0
	cpu_stat_single_phase pod-contention-recovery 100 0
fi

cpu_stat_command "${a}" idle 0
cpu_stat_quota_scenarios
cpu_stat_command "${a}" user 1000

for signal in TERM KILL; do
	cpu_stat_restart_daemon "${signal}"
	cpu_stat_single_phase "daemon-restart-${signal}" 100 0
done

old_id=$(kubelet_container_ids "${CPU_STAT_NS}" "^${a}$")
cpu_stat_target "${a}" -1 -1 | jq -s '.' > "${CPU_STAT_DIR}/deleted-target.json"
kubectl delete pod -n "${CPU_STAT_NS}" "${a}" --timeout=30s
assert_huatuo_bamai_containers_absent "deleted cpu_stat Pod" "${old_id}"
wait_until 30 2 "${CPU_STAT_BIN}" absent --url "${HUATUO_BAMAI_METRICS_API}" --targets "${CPU_STAT_DIR}/deleted-target.json" \
	|| fatal "deleted Pod still exports cpu_stat metrics"
cpu_stat_create "${a}" 2
cpu_stat_command "${a}" user 1000
cpu_stat_single_phase pod-recreated 100 0

CPU_STAT_DISABLED=true
cpu_stat_restart_daemon
"${CPU_STAT_BIN}" disabled --url "${HUATUO_BAMAI_METRICS_API}"
CPU_STAT_DISABLED=false
cpu_stat_restart_daemon
cpu_stat_single_phase blacklist-reenabled 100 0

# Keep concurrent scrapes outside numeric windows: they advance the baseline.
"${CPU_STAT_BIN}" concurrent --url "${HUATUO_BAMAI_METRICS_API}" --targets "${CPU_STAT_DIR}/targets.json" \
	--wait="${cpu_stat_wait}" --ticks "${CPU_STAT_TICKS}" 2>&1 | tee "${CPU_STAT_DIR}/concurrent.log"
log_info "cpu_stat E2E passed; artifacts: ${CPU_STAT_DIR}"
