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

# Combine two Kubernetes readers and one unattributed cgroup reader. Distinct
# and shared Pod hostnames exercise public aggregation through real discovery.

set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
io_test_isolate_run_directory
source "${ROOT_DIR}/integration/lib_io_container.sh"
source "${ROOT_DIR}/integration/lib_io_wait.sh"
CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
WAIT_HUATUO_BAMAI_TIMEOUT=30
readonly WAIT_WORK="${HUATUO_BAMAI_TEST_TMPDIR}/container-waits"
readonly WAIT_HELPER="${ROOT_DIR}/integration/testdata/test_io_wait.py"
mkdir -p "${WAIT_WORK}"
require_commands python3 jq timeout
container_wait_cgroup_root=/sys/fs/cgroup
container_wait_control_prefix=io
container_wait_parent=""
container_wait_other_group=""
container_wait_groups=()
container_wait_jobs=()
container_wait_delegated=0
container_wait_cost_enabled=0
container_wait_controls_enabled=0
container_wait_collector=""
container_wait_device_number=""
container_wait_disk_lock=""
passed=0
container_wait_ready_dir=""
case ${TEST_IO_WAIT_COLLECTOR:-all} in
all) collectors=(iocost blk_throtl) ;;
blk_throtl | iocost) collectors=("${TEST_IO_WAIT_COLLECTOR}") ;;
*) fatal "TEST_IO_WAIT_COLLECTOR must be all, blk_throtl or iocost" ;;
esac

container_wait_control() {
	local enabled=$1 group value owner file status=0
	printf 'collector=%s controls_enabled=%s device=%s\n' \
		"${container_wait_collector}" "${enabled}" "${container_wait_device_number}" >> "${WAIT_WORK}/resources.txt"
	[[ ${enabled} != 1 ]] || container_wait_controls_enabled=1
	if [[ ${container_wait_collector} == blk_throtl ]]; then
		for group in "${container_wait_groups[@]}"; do
			if [[ ! -d ${group} ]]; then
				[[ ${enabled} == 0 ]] && continue
				return 1
			fi
			if [[ ${container_wait_control_prefix} == io ]]; then
				file="${group}/io.max"
				value="${container_wait_device_number} riops=$([[ ${enabled} == 1 ]] && echo 8 || echo max)"
			else
				file="${group}/blkio.throttle.read_iops_device"
				value="${container_wait_device_number} $([[ ${enabled} == 1 ]] && echo 8 || echo 0)"
			fi
			if ! printf '%s\n' "${value}" > "${file}"; then
				[[ ${enabled} == 0 ]] || return 1
				status=1
				continue
			fi
			cat "${file}" >> "${WAIT_WORK}/${container_wait_collector}-controls.txt" || status=1
			if [[ ${enabled} == 1 ]]; then
				if [[ ${container_wait_control_prefix} == io ]]; then
					grep -Eq "^${container_wait_device_number} .*riops=8([[:space:]]|$)" "${file}" || return 1
				else
					grep -Eq "^${container_wait_device_number}[[:space:]]+8$" "${file}" || return 1
				fi
			fi
		done
	elif [[ ${container_wait_collector} == iocost ]]; then
		assert_io_test_disk_identity "${container_wait_disk}" || return 1
		io_test_save_cost_settings "${container_wait_cgroup_root}" "${container_wait_control_prefix}" "${container_wait_device_number}" || return 1
		if [[ ${enabled} == 1 ]]; then
			printf '%s ctrl=user model=linear rbps=32768 rseqiops=8 rrandiops=8 wbps=32768 wseqiops=8 wrandiops=8\n' \
				"${container_wait_device_number}" > "${container_wait_cgroup_root}/${container_wait_control_prefix}.cost.model" || return 1
			container_wait_cost_enabled=1
			printf '%s enable=1 ctrl=user rpct=0 rlat=0 wpct=0 wlat=0 min=100 max=100\n' \
				"${container_wait_device_number}" > "${container_wait_cgroup_root}/${container_wait_control_prefix}.cost.qos" || return 1
			grep -Eq "^${container_wait_device_number} .*rbps=32768([[:space:]]|$)" \
				"${container_wait_cgroup_root}/${container_wait_control_prefix}.cost.model" || return 1
			grep -Eq "^${container_wait_device_number} .*enable=1([[:space:]]|$)" \
				"${container_wait_cgroup_root}/${container_wait_control_prefix}.cost.qos" || return 1
		else
			printf '%s enable=0\n' "${container_wait_device_number}" > "${container_wait_cgroup_root}/${container_wait_control_prefix}.cost.qos" || return 1
			container_wait_cost_enabled=0
		fi
		cat "${container_wait_cgroup_root}/${container_wait_control_prefix}.cost.model" "${container_wait_cgroup_root}/${container_wait_control_prefix}.cost.qos" \
			>> "${WAIT_WORK}/${container_wait_collector}-controls.txt"
	fi
	((status == 0)) || return "${status}"
	[[ ${enabled} != 0 ]] || container_wait_controls_enabled=0
}

