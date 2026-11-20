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

# Exercise the real native CPU command with missing, corrupt and usable BTF.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler.sh"

run_isolated_btf_profiler() {
	local mode=$1 case_dir=$2 target_pid=$3 directory release
	local fixtures
	fixtures=$(dirname "${case_dir}")
	release=$(uname -r)
	[[ $(stat -Lc '%i' /proc/self/ns/mnt) != $(stat -Lc '%i' "/proc/${PPID}/ns/mnt") ]] \
		|| fatal "BTF fault injection must run in a separate mount namespace"
	mount --make-rprivate /
	# This child runs in a private mount namespace; never mask host BTF globally.
	if [[ ! -d /sys/kernel/btf ]]; then
		mount -t tmpfs -o size=64m tmpfs /sys/kernel
		mkdir /sys/kernel/btf
	fi
	for directory in /sys/kernel/btf /boot /lib/modules /usr/lib/modules /usr/lib/debug; do
		[[ ! -d ${directory} ]] || mount -t tmpfs -o size=64m tmpfs "${directory}"
	done
	case ${mode} in
	sysfs) cp "${fixtures}/kernel.btf" /sys/kernel/btf/vmlinux ;;
	elf) cp "${fixtures}/kernel.elf" "/boot/vmlinux-${release}" ;;
	corrupt-sysfs) printf 'invalid BTF\n' > /sys/kernel/btf/vmlinux ;;
	corrupt-elf) cp "${fixtures}/corrupt.elf" "/boot/vmlinux-${release}" ;;
	missing) ;;
	*) fatal "unknown BTF scenario: ${mode}" ;;
	esac
	if [[ ${mode} == missing ]]; then
		local preflight_status=0
		(require_kernel_btf) || preflight_status=$?
		assert_eq "${preflight_status}" 77 "missing BTF dependency classification" \
			|| fatal "missing BTF sources were not classified as unavailable"
	else
		require_kernel_btf
	fi
	exec timeout --signal=TERM --kill-after=5 20 "${ROOT_DIR}/_output/bin/profiler" \
		--type cpu --language c --pid "${target_pid}" --duration 5 \
		--freq 99 --aggr-interval 5 --output-format collapsed \
		--output-path "${case_dir}/profile.folded" --verbose
}

if [[ ${1:-} == --isolated ]]; then
	shift
	run_isolated_btf_profiler "$@"
	exit
fi

require_native_pid_profiling
require_commands clang objcopy unshare mount timeout
require_profiler_bpf_sampling
require_kernel_btf
[[ -d /boot ]] || skip "BTF fallback fault injection requires /boot"
unshare --mount --propagation private true \
	|| skip "BTF fault injection requires permission to create a private mount namespace"
bpf_tool_setup profiler native_oncpu_profiler profiler-kernel-btf

if [[ -r /sys/kernel/btf/vmlinux ]]; then
	cp /sys/kernel/btf/vmlinux "${TOOL_WORK_DIR}/kernel.btf"
else
	btf_sources=()
	kernel_btf_sources btf_sources
	for source in "${btf_sources[@]:1}"; do
		[[ -e ${source} ]] || continue
		objcopy --dump-section ".BTF=${TOOL_WORK_DIR}/kernel.btf" \
			"${source}" "${TOOL_WORK_DIR}/source.elf" \
			|| fatal "cannot extract the running kernel's BTF from ${source}"
		break
	done
fi
[[ -s ${TOOL_WORK_DIR}/kernel.btf ]] || fatal "cannot prepare the running kernel's BTF fault fixtures"
compile_user_fixture "${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c" "${TOOL_WORK_DIR}/workload"
if [[ -f ${TOOL_WORK_DIR}/source.elf ]]; then
	cp "${TOOL_WORK_DIR}/source.elf" "${TOOL_WORK_DIR}/kernel.elf"
else
	objcopy --add-section ".BTF=${TOOL_WORK_DIR}/kernel.btf" "${TOOL_WORK_DIR}/workload" "${TOOL_WORK_DIR}/kernel.elf"
fi
printf 'invalid BTF\n' > "${TOOL_WORK_DIR}/corrupt.btf"
objcopy --add-section ".BTF=${TOOL_WORK_DIR}/corrupt.btf" "${TOOL_WORK_DIR}/workload" "${TOOL_WORK_DIR}/corrupt.elf"

btf_target_pid=""
cleanup() {
	[[ -z ${btf_target_pid} ]] || stop_and_wait_by_pid "${btf_target_pid}" 5 || true
}
trap cleanup EXIT

assert_kernel_btf_profile() {
	local mode=$1 status=$2 case_dir=$3
	case ${mode} in
	sysfs | elf)
		assert_eq "${status}" 0 "${mode}: profiler exit status" \
			|| fatal "profiler failed with usable BTF (see ${case_dir}/profiler.err)"
		[[ -s ${case_dir}/profile.folded ]] \
			|| fatal "profiler produced no samples with ${mode} BTF"
		grep -qE ';f1;f2;f3 [1-9][0-9]*$' "${case_dir}/profile.folded" \
			|| fatal "profiler missed the native call chain with ${mode} BTF"
		;;
	*)
		assert_eq "${status}" 1 "${mode}: profiler must fail, not skip or time out" \
			|| fatal "unexpected profiler failure status (see ${case_dir}/profiler.err)"
		grep -qF 'load native CPU' "${case_dir}/profiler.err" \
			|| fatal "failure did not identify native BPF loading (see ${case_dir}/profiler.err)"
		if [[ ${mode} == missing ]]; then
			grep -qF 'no BTF found for kernel version' "${case_dir}/profiler.err" \
				|| fatal "missing BTF diagnostic did not identify the kernel (see ${case_dir}/profiler.err)"
		else
			grep -qi 'btf' "${case_dir}/profiler.err" \
				|| fatal "corrupt BTF diagnostic is missing (see ${case_dir}/profiler.err)"
		fi
		[[ ! -e ${case_dir}/profile.folded ]] || fatal "failed profiler published a profile"
		;;
	esac
}

for mode in missing corrupt-sysfs corrupt-elf sysfs elf; do
	case_dir="${TOOL_WORK_DIR}/${mode}"
	mkdir -p "${case_dir}"
	"${TOOL_WORK_DIR}/workload" > "${case_dir}/workload.log" 2>&1 &
	btf_target_pid=$!
	status=0
	unshare --mount --propagation private bash "${BASH_SOURCE[0]}" --isolated \
		"${mode}" "${case_dir}" "${btf_target_pid}" \
		> "${case_dir}/profiler.out" 2> "${case_dir}/profiler.err" || status=$?
	assert_kernel_btf_profile "${mode}" "${status}" "${case_dir}"
	stop_and_wait_by_pid "${btf_target_pid}" 5 || true
	btf_target_pid=""
	log_info "${mode}: native profiler BTF behavior verified"
done
