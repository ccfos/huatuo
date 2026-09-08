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

# Verify externally observable iterator, EMA, lifecycle and partial-output
# contracts through the standard daemon. Internal generation/capacity and
# exact cache deadlines are not inferred from these acceptance checks.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_memsnapshot.sh"

[[ ${EUID} -eq 0 ]] || skip "requires root"
[[ -r /sys/fs/cgroup/cgroup.controllers ]] || skip "requires native cgroup v2"
[[ -r /sys/kernel/btf/vmlinux ]] || skip "requires kernel BTF"
kernel_version_le 5 7 && skip "BPF task iterator requires Linux 5.8 or newer"

# The real daemon loads each target BTF through the kernel BTF path in the
# runner's private mount namespace. No production API or test-only Go hooks.
require_commands python3 mount openssl
python3 -c "import sys; sys.exit(sys.version_info < (3, 7))" || skip "metadata fixture requires Python 3.7 or newer"
if [[ -z ${HUATUO_LOAD_BTF_LAYOUT:-} ]]; then
	for load_stats_layout in scalar union; do
		log_info "kernfs BTF layout: ${load_stats_layout} (host-only and container daemon paths)"
		HUATUO_LOAD_BTF_LAYOUT=${load_stats_layout} env -u __HUATUO_ENV_SH_LOADED bash "${ROOT_DIR}/integration/run.sh" test_metrics_loadavg_cgroup.sh
	done
	exit 0
fi
python3 "${ROOT_DIR}/integration/testdata/test_observability_fixture.py" kernfs-layout \
	"${HUATUO_LOAD_BTF_LAYOUT}" "${HUATUO_BAMAI_TEST_TMPDIR}/vmlinux.btf"
mount --bind "${HUATUO_BAMAI_TEST_TMPDIR}/vmlinux.btf" /sys/kernel/btf/vmlinux

readonly observability_component=loadavg
integration_huatuo_bamai_start write_observability_config \
	--region integration --disable-kubelet --disable-storage --log-debug

load_stats_ready() {
	huatuo_bamai_collect_metrics || return 1
	grep -q '^huatuo_bamai_loadavg_nr_uninterruptible{' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}
wait_until 30 .2 load_stats_ready || fatal "daemon did not publish host task-iterator metrics"