clear_wait_controls() {
	if ((container_wait_controls_enabled || container_wait_cost_enabled)); then
		container_wait_control 0
	fi
}

capture_container_wait_diagnostics() {
	local directory="${WAIT_WORK}/failure" index group file
	mkdir -p "${directory}" || return 1
	log_error "container wait failure diagnostics: ${directory}"
	io_container_report_failure
	{
		date -u
		printf 'collector=%s phase=%s device=%s cgroup_root=%s namespace=%s\n' \
			"${container_wait_collector}" "${container_wait_phase:-}" "${container_wait_device_number}" "${container_wait_cgroup_root}" "${POD_NAMESPACE:-}"
	} > "${directory}/context.txt" 2>&1
	for index in "${!container_wait_groups[@]}"; do
		group=${container_wait_groups[index]}
		{
			printf 'cgroup=%s\n' "${group}"
			# Reading the CSS notification file would change the failure state.
			for file in cgroup.procs io.stat io.max io.weight \
				blkio.weight blkio.throttle.read_iops_device blkio.throttle.io_service_bytes; do
				[[ -f ${group}/${file} ]] || continue
				printf '\n%s\n' "${file}"
				cat "${group}/${file}"
			done
		} > "${directory}/cgroup-${index}.txt" 2>&1
	done
}

