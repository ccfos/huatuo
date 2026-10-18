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

# Combine two real container readers and one unattributed cgroup reader. Kubelet
# metadata supplies distinct and shared labels for public aggregation checks.

set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
source "${ROOT_DIR}/integration/lib_io_container.sh"
source "${ROOT_DIR}/integration/lib_io_wait.sh"
CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
WAIT_HUATUO_BAMAI_TIMEOUT=30
readonly WAIT_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/container-waits"
readonly WAIT_HELPER="${ROOT_DIR}/integration/testdata/test_io_wait.py"
mkdir -p "${WAIT_WORK}"
require_commands python3 jq timeout
cgroup_root=/sys/fs/cgroup
control_prefix=io
parent=""
other_group=""
wait_groups=()
jobs=()
delegated=0
cost_enabled=0
controls_enabled=0
collector=""
device_number=""
disk_lock=""
passed=0
case ${TEST_IO_WAIT_COLLECTOR:-all} in
all) collectors=(iocost blk_throtl) ;;
blk_throtl | iocost) collectors=("${TEST_IO_WAIT_COLLECTOR}") ;;
*) fatal "TEST_IO_WAIT_COLLECTOR must be all, blk_throtl or iocost" ;;
esac

container_wait_control() {
	local enabled=$1 group value owner file status=0
	printf 'collector=%s controls_enabled=%s device=%s\n' \
		"${collector}" "${enabled}" "${device_number}" >> "${WAIT_WORK}/resources.txt"
	[[ ${enabled} != 1 ]] || controls_enabled=1
	if [[ ${collector} == blk_throtl ]]; then
		for group in "${wait_groups[@]}"; do
			if [[ ! -d ${group} ]]; then
				[[ ${enabled} == 0 ]] && continue
				return 1
			fi
			if [[ ${control_prefix} == io ]]; then
				file="${group}/io.max"
				value="${device_number} riops=$([[ ${enabled} == 1 ]] && echo 8 || echo max)"
			else
				file="${group}/blkio.throttle.read_iops_device"
				value="${device_number} $([[ ${enabled} == 1 ]] && echo 8 || echo 0)"
			fi
			if ! printf '%s\n' "${value}" > "${file}"; then
				[[ ${enabled} == 0 ]] || return 1
				status=1
				continue
			fi
			cat "${file}" >> "${WAIT_WORK}/${collector}-controls.txt" || status=1
			if [[ ${enabled} == 1 ]]; then
				if [[ ${control_prefix} == io ]]; then
					grep -Eq "^${device_number} .*riops=8([[:space:]]|$)" "${file}" || return 1
				else
					grep -Eq "^${device_number}[[:space:]]+8$" "${file}" || return 1
				fi
			fi
		done
	elif [[ ${collector} == iocost ]]; then
		((io_test_scsi_owned)) || return 1
		read -r owner < "/sys/block/${disk}/device/vendor"
		if [[ ${owner} != "${io_test_scsi_owner}" ]]; then
			log_error "IOCOST container test disk ownership changed"
			return 1
		fi
		if [[ ${enabled} == 1 ]]; then
			printf '%s ctrl=user model=linear rbps=32768 rseqiops=8 rrandiops=8 wbps=32768 wseqiops=8 wrandiops=8\n' \
				"${device_number}" > "${cgroup_root}/${control_prefix}.cost.model" || return 1
			cost_enabled=1
			printf '%s enable=1 ctrl=user rpct=0 rlat=0 wpct=0 wlat=0 min=100 max=100\n' \
				"${device_number}" > "${cgroup_root}/${control_prefix}.cost.qos" || return 1
			grep -Eq "^${device_number} .*rbps=32768([[:space:]]|$)" \
				"${cgroup_root}/${control_prefix}.cost.model" || return 1
			grep -Eq "^${device_number} .*enable=1([[:space:]]|$)" \
				"${cgroup_root}/${control_prefix}.cost.qos" || return 1
		else
			printf '%s enable=0\n' "${device_number}" > "${cgroup_root}/${control_prefix}.cost.qos" || return 1
			cost_enabled=0
		fi
		cat "${cgroup_root}/${control_prefix}.cost.model" "${cgroup_root}/${control_prefix}.cost.qos" \
			>> "${WAIT_WORK}/${collector}-controls.txt"
	fi
	((status == 0)) || return "${status}"
	[[ ${enabled} != 0 ]] || controls_enabled=0
}

clear_wait_controls() {
	if ((controls_enabled || cost_enabled)); then
		container_wait_control 0
	fi
}

