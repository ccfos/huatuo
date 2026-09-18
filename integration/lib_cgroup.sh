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

# Memory-controller helpers for isolated, flat test cgroups.
set -euo pipefail

# cgroup_create <name>: print the new memory cgroup path. Prefer v1 on hybrid hosts.
cgroup_create() {
	local root
	[[ $1 =~ ^[a-zA-Z0-9_-]+$ ]] || return 1
	root=$(findmnt -rn -t cgroup -O memory -o TARGET) || true
	if [[ -z "${root}" ]]; then
		root=$(findmnt -rn -t cgroup2 -o TARGET) || return 1
	fi
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

cgroup_oom_kills() {
	local file=$1/memory.events
	[[ -e "${file}" ]] || file=$1/memory.oom_control
	awk '$1 == "oom_kill" { print $2; found = 1 } END { exit !found }' "${file}"
}

# Delete only empty cgroups; the caller owns workload termination and reaping.
cgroup_delete() {
	rmdir "$1"
}
