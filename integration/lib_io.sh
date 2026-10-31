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

# Prefer owned SCSI disks for IO tests. Existing disks are read-only fallbacks;
# only disks created here may have their configuration changed or be removed.

io_test_disks=()
io_test_scsi_owned=0
io_test_scsi_owner=""

io_test_scsi_disks_ready() {
	local count=$1 block model
	io_test_disks=()
	for block in /sys/block/sd*; do
		[[ -r ${block}/device/model && -b /dev/${block##*/} ]] || continue
		read -r model < "${block}/device/model"
		[[ ${model} == scsi_debug ]] && io_test_disks+=("${block##*/}")
	done
	((${#io_test_disks[@]} == count))
}

io_test_prepare_disks() {
	local count=$1 block name sectors mounts reads writes owner load_status=0 signal_status=0
	io_test_disks=()
	io_test_scsi_owned=0
	io_test_scsi_owner=""
	unset TEST_IOLATENCY_IO_DEVICE TEST_IOLATENCY_IO_EXCLUSIVE

	if [[ ! -e /sys/module/scsi_debug ]] \
		&& command -v modprobe > /dev/null \
		&& command -v modinfo > /dev/null \
		&& modinfo scsi_debug > /dev/null 2>&1; then
		require_commands timeout setsid
		read -r owner < /proc/sys/kernel/random/uuid || fatal "cannot create scsi_debug ownership marker"
		io_test_scsi_owner=${owner%%-*}
		[[ ${io_test_scsi_owner} =~ ^[[:xdigit:]]{8}$ ]] || fatal "invalid scsi_debug ownership marker"
		# Record ownership before allowing interruption to run caller cleanup.
		trap 'signal_status=130' INT
		trap 'signal_status=143' TERM
		setsid timeout 10s modprobe --first-time scsi_debug \
			dev_size_mb=128 add_host="${count}" inq_vendor="${io_test_scsi_owner}" || load_status=$?
		# A timeout can follow kernel insertion; claim only our marked module.
		if [[ -r /sys/module/scsi_debug/parameters/inq_vendor ]] \
			&& read -r owner < /sys/module/scsi_debug/parameters/inq_vendor \
			&& [[ ${owner} == "${io_test_scsi_owner}" ]]; then
			io_test_scsi_owned=1
		fi
		trap 'exit 130' INT
		trap 'exit 143' TERM
		((signal_status == 0)) || return "${signal_status}"
		((load_status == 0)) || fatal "cannot create scsi_debug test disks (status ${load_status})"
		((io_test_scsi_owned)) || fatal "cannot verify ownership of scsi_debug test disks"

		wait_until 10 0.1 io_test_scsi_disks_ready "${count}" \
			|| fatal "scsi_debug did not create ${count} test disks"
		for name in "${io_test_disks[@]}"; do
			printf '1\n' > "/sys/block/${name}/queue/iostats"
		done
		log_info "created IO test disks: ${io_test_disks[*]}"
	else
		# iostats must already be enabled. Kernel timestamp availability is
		# checked by IOLatency itself; preparation never changes existing disks.
		for block in /sys/block/vd* /sys/block/sd* /sys/block/nvme*; do
			name=${block##*/}
			[[ -b /dev/${name} && -r ${block}/queue/iostats && -r ${block}/size ]] || continue
			[[ $(< "${block}/queue/iostats") -ne 0 ]] || continue
			[[ ${name} != nvme* || -d ${block}/mq ]] || continue
			read -r sectors < "${block}/size"
			[[ ${sectors} =~ ^[0-9]+$ ]] || continue
			((sectors >= 262144)) || continue
			if ((${#io_test_disks[@]} == 0)); then
				io_test_disks=("${name}")
			fi
			# Prefer unused disks, but none is assumed exclusive: other
			# processes can issue IO after this check. All fallbacks stay read-only.
			command -v lsblk > /dev/null || continue
			mounts=$(lsblk -nr -o MOUNTPOINT "/dev/${name}") || continue
			[[ -z ${mounts//[[:space:]]/} ]] || continue
			compgen -G "${block}/holders/*" > /dev/null && continue
			compgen -G "${block}/${name}*/holders/*" > /dev/null && continue
			io_test_disks=("${name}")
			[[ -r ${block}/inflight ]] || continue
			read -r reads writes < "${block}/inflight" || continue
			((reads == 0 && writes == 0)) && break
		done
		if ((${#io_test_disks[@]})); then
			log_info "using existing IO test disk read-only: ${io_test_disks[0]}"
		fi
	fi
	if ((${#io_test_disks[@]})); then
		export TEST_IOLATENCY_IO_DEVICE=/dev/${io_test_disks[0]}
		export TEST_IOLATENCY_IO_EXCLUSIVE=${io_test_scsi_owned}
	else
		log_info "SKIP disk-dependent IO checks: no eligible SCSI/NVMe/virtio disk is available"
	fi
}

io_test_cleanup_disks() {
	if ((io_test_scsi_owned)); then
		# udev may still hold a freshly created disk after a case exits.
		timeout --kill-after=1s 10s bash -c \
			'while [[ -e /sys/module/scsi_debug ]]; do
				[[ -n $1 && -r /sys/module/scsi_debug/parameters/inq_vendor ]] \
					&& read -r owner < /sys/module/scsi_debug/parameters/inq_vendor \
					&& [[ ${owner} == "$1" ]] || exit 1
				modprobe -r scsi_debug && exit 0
				sleep 0.1
			done' io-test-scsi-cleanup "${io_test_scsi_owner}" || return 1
		io_test_scsi_owned=0
	fi
	io_test_scsi_owner=""
}