cleanup_container_waits() {
	local status=$? clean=1 job
	trap - EXIT
	trap '' INT TERM
	set +e
	clear_wait_controls || clean=0
	for job in "${jobs[@]}"; do
		kill -TERM "${job}" 2> /dev/null || true
	done
	huatuo_bamai_stop || clean=0
	io_container_cleanup || clean=0
	for job in "${jobs[@]}"; do
		# TERM is expected during cancellation. The exit status of the stopped
		# workload is not a resource leak; an unreaped live process is.
		stop_and_wait_by_pid "${job}" 3 || true
		if kill -0 "${job}" 2> /dev/null; then
			log_error "container wait cleanup left process ${job}; inspect resources.txt"
			clean=0
		fi
	done
	io_test_remove_cgroup "${other_group}" || clean=0
	if [[ -n ${disk_lock} ]]; then
		exec {disk_lock}<&-
	fi
	io_test_remove_cgroup "${parent}" || clean=0
	if ((delegated && clean)); then
		printf '%s\n' -io > "${cgroup_root}/cgroup.subtree_control" || clean=0
	fi
	if ((clean)); then
		io_test_cleanup_disks || clean=0
	fi
	if ((clean == 0)); then
		log_error "container wait cleanup incomplete; inspect ${WAIT_WORK}/resources.txt and ${CONTAINER_WORK}/resources.txt"
		status=1
	fi
	exit "${status}"
}
trap cleanup_container_waits EXIT
trap 'log_error "container wait test interrupted; entering cleanup"; exit 130' INT
trap 'log_error "container wait test terminated or timed out; entering cleanup"; exit 143' TERM

io_container_prepare
if [[ -f ${cgroup_root}/cgroup.controllers ]]; then
	grep -qw io "${cgroup_root}/cgroup.controllers" || skip "container waits require the cgroup v2 IO controller"
	if ! grep -qw io "${cgroup_root}/cgroup.subtree_control"; then
		delegated=1
		printf 'root_io_delegated=1\n' >> "${WAIT_WORK}/resources.txt"
		printf '%s\n' +io > "${cgroup_root}/cgroup.subtree_control"
	fi
else
	cgroup_root=/sys/fs/cgroup/blkio
	control_prefix=blkio
	[[ -d ${cgroup_root} ]] || skip "container waits require the blkio controller"
fi
read -r owner < /proc/sys/kernel/random/uuid
parent="${cgroup_root}/huatuo-io-container-waits-${owner}"
other_group="${parent}/other"
printf 'parent=%s\nother_group=%s\n' "${parent}" "${other_group}" >> "${WAIT_WORK}/resources.txt"
mkdir "${parent}"
if [[ ${control_prefix} == io ]]; then
	printf '%s\n' +io > "${parent}/cgroup.subtree_control"
