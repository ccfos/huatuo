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

# Callers own fixture compilation, scenarios, and EXIT cleanup.
set -euo pipefail

readonly NATIVE_CONTAINER_NAMESPACE=k8s.io
native_container_runtime=""
native_container_id=""

require_native_container_profiling() {
	local caps
	# Tracing BPF needs CAP_BPF (39) and CAP_PERFMON (38), or CAP_SYS_ADMIN (21).
	caps=$(awk '$1 == "CapEff:" { print $2 }' "/proc/$$/status")
	[[ ${caps} =~ ^[0-9a-fA-F]+$ ]] || fatal "cannot read effective capabilities"
	(((16#${caps} & ((1 << 21) | (1 << 39))) != 0)) \
		|| skip "BPF tracing requires CAP_BPF or CAP_SYS_ADMIN"
	(((16#${caps} & ((1 << 21) | (1 << 38))) != 0)) \
		|| skip "perf tracing requires CAP_PERFMON or CAP_SYS_ADMIN"
	require_readable /sys/kernel/btf/vmlinux /proc/sys/kernel/perf_event_paranoid
	if [[ ${HUATUO_TEST_CGROUP_MODE} == unified ]]; then
		grep -qw cpu /sys/fs/cgroup/cgroup.controllers \
			|| skip "CPU controller is unavailable in cgroup v2"
	else
		findmnt -rn -t cgroup -o OPTIONS | grep -E '(^|,)cpu(,|$)' > /dev/null \
			|| skip "CPU controller is not mounted in cgroup v1"
	fi
	# These perf capabilities bypass perf_event_paranoid, even when it exceeds 2.
	log_info "native CPU container prerequisites: perf_event_paranoid=$(< /proc/sys/kernel/perf_event_paranoid), CapEff=${caps}"
}

native_container_runtime_available() {
	local runtime=$1 image=$2
	if [[ $runtime == docker ]]; then
		command -v docker > /dev/null || {
			log_warn "skipping docker: command is not installed"
			return 1
		}
		timeout --kill-after=2 10 docker info > /dev/null 2>&1 || {
			log_warn "skipping docker: daemon is unavailable"
			return 1
		}
		timeout --kill-after=2 10 docker image inspect "$image" > /dev/null 2>&1 || {
			log_warn "skipping docker: image is unavailable: $image"
			return 1
		}
	else
		command -v ctr > /dev/null || {
			log_warn "skipping containerd: ctr is not installed"
			return 1
		}
		timeout --kill-after=2 10 ctr --namespace "$NATIVE_CONTAINER_NAMESPACE" version > /dev/null 2>&1 || {
			log_warn "skipping containerd: daemon is unavailable"
			return 1
		}
		timeout --kill-after=2 10 ctr --namespace "$NATIVE_CONTAINER_NAMESPACE" images list --quiet | grep -Fx "$image" > /dev/null || {
			log_warn "skipping containerd: image is unavailable: $image"
			return 1
		}
	fi
}

native_container_start() {
	local runtime=$1 image=$2 executable=$3 name=${4:-callchain} random_id created_id
	shift 3
	if (($# > 0)); then shift; fi
	[[ -z $native_container_id ]] || fatal "stop owned container before replacing it: $native_container_id"
	native_container_runtime=$runtime
	case $runtime in
	docker)
		# The name remains available for cleanup if the client times out after creation.
		native_container_id="huatuo-native-${BASHPID}-${HUATUO_BAMAI_TEST_TMPDIR##*/}"
		created_id=$(timeout --kill-after=2 15 docker run --detach --rm --name "$native_container_id" \
			--volume "$executable:/work/$name:ro" --entrypoint "/work/$name" "$image" "$@") \
			|| fatal "cannot start docker profiling workload"
		[[ $created_id =~ ^[[:xdigit:]]{64}$ ]] || fatal "docker returned an invalid profiling container ID: $created_id"
		native_container_id=$created_id
		;;
	containerd)
		random_id=$(tr -d '-' < /proc/sys/kernel/random/uuid)
		native_container_id="${random_id}${random_id}"
		timeout --kill-after=2 15 ctr --namespace "$NATIVE_CONTAINER_NAMESPACE" run --detach \
			--mount "type=bind,src=$executable,dst=/work/$name,options=rbind:ro" \
			"$image" "$native_container_id" "/work/$name" "$@" \
			|| fatal "cannot start containerd profiling workload: $native_container_id"
		;;
	*) fatal "unsupported profiling workload runtime: $runtime" ;;
	esac
}

native_container_stop() {
	[[ -n $native_container_id ]] || return 0
	local status=0
	case $native_container_runtime in
	docker) timeout --kill-after=2 15 docker rm -f "$native_container_id" > /dev/null || status=$? ;;
	containerd)
		timeout --kill-after=2 15 ctr --namespace "$NATIVE_CONTAINER_NAMESPACE" tasks delete --force "$native_container_id" > /dev/null || status=$?
		timeout --kill-after=2 15 ctr --namespace "$NATIVE_CONTAINER_NAMESPACE" containers delete "$native_container_id" > /dev/null || status=$?
		;;
	esac
	((status == 0)) || {
		log_error "cannot remove owned $native_container_runtime profiling container: $native_container_id"
		return 1
	}
	native_container_id=""
}