assert_load_stats_metrics() {
	check_metrics "host loadavg and task counts" \
		'loadavg_load1\{' 'loadavg_load5\{' 'loadavg_load15\{' \
		'loadavg_nr_running\{' 'loadavg_nr_uninterruptible\{'
	awk '
	 /^huatuo_bamai_loadavg_nr_uninterruptible\{/ { found=1; if ($NF !~ /^[0-9]+$/) exit 1 }
	 END { if (!found) exit 1 }
	' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" || fatal "host D-state count is not a nonnegative integer"
}
assert_load_stats_metrics
assert_memsnapshot_tracing_metrics loadavg integration 1
huatuo_bamai_stop_with_signal TERM
assert_memsnapshot_logs "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"

# Ordinary iterator load errors must not discard procfs metrics or become a
# permanent unsupported verdict. Restore the object without restarting daemon.
readonly load_stats_bpf_dir="${HUATUO_BAMAI_TEST_TMPDIR}/retry-bpf"
mkdir -p "${load_stats_bpf_dir}"
for load_stats_object in "${ROOT_DIR}/_output/bpf/"*.o; do
	[[ ${load_stats_object##*/} == cgroup_v2_load_stats.o ]] && continue
	ln -s "${load_stats_object}" "${load_stats_bpf_dir}/"
done
integration_huatuo_bamai_start write_observability_config \
	--region integration --disable-kubelet --disable-storage --log-debug --bpf-dir "${load_stats_bpf_dir}"
# HTTP readiness precedes the first background sample on slower kernels.
load_stats_failure_ready() {
	huatuo_bamai_collect_metrics || return 1
	grep '^huatuo_bamai_scrape_collector_success{' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" \
		| grep 'collector="loadavg"' | grep -q ' 0$'
}
wait_until 30 .2 load_stats_failure_ready || fatal "iterator error was not reported before timeout"
assert_load_stats_partial_host() {
	check_metrics "procfs survives iterator failure" 'loadavg_load1\{' 'loadavg_nr_running\{' -- 'loadavg_nr_uninterruptible\{'
	grep '^huatuo_bamai_scrape_collector_success{' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" \
		| grep 'collector="loadavg"' | grep -q ' 0$' || fatal "iterator error was not reported"
}
assert_load_stats_partial_host
ln -s "${ROOT_DIR}/_output/bpf/cgroup_v2_load_stats.o" "${load_stats_bpf_dir}/cgroup_v2_load_stats.o"
wait_until 30 .2 load_stats_ready || fatal "iterator did not retry after object recovery"
assert_load_stats_metrics
huatuo_bamai_stop_with_signal TERM
assert_memsnapshot_logs "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"

readonly load_stats_fixture="${ROOT_DIR}/integration/testdata/test_observability_fixture.py"
readonly load_stats_meta="${HUATUO_BAMAI_TEST_TMPDIR}/metadata"
readonly load_stats_uid="huatuo-load-${BASHPID}"
readonly load_stats_pod="/sys/fs/cgroup/kubepods/pod${load_stats_uid}"
readonly load_stats_a=$(printf '%064x' "${BASHPID}")
readonly load_stats_b=$(printf '%064x' "$((BASHPID + 1))")
load_stats_a_pid=""
load_stats_b_pid=""
load_stats_server_pid=""
load_stats_mask=""
load_stats_proxy=""
load_stats_created_root=0
load_stats_workers=()

load_stats_cleanup() {
	set +e
	[[ ! -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid" ]] || huatuo_bamai_stop_with_signal TERM
	[[ -z "${load_stats_mask}" ]] || umount -R "${load_stats_mask}"
	[[ -z "${load_stats_proxy}" ]] || umount "${load_stats_proxy}"
	local pid
	for pid in "${load_stats_workers[@]}" "${load_stats_server_pid}"; do
		[[ -n ${pid} ]] || continue
		kill -TERM "${pid}" 2> /dev/null
		wait "${pid}" 2> /dev/null
	done
	rmdir "${load_stats_pod}/${load_stats_a}/child" 2> /dev/null
	rmdir "${load_stats_pod}/${load_stats_a}" "${load_stats_pod}/${load_stats_b}" "${load_stats_pod}" 2> /dev/null
	[[ ${load_stats_created_root} -eq 0 ]] || rmdir /sys/fs/cgroup/kubepods 2> /dev/null
}
trap load_stats_cleanup EXIT

mkdir -p "${load_stats_meta}/docker/containers" "${load_stats_meta}/mask"
if [[ ! -d /sys/fs/cgroup/kubepods ]]; then
	mkdir /sys/fs/cgroup/kubepods
	load_stats_created_root=1
fi
mkdir "${load_stats_pod}"
printf '{"items":[]}\n' > "${load_stats_meta}/pods.json"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost \
	-keyout "${load_stats_meta}/key.pem" -out "${load_stats_meta}/cert.pem" > "${load_stats_meta}/openssl.log" 2>&1
python3 "${load_stats_fixture}" serve "${load_stats_meta}" > "${load_stats_meta}/server.log" 2>&1 &
load_stats_server_pid=$!
wait_until 10 .1 test -s "${load_stats_meta}/port" || fatal "metadata fixture did not start"
export DOCKER_HOST="unix://${load_stats_meta}/docker.sock"

load_stats_create_container() {
	local id=$1 name=$2
	mkdir "${load_stats_pod}/${id}"
	[[ -p "${load_stats_meta}/${name}.fifo" ]] || mkfifo "${load_stats_meta}/${name}.fifo"
	rm -f "${load_stats_meta}/${name}.ack"
	python3 "${load_stats_fixture}" worker "${load_stats_meta}/${name}.fifo" "${load_stats_meta}/${name}.ack" \
		> "${load_stats_meta}/${name}.log" 2>&1 &
	load_stats_new_pid=$!
	load_stats_workers+=("${load_stats_new_pid}")
	wait_until 10 .1 grep -qx ready "${load_stats_meta}/${name}.ack" || fatal "${name} worker not ready"
	printf '%s\n' "${load_stats_new_pid}" > "${load_stats_pod}/${id}/cgroup.procs"
	mkdir -p "${load_stats_meta}/docker/containers/${id}"
	printf '{"State":{"Pid":%s}}\n' "${load_stats_new_pid}" > "${load_stats_meta}/docker/containers/${id}/config.v2.json"
	jq -n --arg id "${id}" --arg uid "${load_stats_uid}" --arg name "${name}" \
		--arg started "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" '{
	 metadata:{name:"load-fixture",namespace:"default",uid:$uid},
	 spec:{containers:[{name:$name}]},
	 status:{phase:"Running",qosClass:"Guaranteed",containerStatuses:[{
	 name:$name,containerID:("docker://"+$id),state:{running:{startedAt:$started}}
	 }]}}' > "${load_stats_meta}/${name}.pod"
}

load_stats_publish() {
	jq -n --slurpfile a "${load_stats_meta}/load-a.pod" --slurpfile b "${load_stats_meta}/load-b.pod" \
		'{items:[($a[0] | .spec.containers += $b[0].spec.containers | .status.containerStatuses += $b[0].status.containerStatuses)]}' > "${load_stats_meta}/pods.next"
	mv "${load_stats_meta}/pods.next" "${load_stats_meta}/pods.json"
}

write_load_stats_lifecycle_config() {
	write_observability_config
	sed -i 's/"dload",//;s/"memory_free",//;s/HostThresholdLoad = 1$/HostThresholdLoad = 1000000/;s/ThresholdLoad = 100$/ThresholdLoad = 1000000/' "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF
[Pod]
    KubeletReadOnlyPort = 0
    KubeletAuthorizedPort = $(< "${load_stats_meta}/port")
    KubeletClientCertPath = "${load_stats_meta}/cert.pem,${load_stats_meta}/key.pem"
EOF
}

load_stats_metric() {
	local name=$1 key=${2:-} value=${3:-}
	awk -v name="${name}" -v key="${key}" -v value="${value}" '
	 index($0, name "{") == 1 && (key == "" ||
	 index($0, "{" key "=\"" value "\"") || index($0, "," key "=\"" value "\"")) {
	   count++; result=$NF
	 }
	 END {
	   if (count != 1 || result !~ /^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$/) exit 1
	   print result
	 }
	' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt"
}

load_stats_containers_ready() {
	huatuo_bamai_collect_metrics || return 1
	load_stats_metric huatuo_bamai_loadavg_container_load1 container_name load-a > /dev/null 2>&1 \
		&& load_stats_metric huatuo_bamai_loadavg_container_load1 container_name load-b > /dev/null 2>&1
}

assert_load_stats_idle() {
	local name=$1 value
	value=$(load_stats_metric huatuo_bamai_loadavg_container_load1 container_name "${name}")
	awk -v value="${value}" 'BEGIN { exit value != 0 }' || fatal "${name} inherited nonzero EMA: ${value}"
}

assert_load_stats_descendant_isolation() {
	local started now name state value
	read -r started _ < /proc/uptime
	while :; do
		huatuo_bamai_collect_metrics
		for name in load-a load-b; do
			for state in running uninterruptible; do
				value=$(load_stats_metric "huatuo_bamai_loadavg_container_nr_${state}" container_name "${name}")
				awk -v value="${value}" 'BEGIN { exit value != 0 }' \
					|| fatal "descendant workload leaked into ${name} ${state}"
			done
		done
		read -r now _ < /proc/uptime
		if awk -v now="${now}" -v started="${started}" 'BEGIN { exit now-started < 3 }'; then
			break
		fi
	done
}

load_stats_create_container "${load_stats_a}" load-a
load_stats_a_pid=${load_stats_new_pid}
load_stats_create_container "${load_stats_b}" load-b
load_stats_b_pid=${load_stats_new_pid}
load_stats_publish
integration_huatuo_bamai_start write_load_stats_lifecycle_config \
	--region integration --disable-storage --log-debug
wait_until 45 .2 load_stats_containers_ready || fatal "container EMA baselines not published"
assert_load_stats_idle load-a
assert_load_stats_idle load-b
assert_memsnapshot_tracing_metrics loadavg integration 1 3
assert_memsnapshot_tracing_metrics dload integration 1 3

# Descendant workers must not be charged to the directly attached parent or
# sibling task gauges. Observe several sample intervals, not a stale scrape.
mkdir "${load_stats_pod}/${load_stats_a}/child"
printf '%s\n' "${load_stats_a_pid}" > "${load_stats_pod}/${load_stats_a}/child/cgroup.procs"
printf 'busy\n' > "${load_stats_meta}/load-a.fifo"
wait_until 10 .1 grep -qx busy "${load_stats_meta}/load-a.ack" || fatal "descendant workload did not start"
assert_load_stats_descendant_isolation
printf 'idle\n' > "${load_stats_meta}/load-a.fifo"
wait_until 10 .1 grep -qx idle "${load_stats_meta}/load-a.ack" || fatal "descendant workload did not stop"
printf '%s\n' "${load_stats_a_pid}" > "${load_stats_pod}/${load_stats_a}/cgroup.procs"
rmdir "${load_stats_pod}/${load_stats_a}/child"

printf 'busy\n' > "${load_stats_meta}/load-a.fifo"
load_stats_ema_warm() {
	huatuo_bamai_collect_metrics || return 1
	local value
	value=$(load_stats_metric huatuo_bamai_loadavg_container_load1 container_name load-a) || return 1
	awk -v value="${value}" 'BEGIN { exit value <= .25 }'
}
wait_until 30 .2 load_stats_ema_warm || fatal "busy container EMA did not increase"
assert_load_stats_ema() {
	local one five fifteen
	one=$(load_stats_metric huatuo_bamai_loadavg_container_load1 container_name load-a)
	five=$(load_stats_metric huatuo_bamai_loadavg_container_load5 container_name load-a)
	fifteen=$(load_stats_metric huatuo_bamai_loadavg_container_load15 container_name load-a)
	awk -v one="${one}" -v five="${five}" -v fifteen="${fifteen}" \
		'BEGIN { exit !(0 < fifteen && fifteen < five && five < one && one < 3) }' \
		|| fatal "invalid container EMA windows: ${one}/${five}/${fifteen}"
}
assert_load_stats_ema
assert_load_stats_idle load-b
load_stats_before=$(load_stats_metric huatuo_bamai_loadavg_container_load1 container_name load-a)
read -r load_stats_started _ < /proc/uptime
for ((load_stats_scrape = 0; load_stats_scrape < 20; load_stats_scrape++)); do
	huatuo_bamai_collect_metrics
done
assert_load_stats_scrape_bound() {
	local current now
	current=$(load_stats_metric huatuo_bamai_loadavg_container_load1 container_name load-a)
	read -r now _ < /proc/uptime
	# Two seconds cover sampling phase and the last in-flight sample.
	awk -v current="${current}" -v before="${load_stats_before}" \
		-v now="${now}" -v started="${load_stats_started}" '
	 BEGIN { maximum=3+(before-3)*exp(-(now-started+2)/60); exit current > maximum+0.0001 }
	' || fatal "scrapes changed the EMA sampling cadence"
}
assert_load_stats_scrape_bound

# A missing cgroup must invalidate its cached sample without hiding its sibling.
mkdir -p "${load_stats_meta}/parent-mask/${load_stats_b}"
mount --bind "${load_stats_pod}/${load_stats_b}" "${load_stats_meta}/parent-mask/${load_stats_b}"
load_stats_proxy="${load_stats_meta}/parent-mask/${load_stats_b}"
mount --rbind "${load_stats_meta}/parent-mask" "${load_stats_pod}"
load_stats_mask=${load_stats_pod}
load_stats_gap_observed() {
	huatuo_bamai_collect_metrics || return 1
	! grep '^huatuo_bamai_loadavg_container_' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" | grep -q 'container_name="load-a"'
}
wait_until 10 .1 load_stats_gap_observed || fatal "missing container samples were exported as cached data or zero"
umount -R "${load_stats_mask}"
load_stats_mask=""
umount "${load_stats_proxy}"
load_stats_proxy=""
wait_until 10 .1 load_stats_containers_ready || fatal "EMA did not recover after missing samples"
assert_load_stats_gap_reset() {
	local value
	value=$(load_stats_metric huatuo_bamai_loadavg_container_load1 container_name load-a)
	awk -v value="${value}" 'BEGIN { exit value > .15 }' || fatal "EMA retained pre-gap history: ${value}"
}
assert_load_stats_gap_reset

# Daemon restart must drop EMA history while preserving the live container view.
printf 'idle\n' > "${load_stats_meta}/load-a.fifo"
wait_until 10 .1 grep -qx idle "${load_stats_meta}/load-a.ack" || fatal "worker did not become idle"
huatuo_bamai_stop_with_signal TERM
assert_memsnapshot_logs "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
integration_huatuo_bamai_start write_load_stats_lifecycle_config \
	--region integration --disable-storage --log-debug
wait_until 45 .2 load_stats_containers_ready || fatal "container collection did not recover after restart"
assert_load_stats_idle load-a
assert_load_stats_idle load-b

# Delete and recreate a target at the same cgroup path with a new init PID.
jq -n --slurpfile b "${load_stats_meta}/load-b.pod" '{items:[$b[0]]}' > "${load_stats_meta}/pods.next"
mv "${load_stats_meta}/pods.next" "${load_stats_meta}/pods.json"
kill -TERM "${load_stats_a_pid}"
wait "${load_stats_a_pid}"
for load_stats_worker_index in "${!load_stats_workers[@]}"; do
	if [[ ${load_stats_workers[load_stats_worker_index]} == "${load_stats_a_pid}" ]]; then
		unset 'load_stats_workers[load_stats_worker_index]'
		break
	fi
done
load_stats_a_pid=""
rmdir "${load_stats_pod}/${load_stats_a}"
load_stats_deleted() {
	huatuo_bamai_collect_metrics || return 1
	! grep '^huatuo_bamai_loadavg_container_' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" | grep -q 'container_name="load-a"'
}
wait_until 45 .2 load_stats_deleted || fatal "deleted container metrics remained cached"
load_stats_create_container "${load_stats_a}" load-a
load_stats_a_pid=${load_stats_new_pid}
load_stats_publish
wait_until 45 .2 load_stats_containers_ready || fatal "recreated container was not sampled"
assert_load_stats_idle load-a
assert_load_stats_idle load-b
huatuo_bamai_stop_with_signal TERM
# A creation hint can precede publication of this fixture's new PodStatus.
# Accept only that named target's bounded reconciliation warning; recovery
# above remains mandatory and all other warnings/errors still fail.
assert_load_stats_recreation_logs() {
	awk -v id="${load_stats_a}" '
	 /level="warning"/ && /msg="refresh container view"/ &&
	 /creation is not yet reflected by kubelet/ && index($0,id) { allowed++; next }
	 { print }
	 END { if (allowed > 3) exit 1 }
	' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" > "${HUATUO_BAMAI_TEST_TMPDIR}/recreation-checked.log" \
		|| fatal "container reconciliation warnings did not converge"
	assert_memsnapshot_logs "${HUATUO_BAMAI_TEST_TMPDIR}/recreation-checked.log"
}
assert_load_stats_recreation_logs
log_info "loadavg/dload coexistence, retry, EMA restart/recreation verified"