fi
mkdir "${other_group}"
wait_groups=("${other_group}")
io_test_prepare_disks 1
trap 'log_error "container wait test interrupted; entering cleanup"; exit 130' INT
trap 'log_error "container wait test terminated or timed out; entering cleanup"; exit 143' TERM
((${#io_test_disks[@]})) || skip "container waits require an eligible block device"
disk=${io_test_disks[0]}
device="/dev/${disk}"
if ((io_test_scsi_owned)); then
	require_commands flock
	# Device-wide IOCOST limits also affect udev's background probes.
	# Hold the canonical node lock until controls and workloads are stopped.
	exec {disk_lock}< "${device}"
	flock -x -w 10 "${disk_lock}" || fatal "cannot lock owned IO test disk ${device}"
fi
read -r device_number < "/sys/block/${disk}/dev"
[[ ${device_number} =~ ^[0-9]+:[0-9]+$ ]] || fatal "invalid public device number: ${device_number}"
for index in 0 1; do
	if [[ ${control_prefix} == io ]]; then
		start_io_container "${index}" "/${parent##*/}/${container_ids[index]}"
	else
		start_io_container "${index}"
	fi
	pid=$(container_ctr tasks list | awk -v id="${container_ids[index]}" '$1 == id && $3 == "RUNNING" { print $2 }')
	[[ ${pid} =~ ^[0-9]+$ ]] || fatal "container ${container_ids[index]} is not running"
	group=""
	while IFS=: read -r hierarchy controllers path; do
		if [[ (${control_prefix} == io && ${hierarchy} == 0) ||
			(${control_prefix} == blkio && ,${controllers}, == *,blkio,*) ]]; then
			group="${cgroup_root}${path}"
		fi
	done < "/proc/${pid}/cgroup"
	[[ ${group} == "${cgroup_root}/"*"/${container_ids[index]}" && -d ${group} ]] \
		|| skip "cannot identify an owned runtime IO cgroup for ${container_ids[index]}"
	wait_groups+=("${group}")
	printf 'container=%s pid=%s cgroup=%s\n' "${container_ids[index]}" "${pid}" "${group}" >> "${WAIT_WORK}/resources.txt"
done
write_container_pods 2
io_container_start_kubelet

write_container_wait_config() {
	write_sched_tick_config
	sed "s/\"${collector}\"/\"sched_tick\"/" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" \
		> "${WAIT_WORK}/config.next"
	mv "${WAIT_WORK}/config.next" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf"
	io_container_write_pod_config
}

run_container_wait_workload() {
	local output=$1 container=$2 started finished uptime_idle command_pid="" status=0
	trap '
		trap "" INT TERM
		if [[ -n ${command_pid} ]]; then
			kill -TERM "${command_pid}" 2> /dev/null || true
			wait "${command_pid}" 2> /dev/null || true
		fi
		exit 143
	' INT TERM
	read -r started uptime_idle < /proc/uptime
	TMPDIR="${runtime_work}" timeout --kill-after=2 20 \
		ctr --address /run/containerd/containerd.sock --namespace k8s.io tasks exec \
		--exec-id "read-${BASHPID}-${RANDOM}" --fifo-dir "${runtime_work}/fifos" \
		"${container}" /bin/dd "if=${device}" of=/dev/null bs=4096 count=8 iflag=direct &
	command_pid=$!
	wait "${command_pid}" || status=$?
	command_pid=""
	trap - INT TERM
	((status == 0)) || return "${status}"
	read -r finished uptime_idle < /proc/uptime
	jq -n --argjson started "${started}" --argjson finished "${finished}" \
		'{requests: 8, operation: "read", seconds: ($finished - $started)}' > "${output}"
}

start_wait_jobs() {
	local directory=$1 index
	jobs=()
	mkdir -p "${directory}"
	timeout --kill-after=2 25 python3 "${WAIT_HELPER}" workload \
		--device "${device}" --cgroup "${other_group}" --operation read --requests 8 \
		--output "${directory}/other.json" > "${directory}/other.log" 2>&1 &
	jobs+=("$!")
	for index in 0 1; do
		run_container_wait_workload "${directory}/${index}.json" "${container_ids[index]}" \
			> "${directory}/${index}.log" 2>&1 &
		jobs+=("$!")
	done
	printf 'jobs=%s output=%s\n' "${jobs[*]}" "${directory}" >> "${WAIT_WORK}/resources.txt"
}

finish_wait_jobs() {
	local job status=0
	for job in "${jobs[@]}"; do
		wait "${job}" || status=1
	done
	jobs=()
	((status == 0)) || fatal "container wait workload failed; inspect its per-reader logs"
}

assert_container_wait_ready() {
	local phase=$1 expected=$2 deadline=$((SECONDS + 30)) attempt=0 directory status
	while true; do
		attempt=$((attempt + 1))
		directory="${WAIT_WORK}/${collector}/${phase}-warmup-${attempt}"
		mkdir -p "${directory}"
		huatuo_bamai_metrics > "${directory}/before.metrics" || fatal "wait startup baseline scrape failed"
		start_wait_jobs "${directory}"
		finish_wait_jobs
		huatuo_bamai_metrics > "${directory}/after.metrics" || fatal "wait startup scrape failed"
		status=0
		assert_io_wait_metrics --collector "${collector}" --disk "${disk}" --operation read \
			--warmup --positive-windows 1 --container-groups "${expected}" --namespace "${POD_NAMESPACE}" \
			--require-other "${directory}/after.metrics" > "${directory}/summary.json" || status=$?
		((status != 0)) || return 0
		((status == 3)) || fatal "invalid ${collector}/${phase} output during startup qualification"
		((SECONDS < deadline)) || fatal "${collector}/${phase} did not produce healthy Host, Other and container waits before startup deadline"
	done
}

assert_container_wait_phase() {
	local phase=$1 expected=$2 directory="${WAIT_WORK}/${collector}/$1" sample sample_path participant timing_status unconfirmed=0
	huatuo_bamai_metrics > "${WAIT_WORK}/${collector}/before-${phase}.metrics" || fatal "wait baseline scrape failed"
	start_wait_jobs "${directory}"
	for ((sample = 0; sample < 52; sample++)); do
		printf -v sample_path '%s/sample-%03d.metrics' "${directory}" "${sample}"
		huatuo_bamai_metrics > "${sample_path}" || fatal "container wait scrape failed"
		[[ -s ${directory}/0.json && -s ${directory}/1.json && -s ${directory}/other.json ]] && break
		sleep 0.5
	done
	finish_wait_jobs
	huatuo_bamai_metrics > "${directory}/sample-final.metrics" || fatal "final container wait scrape failed"
	for participant in 0 1 other; do
		timing_status=0
		assert_io_wait_slowdown "${WAIT_WORK}/${collector}/baseline/${participant}.json" \
			"${directory}/${participant}.json" > "${directory}/timing-${participant}.json" || timing_status=$?
		if ((timing_status == 3)); then
			unconfirmed=1
		elif ((timing_status != 0)); then
			fatal "invalid workload evidence for ${collector}/${phase}/${participant}"
		fi
	done
	if ((unconfirmed)); then
		log_info "SKIP ${collector}/${phase}: external slowdown was not confirmed for every reader"
		return 77
	fi
	assert_io_wait_metrics --collector "${collector}" --disk "${disk}" --operation read \
		--positive-windows 1 --container-groups "${expected}" --namespace "${POD_NAMESPACE}" \
		--require-other "${directory}"/sample-*.metrics > "${directory}/summary.json" \
		|| fatal "confirmed ${collector}/${phase} waits violate public container aggregation"
}

for collector in "${collectors[@]}"; do
	if [[ ${collector} == iocost ]]; then
		if ((io_test_scsi_owned == 0)) || [[ $(uname -m) != x86_64 ||
		! -w ${cgroup_root}/${control_prefix}.cost.model || ! -w ${cgroup_root}/${control_prefix}.cost.qos ||
		(-r /sys/kernel/realtime && $(< /sys/kernel/realtime) == 1) ]]; then
			log_info "SKIP IOCOST container waits: owned disk and non-RT x86_64 cost controller are required"
			continue
		fi
	else
		available=1
		for group in "${wait_groups[@]}"; do
			[[ -w ${group}/io.max || -w ${group}/blkio.throttle.read_iops_device ]] || available=0
		done
		if ((available == 0)); then
			log_info "SKIP blk_throtl container waits: runtime IO cgroups lack throttle controls"
			continue
		fi
	fi
	mkdir -p "${WAIT_WORK}/${collector}"
	write_container_pods 2 distinct
	SSL_CERT_FILE="${CONTAINER_WORK}/kubelet.crt" \
		integration_huatuo_bamai_start write_container_wait_config --region dev --disable-storage --log-debug
	for index in 0 1; do
		wait_until 30 1 assert_container_metadata_ready "${index}" || fatal "Huatuo did not recognize the real wait container"
	done
	container_wait_control 0 || fatal "cannot disable owned IO controls for the baseline"
	start_wait_jobs "${WAIT_WORK}/${collector}/baseline"
	finish_wait_jobs
	mode_complete=1
	for phase in distinct shared; do
		expected=2
		if [[ ${phase} == shared ]]; then
			# Label aggregation needs the two public label sets, not repeated
			# waits for the common metadata cache to expire and refill.
			huatuo_bamai_stop
			write_container_pods 2 shared
			SSL_CERT_FILE="${CONTAINER_WORK}/kubelet.crt" \
				integration_huatuo_bamai_start write_container_wait_config --region dev --disable-storage --log-debug
			for index in 0 1; do
				wait_until 30 1 assert_container_metadata_ready "${index}" || fatal "shared-label wait container was not recognized"
			done
			expected=1
		fi
		if ! container_wait_control 1; then
			log_info "SKIP ${collector} container waits: controller settings could not be established and read back"
			mode_complete=0
			break
		fi
		assert_container_wait_ready "${phase}" "${expected}"
		phase_status=0
		assert_container_wait_phase "${phase}" "${expected}" || phase_status=$?
		container_wait_control 0 || fatal "cannot restore IO controls after ${phase}"
		if ((phase_status == 77)); then
			mode_complete=0
			break
		fi
		((phase_status == 0)) || fatal "container wait phase failed: ${collector}/${phase}"
		log_info "PASS ${collector}/${phase}: Host=Other+containers with ${expected} public container groups"
	done
	container_wait_control 0 || fatal "cannot restore owned IO controls"
	huatuo_bamai_stop
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${WAIT_WORK}/${collector}/huatuo.log"
	if ((mode_complete)); then
		passed=$((passed + 1))
		log_info "W04/W05 ${collector}: distinct/shared labels and Other aggregation verified"
	fi
done
((passed)) || skip "no container waiting mode established all required external workloads"
