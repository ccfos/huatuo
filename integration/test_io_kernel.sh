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

# Run live IO tests with owned virtual disks; existing disks are never written.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_io.sh"
require_commands go timeout setsid

unset TEST_IO_MULTILEAF_DEVICE TEST_IO_UNIQUE_DEVICE TEST_IOSTAT_DEVICE
unset TEST_IOLATENCY_IO_DEVICE

dm_name=""
dm_uuid=""
signal_status=0
cleanup() {
	local status=$?
	local uuid
	trap - EXIT
	trap '' INT TERM
	if [[ -n ${dm_name} ]]; then
		if uuid=$(timeout 10s dmsetup info -c --noheadings -o uuid "${dm_name}") \
			&& [[ ${uuid//[[:space:]]/} == "${dm_uuid}" ]]; then
			timeout 10s dmsetup remove --retry "${dm_name}" || status=1
		else
			status=1
		fi
	fi
	io_test_cleanup_disks || status=1
	exit "${status}"
}
trap cleanup EXIT
# Defer interruption until a successful creation has recorded its owner.
trap 'signal_status=130' INT
trap 'signal_status=143' TERM

prepare_scsi_disks() {
	local name
	io_test_prepare_disks 2
	if ((${#io_test_disks[@]} == 0)); then
		return
	fi
	require_commands fio
	# Hardware evidence commands support SCSI/NVMe, not virtio-blk.
	if [[ ${io_test_disks[0]} == sd* || ${io_test_disks[0]} == nvme* ]]; then
		export TEST_IO_UNIQUE_DEVICE=${io_test_disks[0]}
	fi
	export TEST_IOSTAT_DEVICE=${io_test_disks[0]}
	# Reconfiguration and DM topology changes are limited to owned disks.
	((io_test_scsi_owned)) || return 0
	require_commands dmsetup
	trap 'signal_status=130' INT
	trap 'signal_status=143' TERM

	# The second disk gives target discovery a genuine multi-leaf DM topology.
	name="huatuo-io-kernel-$$"
	# DM can exist even if dmsetup subsequently times out waiting for udev.
	dm_name=${name}
	dm_uuid="HUATUO-IO-KERNEL-$(< /proc/sys/kernel/random/uuid)"
	setsid timeout 10s dmsetup create "${name}" --uuid "${dm_uuid}" --table \
		"0 131072 linear /dev/${io_test_disks[0]} 0
131072 131072 linear /dev/${io_test_disks[1]} 0"
	((signal_status == 0)) || exit "${signal_status}"
	export TEST_IO_MULTILEAF_DEVICE
	TEST_IO_MULTILEAF_DEVICE=$(basename "$(readlink -f "/dev/mapper/${dm_name}")")
}

prepare_scsi_disks
((signal_status == 0)) || exit "${signal_status}"
trap 'exit 130' INT
trap 'exit 143' TERM
io_test_select_iolatency_disk
export TEST_INTEGRATION=true
cd "${ROOT_DIR}"

# Keep the root-only selection here so make unit never acquires devices.
timeout --kill-after=10s 290s go test -mod=vendor -p=1 -count=1 -v -timeout=4m \
	-run '^(TestIOLatency(ArgumentProbeKernel|ReadMetricsKernel|CryptoProbeKernel)|TestThrotl(StartAttachSmoke|KernelThrottleLifecycle|KernelPendingCapacityFailure)|TestIOHealth(FreezeKernel|Real(CommandEvidence|TargetResolution))|TestMDWatcherMonitorsMdadmLoopArray|TestIOTracingRealDiskstatsMetrics)$' \
	./core/metrics ./internal/iohealth ./core/autotracing