cleanup_container_waits() {
	local status=$? clean=1 job
	trap - EXIT
	trap '' INT TERM
	set +e
	if ((status != 0 && status != 77 && status != 130 && status != 143)); then
		capture_container_wait_diagnostics || log_error "could not save all container wait failure diagnostics"
	fi
	clear_wait_controls || clean=0
	for job in "${container_wait_jobs[@]}"; do
		kill -TERM "${job}" 2> /dev/null || true
	done
	huatuo_bamai_stop || clean=0
	io_container_cleanup || clean=0
	for job in "${container_wait_jobs[@]}"; do
		# TERM is expected during cancellation. The exit status of the stopped
		# workload is not a resource leak; an unreaped live process is.
		stop_and_wait_by_pid "${job}" 3 || true
		if kill -0 "${job}" 2> /dev/null; then
			log_error "container wait cleanup left process ${job}; inspect resources.txt"
			clean=0
		fi
	done
	io_test_remove_cgroup "${container_wait_other_group}" || clean=0
	if [[ -n ${container_wait_disk_lock} ]]; then
		exec {container_wait_disk_lock}<&-
	fi
	io_test_remove_cgroup "${container_wait_parent}" || clean=0
	if ((container_wait_delegated && clean)); then
		printf '%s\n' -io > "${container_wait_cgroup_root}/cgroup.subtree_control" || clean=0
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
if [[ -f ${container_wait_cgroup_root}/cgroup.controllers ]]; then
	grep -qw io "${container_wait_cgroup_root}/cgroup.controllers" || skip "container waits require the cgroup v2 IO controller"
	if ! grep -qw io "${container_wait_cgroup_root}/cgroup.subtree_control"; then
		container_wait_delegated=1
		printf 'root_io_delegated=1\n' >> "${WAIT_WORK}/resources.txt"
		printf '%s\n' +io > "${container_wait_cgroup_root}/cgroup.subtree_control"
	fi
else
	container_wait_cgroup_root=/sys/fs/cgroup/blkio
	container_wait_control_prefix=blkio
	[[ -d ${container_wait_cgroup_root} ]] || skip "container waits require the blkio controller"
fi
read -r owner < /proc/sys/kernel/random/uuid
container_wait_parent="${container_wait_cgroup_root}/huatuo-io-container-waits-${owner}"
container_wait_other_group="${container_wait_parent}/other"
printf 'parent=%s\nother_group=%s\n' "${container_wait_parent}" "${container_wait_other_group}" >> "${WAIT_WORK}/resources.txt"
mkdir "${container_wait_parent}"
if [[ ${container_wait_control_prefix} == io ]]; then
	printf '%s\n' +io > "${container_wait_parent}/cgroup.subtree_control"
fi
mkdir "${container_wait_other_group}"
container_wait_groups=("${container_wait_other_group}")
io_test_prepare_disks
trap 'log_error "container wait test interrupted; entering cleanup"; exit 130' INT
trap 'log_error "container wait test terminated or timed out; entering cleanup"; exit 143' TERM
container_wait_disk=${io_test_disks[0]}
container_device="/dev/${container_wait_disk}"
if ((io_test_disk_exclusive)); then
	require_commands flock
	# Device-wide IOCOST limits also affect udev's background probes.
	# Hold the canonical node lock until controls and workloads are stopped.
	exec {container_wait_disk_lock}< "${container_device}"
	flock -x -w 10 "${container_wait_disk_lock}" || fatal "cannot lock owned IO test disk ${container_device}"
fi
read -r container_wait_device_number < "/sys/block/${container_wait_disk}/dev"
[[ ${container_wait_device_number} =~ ^[0-9]+:[0-9]+$ ]] || fatal "invalid public device number: ${container_wait_device_number}"
for index in 0 1; do start_io_container "${index}"; done

container_wait_refresh_groups() {
	local index pid group hierarchy controllers path
	container_wait_groups=("${container_wait_other_group}")
	for index in 0 1; do
		pid=${container_pids[index]}
		group=""
		while IFS=: read -r hierarchy controllers path; do
			if [[ (${container_wait_control_prefix} == io && ${hierarchy} == 0) ||
				(${container_wait_control_prefix} == blkio && ,${controllers}, == *,blkio,*) ]]; then
				group="${container_wait_cgroup_root}${path}"
			fi
		done < "/proc/${pid}/cgroup"
		[[ ${group} == "${container_wait_cgroup_root}/"* && -d ${group} ]] \
			|| fatal "cannot identify an owned Pod IO cgroup for ${container_ids[index]}"
		container_wait_groups+=("${group}")
		printf 'container=%s pid=%s cgroup=%s\n' "${container_ids[index]}" "${pid}" "${group}" >> "${WAIT_WORK}/resources.txt"
	done
}
container_wait_refresh_groups

write_config_bamai_io_container_wait() {
	write_config_bamai_sched_tick | sed "s/\"${container_wait_collector}\"/\"sched_tick\"/"
	write_config_bamai_kubelet 10250 "${KUBELET_CERT}" "${KUBELET_KEY}"
}

run_container_wait_workload() {
	local output=$1 index=$2 started finished uptime_idle command_pid="" status=0
	trap '
		trap "" INT TERM
		if [[ -n ${command_pid} ]]; then
			kill -TERM "${command_pid}" 2> /dev/null || true
			wait "${command_pid}" 2> /dev/null || true
		fi
		exit 143
	' INT TERM
	read -r started uptime_idle < /proc/uptime
	timeout --kill-after=2 20 kubectl --request-timeout=10s -n "${POD_NAMESPACE}" \
		exec "${container_pods[index]}" -- /bin/dd if=/io-device of=/dev/null bs=4096 count=8 iflag=direct &
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
	container_wait_jobs=()
	mkdir -p "${directory}"
	timeout --kill-after=2 25 python3 "${WAIT_HELPER}" workload \
		--device "${container_device}" --cgroup "${container_wait_other_group}" --operation read --requests 8 \
		--output "${directory}/other.json" > "${directory}/other.log" 2>&1 &
	container_wait_jobs+=("$!")
	for index in 0 1; do
		run_container_wait_workload "${directory}/${index}.json" "${index}" \
			> "${directory}/${index}.log" 2>&1 &
		container_wait_jobs+=("$!")
	done
	printf 'jobs=%s output=%s\n' "${container_wait_jobs[*]}" "${directory}" >> "${WAIT_WORK}/resources.txt"
}

assert_container_wait_jobs_completed() {
	local job status=0
	for job in "${container_wait_jobs[@]}"; do
		wait "${job}" || status=1
	done
	container_wait_jobs=()
	((status == 0)) || fatal "container wait workload failed; inspect its per-reader logs"
}

assert_container_wait_ready() {
	local container_wait_phase=$1 expected=$2 deadline=$((SECONDS + 30)) attempt=0 directory status
	io_container_phase="${container_wait_collector}/${container_wait_phase}"
	container_wait_ready_dir=""
	while true; do
		attempt=$((attempt + 1))
		directory="${WAIT_WORK}/${container_wait_collector}/${container_wait_phase}-warmup-${attempt}"
		mkdir -p "${directory}"
		huatuo_bamai_metrics > "${directory}/before.metrics" || fatal "wait startup baseline scrape failed"
		start_wait_jobs "${directory}"
		assert_container_wait_jobs_completed
		huatuo_bamai_metrics > "${directory}/after.metrics" || fatal "wait startup scrape failed"
		status=0
		assert_io_wait_metrics --collector "${container_wait_collector}" --disk "${container_wait_disk}" --operation read \
			--warmup --positive-windows 1 --container-groups "${expected}" --namespace "${POD_NAMESPACE}" \
			--require-other "${directory}/after.metrics" > "${directory}/summary.json" || status=$?
		if ((status == 0)); then
			container_wait_ready_dir=${directory}
			return 0
		fi
		((status == 3)) || fatal "invalid ${container_wait_collector}/${container_wait_phase} output during startup qualification"
		((SECONDS < deadline)) || fatal "${container_wait_collector}/${container_wait_phase} did not produce healthy Host, Other and container waits before startup deadline"
	done
}

assert_container_wait_inputs() {
	local container_wait_phase=$1 directory=${container_wait_ready_dir} participant timing_status unconfirmed=0
	io_container_phase="${container_wait_collector}/${container_wait_phase}"
	# The successful startup batch already has fresh IO, timing receipts and a
	# positive public interval for every required owner. Reuse that same window.
	[[ -n ${directory} && -s ${directory}/after.metrics ]] || fatal "container waits lack a qualified measurement window"
	for participant in 0 1 other; do
		timing_status=0
		assert_io_wait_slowdown "${WAIT_WORK}/${container_wait_collector}/baseline/${participant}.json" \
			"${directory}/${participant}.json" > "${directory}/timing-${participant}.json" || timing_status=$?
		if ((timing_status == 3)); then
			unconfirmed=1
		elif ((timing_status != 0)); then
			fatal "invalid workload evidence for ${container_wait_collector}/${container_wait_phase}/${participant}"
		fi
	done
	if ((unconfirmed)); then
		log_info "SKIP ${container_wait_collector}/${container_wait_phase}: external slowdown was not confirmed for every reader"
		return 77
	fi
}

for container_wait_collector in "${collectors[@]}"; do
	io_container_set_labels distinct
	container_wait_refresh_groups
	if [[ ${container_wait_collector} == iocost ]]; then
		if ((io_test_disk_exclusive == 0)) || [[ ! -w ${container_wait_cgroup_root}/${container_wait_control_prefix}.cost.model ||
			! -w ${container_wait_cgroup_root}/${container_wait_control_prefix}.cost.qos ||
			(-r /sys/kernel/realtime && $(< /sys/kernel/realtime) == 1) ]]; then
			log_info "SKIP IOCOST container waits: owned disk, cost controller and non-RT kernel are required"
			continue
		fi
	else
		available=1
		for group in "${container_wait_groups[@]}"; do
			[[ -w ${group}/io.max || -w ${group}/blkio.throttle.read_iops_device ]] || available=0
		done
		if ((available == 0)); then
			log_info "SKIP blk_throtl container waits: runtime IO cgroups lack throttle controls"
			continue
		fi
	fi
	mkdir -p "${WAIT_WORK}/${container_wait_collector}"
	io_test_start_bamai write_config_bamai_io_container_wait --region dev --disable-storage --log-debug
	assert_container_io_ready "${container_wait_collector}/distinct" 0 1
	container_wait_control 0 || fatal "cannot disable owned IO controls for the baseline"
	start_wait_jobs "${WAIT_WORK}/${container_wait_collector}/baseline"
	assert_container_wait_jobs_completed
	mode_complete=1
	for container_wait_phase in distinct shared; do
		expected=2
		if [[ ${container_wait_phase} == shared ]]; then
			huatuo_bamai_stop
			io_container_set_labels shared
			container_wait_refresh_groups
			io_test_start_bamai write_config_bamai_io_container_wait --region dev --disable-storage --log-debug
			assert_container_io_ready "${container_wait_collector}/shared" 0 1
			expected=1
		fi
		if ! container_wait_control 1; then
			log_info "SKIP ${container_wait_collector} container waits: controller settings could not be established and read back"
			mode_complete=0
			break
		fi
		assert_container_wait_ready "${container_wait_phase}" "${expected}"
		phase_status=0
		assert_container_wait_inputs "${container_wait_phase}" || phase_status=$?
		container_wait_control 0 || fatal "cannot restore IO controls after ${container_wait_phase}"
		if ((phase_status == 77)); then
			mode_complete=0
			break
		fi
		((phase_status == 0)) || fatal "container wait phase failed: ${container_wait_collector}/${container_wait_phase}"
		log_info "PASS ${container_wait_collector}/${container_wait_phase}: Host=Other+containers with ${expected} public container groups"
	done
	container_wait_control 0 || fatal "cannot restore owned IO controls"
	huatuo_bamai_stop
	cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${WAIT_WORK}/${container_wait_collector}/huatuo.log"
	if ((mode_complete)); then
		passed=$((passed + 1))
		log_info "W04/W05 ${container_wait_collector}: distinct/shared labels and Other aggregation verified"
	fi
done
((passed)) || skip "no container waiting mode established all required external workloads"
