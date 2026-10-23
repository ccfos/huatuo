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

# Helpers for test cgroups and daemon resource limits.
set -euo pipefail

# Prefer v1 on hybrid hosts, where it owns the memory controller.
cgroup_memory_root() {
	local root
	root=$(findmnt -rn -t cgroup -O memory -o TARGET) || true
	if [[ -z "${root}" ]]; then
		root=$(findmnt -rn -t cgroup2 -o TARGET) || return 1
	fi
	printf '%s\n' "${root}"
}

# cgroup_find_container <root> <container-id>: support cgroupfs and systemd scopes.
cgroup_find_container() {
	local root=$1 container_id=$2 path
	[[ ${container_id} =~ ^[0-9a-f]{64}$ ]] || return 1
	path=$(find "${root}" -type d \
		\( -name "${container_id}" -o -name "*-${container_id}.scope" \) -print -quit) || return 1
	[[ -n "${path}" ]] || return 1
	printf '%s\n' "${path}"
}

# cgroup_create <name>: print the new memory cgroup path.
cgroup_create() {
	local root
	[[ $1 =~ ^[a-zA-Z0-9_-]+$ ]] || return 1
	root=$(cgroup_memory_root) || return 1
	mkdir "${root}/$1" || return 1
	printf '%s\n' "${root}/$1"
}

# cgroup_configure_memory <path> <bytes>: enforce a resident-memory limit without swap.
cgroup_configure_memory() {
	local path=$1 limit=$2 memory=memory.max swap=memory.swap.max
	[[ ${limit} =~ ^[1-9][0-9]*$ ]] || return 1
	if [[ -e "${path}/memory.limit_in_bytes" ]]; then
		memory=memory.limit_in_bytes
		swap=memory.swappiness
		[[ -w "${path}/memory.oom_control" ]] || return 1
		echo 0 > "${path}/memory.oom_control" || return 1
	fi
	[[ -w "${path}/${memory}" && -w "${path}/${swap}" ]] || return 1
	echo "${limit}" > "${path}/${memory}" || return 1
	echo 0 > "${path}/${swap}" || return 1
	[[ $(< "${path}/${memory}") == "${limit}" && $(< "${path}/${swap}") == 0 ]]
}

# cgroup_run <path> <timeout-seconds> <command...>. Keep the timeout supervisor outside.
cgroup_run() {
	local path=$1 duration=$2
	shift 2
	timeout --kill-after=1 "${duration}" bash -c '
		echo "$BASHPID" > "$1/cgroup.procs" || exit 1
		shift
		exec "$@"
	' _ "${path}" "$@"
}

# Delete only empty cgroups; the caller owns workload termination and reaping.
cgroup_delete() {
	rmdir "$1"
}

# cgroup_css_require_environment <root-var> <symbol-var> <notify-file-var>.
# The notification file must trigger the selected kernel read function.
# Outputs belong to the caller; this check never creates or configures cgroups.
cgroup_css_require_environment() {
	local _css_root _css_symbol _css_notify_file
	if [[ -r /sys/fs/cgroup/cgroup.controllers ]]; then
		_css_root=/sys/fs/cgroup
		_css_symbol=memory_current_read
		_css_notify_file=memory.current
	elif [[ -d /sys/fs/cgroup/memory ]]; then
		_css_root=/sys/fs/cgroup/memory
		_css_symbol=cgroup_clone_children_read
		_css_notify_file=cgroup.clone_children
	else
		skip "supported cgroup v1/v2 hierarchy is not available"
	fi

	kprobe_available "${_css_symbol}" \
		|| skip "${_css_symbol} is not available for kprobe"

	printf -v "$1" '%s' "${_css_root}"
	printf -v "$2" '%s' "${_css_symbol}"
	printf -v "$3" '%s' "${_css_notify_file}"
}

# Call once before startup, in the current shell. Reject existing leaf groups;
# absence is only a precondition, not proof of ownership during later cleanup.
# This check never creates or configures cgroups.
huatuo_bamai_require_cgroup() {
	local controller root
	require_commands findmnt

	if [[ -f /sys/fs/cgroup/cgroup.controllers ]]; then
		require_commands systemctl timeout
		timeout --kill-after=1 10 systemctl show --property=Version > /dev/null 2>&1 \
			|| skip "cgroup v2 runtime limits require the systemd manager"
		for controller in cpu memory; do
			grep -qw "${controller}" /sys/fs/cgroup/cgroup.controllers \
				|| skip "cgroup v2 ${controller} controller is unavailable"
		done

		readonly RUNTIME_CGROUP_MODE=v2
		readonly RUNTIME_CGROUP_CPU_ROOT=/sys/fs/cgroup
		readonly RUNTIME_CGROUP_MEMORY_ROOT=/sys/fs/cgroup
		readonly RUNTIME_CGROUP_PATH=/huatuo.slice/huatuo-bamai.slice
		readonly RUNTIME_CGROUP_ROOTS=(/sys/fs/cgroup)
	else
		readonly RUNTIME_CGROUP_MODE=v1
		RUNTIME_CGROUP_CPU_ROOT=$(findmnt -rn -t cgroup -O cpu -o TARGET) \
			|| skip "cgroup v1 CPU controller is unavailable"
		RUNTIME_CGROUP_MEMORY_ROOT=$(cgroup_memory_root) \
			|| skip "cgroup memory controller is unavailable"
		readonly RUNTIME_CGROUP_CPU_ROOT RUNTIME_CGROUP_MEMORY_ROOT
		readonly RUNTIME_CGROUP_PATH=/huatuo-bamai
		mapfile -t RUNTIME_CGROUP_ROOTS < <(findmnt -rn -t cgroup -o TARGET)
		readonly RUNTIME_CGROUP_ROOTS
	fi

	for root in "${RUNTIME_CGROUP_ROOTS[@]}"; do
		[[ ! -d "${root}${RUNTIME_CGROUP_PATH}" ]] \
			|| skip "runtime cgroup already exists: ${root}${RUNTIME_CGROUP_PATH}"
	done
	[[ -w "${RUNTIME_CGROUP_CPU_ROOT}" && -w "${RUNTIME_CGROUP_MEMORY_ROOT}" ]] \
		|| skip "CPU and memory cgroup roots must be writable"
}
