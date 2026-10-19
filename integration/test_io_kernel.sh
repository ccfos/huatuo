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
test_pid=""
retain_resources=0
results_file="${HUATUO_BAMAI_TEST_TMPDIR}/io-kernel-results.log"
cleanup() {
	local status=$?
	local uuid
	((signal_status == 0)) || status=${signal_status}
	trap - EXIT
	trap '' INT TERM
	if ((retain_resources)); then
		log_error "IO test cleanup did not complete; retaining dm=${dm_name} uuid=${dm_uuid} scsi_owner=${io_test_scsi_owner}; results=${results_file}"
		exit "${status}"
	fi
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
forward_interrupt() {
	signal_status=$1
	[[ -z ${test_pid} ]] || kill -TERM "${test_pid}" 2> /dev/null || true
}
trap 'forward_interrupt 130' INT
trap 'forward_interrupt 143' TERM

prepare_scsi_disks() {
	local name
	io_test_prepare_disks 2
	trap 'forward_interrupt 130' INT
	trap 'forward_interrupt 143' TERM
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

# Compile before acquiring disks. Run each top-level test separately so a
# signal received during successful cleanup cannot start another fixture.
packages=(core/metrics internal/iohealth core/autotracing)
pattern='^(TestIOLatency(ArgumentProbeKernel|ReadMetricsKernel|CryptoProbeKernel)|TestBlkThrotl(StartAttachSmoke|KernelThrottleLifecycle|KernelPendingCapacityFailure)|TestIOHealth(FreezeKernel|Real(CommandEvidence|TargetResolution))|TestMDWatcherMonitorsMdadmLoopArray|TestIOTracingRealDiskstatsMetrics)$'
cd "${ROOT_DIR}"
for package in "${packages[@]}"; do
	go test -mod=vendor -p=1 -c \
		-o "${HUATUO_BAMAI_TEST_TMPDIR}/${package##*/}.test" "./${package}"
	((signal_status == 0)) || exit "${signal_status}"
done
prepare_scsi_disks
((signal_status == 0)) || exit "${signal_status}"
io_test_select_iolatency_disk
((signal_status == 0)) || exit "${signal_status}"
export TEST_INTEGRATION=true
deadline=$((SECONDS + 290))
: > "${results_file}"
status=0
for package in "${packages[@]}"; do
	binary="${HUATUO_BAMAI_TEST_TMPDIR}/${package##*/}.test"
	test_names=$("${binary}" -test.list "${pattern}")
	while IFS= read -r test_name; do
		[[ ${test_name} == Test* ]] || continue
		((signal_status == 0)) || break
		remaining=$((deadline - SECONDS))
		((remaining <= 240)) || remaining=240
		if ((remaining <= 60)); then
			log_error "IO qualification deadline reached before ${test_name}"
			status=124
			break
		fi
		# Isolate the test from the runner's process-group signals. timeout
		# signals only Go; body contexts stop IO, leaving cleanup commands alive.
		(
			cd "${ROOT_DIR}/${package}"
			exec setsid timeout --foreground --kill-after=60s "$((remaining - 60))s" \
				"${binary}" -test.count=1 -test.v -test.failfast \
				-test.timeout="${remaining}s" -test.run="^${test_name}$"
		) >> "${results_file}" 2>&1 &
		test_pid=$!
		if ((signal_status)); then
			kill -TERM "${test_pid}" 2> /dev/null || true
		fi
		while true; do
			if wait "${test_pid}"; then
				status=0
				break
			else
				status=$?
			fi
			# A trapped signal interrupts wait before owned cleanup finishes.
			kill -0 "${test_pid}" 2> /dev/null || break
		done
		test_pid=""
		if ((status == 137)) || grep -q '^panic: test timed out after ' "${results_file}"; then
			retain_resources=1
		fi
		if ((signal_status || status)); then
			break
		fi
	done <<< "${test_names}"
	if ((signal_status || status)); then
		break
	fi
done
cat "${results_file}"
if ((signal_status)); then
	exit "${signal_status}"
fi
exit "${status}"
