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

# Exercise collector registration and Prometheus output after service startup.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

require_commands curl truncate losetup mkfs.xfs mount umount mountpoint grep awk readlink dd
if ! grep -qw xfs /proc/filesystems; then
	require_commands modprobe
	modprobe xfs || skip "XFS is unavailable in the running kernel"
fi
require_readable /sys/fs/xfs/stats/stats

readonly image="${HUATUO_BAMAI_TEST_TMPDIR}/xfs-hotplug.img"
readonly mount_dir="${HUATUO_BAMAI_TEST_TMPDIR}/xfs-mnt"
loop_device=""

cleanup() {
	local status=$? backing
	trap - EXIT
	huatuo_bamai_stop || status=1
	if mountpoint -q "${mount_dir}"; then
		umount -- "${mount_dir}" || status=1
	fi
	if [[ -n "${loop_device}" ]]; then
		backing=$(losetup -n -O BACK-FILE "${loop_device}") || status=1
		if [[ $(readlink -f -- "${backing}") == $(readlink -f -- "${image}") ]]; then
			losetup -d "${loop_device}" || status=1
		else
			log_error "refusing to detach ${loop_device}: unexpected backing file ${backing}"
			status=1
		fi
	fi
	if mountpoint -q "${mount_dir}" || [[ -n $(losetup -j "${image}") ]]; then
		log_error "XFS hotplug resources remain: ${mount_dir}, ${image}"
		status=1
	else
		rm -f -- "${image}" || status=1
		log_info "XFS hotplug cleanup: mount released, loop detached, image removed"
	fi
	exit "${status}"
}
trap cleanup EXIT

# Unrelated tracers can require unavailable hardware or kernel probes.
write_xfs_config() {
	cat > "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << 'EOF'
BlackList = ["arp", "ascend_npu", "cpu_stat", "cpu_util", "cpuidle", "cpusys", "diskio", "dload", "dropwatch", "hungtask", "iolatency", "iotracing", "irqtracing", "loadavg", "memburst", "memory_buddyinfo", "memory_events", "memory_free", "memory_others", "memory_reclaim", "memory_reclaim_events", "memory_vmstat", "memory_threshold_snapshot", "metax_gpu", "mountpoint_perm", "mthreads_gpu", "mthreads_xid", "net_conntrack", "net_rx_latency", "netdev", "netdev_bonding_lacp", "netdev_dcb", "netdev_events", "netdev_hw", "netdev_qdisc", "netdev_rdma_link", "netdev_txqueue_timeout", "netstat", "memory_oom_kill", "ras", "runqlat", "sched_tick", "sockstat", "softirq", "softlockup", "tcp_memory", "tcp_retransmit", "tracing_status"]

[HTTPServer.Auth]
    BearerToken = "integration-node-token"
EOF
}

xfs_metric_sample() {
	local metric=$1 metric_type=$2
	grep -Fxq "# TYPE huatuo_bamai_xfs_${metric} ${metric_type}" "${metrics_file}" \
		&& grep -Eq "^huatuo_bamai_xfs_${metric}\\{device=\"${device_name}\",host=\"huatuo-dev\",region=\"dev\"\\} [0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$" "${metrics_file}"
}

xfs_device_metrics() {
	awk -v device="${device_name}" \
		'$0 ~ /^huatuo_bamai_xfs_/ && index($0, "device=\"" device "\"") {print}' "${metrics_file}"
}

xfs_metrics_present() {
	huatuo_bamai_metrics > "${metrics_file}" || return 1
	local metric expected_count=7
	for metric in log_space_sleep_total alloc_blocks_total alloc_extents_total \
		inode_missed_total inode_attempts_total buf_busy_locked_total buf_locked_waited_total; do
		xfs_metric_sample "${metric}" counter || return 1
	done
	if [[ ${legacy_log} == true ]]; then
		xfs_metric_sample log_free_bytes gauge || return 1
		expected_count=8
	fi
	[[ $(xfs_device_metrics | wc -l) -eq ${expected_count} ]]
}

xfs_metrics_absent() {
	huatuo_bamai_metrics > "${metrics_file}" || return 1
	[[ -z $(xfs_device_metrics) ]]
}

truncate -s 512M "${image}"
loop_device=$(losetup --find --show "${image}") || skip "no free loop device is available"
[[ ${loop_device} =~ ^/dev/loop[0-9]+$ ]] || fatal "unexpected loop device: ${loop_device}"
readonly device_name=${loop_device##*/}
[[ $(readlink -f -- "$(losetup -n -O BACK-FILE "${loop_device}")") == $(readlink -f -- "${image}") ]] \
	|| fatal "loop device does not belong to this workspace: ${loop_device}"
format_options=()
usage=$(mkfs.xfs 2>&1 || true)
for feature in inobtcount bigtime; do
	[[ ${usage} != *"${feature}="* ]] || format_options+=(-m "${feature}=0")
done
for feature in nrext64 exchange; do
	[[ ${usage} != *"${feature}="* ]] || format_options+=(-i "${feature}=0")
done
[[ ${usage} != *"parent="* ]] || format_options+=(-n parent=0)
mkfs.xfs -q -f "${format_options[@]}" "${loop_device}"
mkdir "${mount_dir}"

# Explicit runtime arguments retain real /proc, /sys and /dev instead of fixtures.
integration_huatuo_bamai_start write_xfs_config \
	--region dev --disable-storage --disable-kubelet --log-debug
metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/xfs-before.txt"
wait_until 30 1 xfs_metrics_absent || fatal "unmounted device already has XFS metrics: ${device_name}"
log_info "XFS hotplug: service ready before mounting ${device_name}"

for round in 1 2; do
	mount -- "${loop_device}" "${mount_dir}"
	log_info "XFS hotplug: mounted ${device_name}, round ${round}"
	dd if=/dev/zero of="${mount_dir}/probe" bs=4096 count=1 conv=fsync status=none
	legacy_log=false
	if [[ -r /sys/fs/xfs/${device_name}/log/reserve_grant_head ]]; then
		legacy_log=true
		log_info "XFS log interface: legacy (eight metrics including log_free_bytes)"
	elif [[ -r /sys/fs/xfs/${device_name}/log/reserve_grant_head_bytes ]]; then
		# The separate new-grant ABI correction is tracked in #1243.
		log_info "XFS log interface: bytes (seven counters; log_free_bytes omitted)"
	else
		fatal "XFS log grant interface is unavailable: ${device_name}"
	fi
	metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/xfs-mounted-${round}.txt"
	wait_until 30 1 xfs_metrics_present || fatal "missing or invalid XFS metrics for mounted device: ${device_name}"
	grep '^# TYPE huatuo_bamai_xfs_' "${metrics_file}"
	xfs_device_metrics
	log_info "XFS hotplug: metric names, types and device/host/region labels verified, round ${round}"
	umount -- "${mount_dir}"
	metrics_file="${HUATUO_BAMAI_TEST_TMPDIR}/xfs-unmounted-${round}.txt"
	wait_until 30 1 xfs_metrics_absent || fatal "XFS device metrics survived unmount: ${device_name}"
	log_info "XFS hotplug: metrics removed after unmount, round ${round}"
done
