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

# Verify native container-ID profiling resolves the target cgroup CSS and
# restricts samples to the container workload.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

require_commands jq mkfifo setsid timeout awk findmnt
[[ ${EUID} -eq 0 ]] || skip "native CPU container profiling requires root"

# Tracing BPF needs CAP_BPF (39) and CAP_PERFMON (38), or CAP_SYS_ADMIN (21).
NATIVE_EFFECTIVE_CAPS=$(awk '$1 == "CapEff:" { print $2 }' "/proc/$$/status")
[[ ${NATIVE_EFFECTIVE_CAPS} =~ ^[0-9a-fA-F]+$ ]] || fatal "cannot read effective capabilities"
readonly NATIVE_EFFECTIVE_CAPS
(((16#${NATIVE_EFFECTIVE_CAPS} & ((1 << 21) | (1 << 39))) != 0)) \
	|| skip "BPF tracing requires CAP_BPF or CAP_SYS_ADMIN"
(((16#${NATIVE_EFFECTIVE_CAPS} & ((1 << 21) | (1 << 38))) != 0)) \
	|| skip "perf tracing requires CAP_PERFMON or CAP_SYS_ADMIN"
require_readable /sys/kernel/btf/vmlinux /proc/sys/kernel/perf_event_paranoid
if [[ -f /sys/fs/cgroup/cgroup.controllers ]]; then
	grep -qw cpu /sys/fs/cgroup/cgroup.controllers \
		|| skip "CPU controller is unavailable in cgroup v2"
else
	findmnt -rn -t cgroup -o OPTIONS | grep -E '(^|,)cpu(,|$)' > /dev/null \
		|| skip "CPU controller is not mounted in cgroup v1"
fi
# These perf capabilities bypass perf_event_paranoid, even when it exceeds 2.
log_info "native CPU container prerequisites: perf_event_paranoid=$(< /proc/sys/kernel/perf_event_paranoid), CapEff=${NATIVE_EFFECTIVE_CAPS}"

bpf_tool_setup profiler native_oncpu_profiler profiler-native-container
readonly PROFILER_BIN=${TOOL_BIN}
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c"
readonly DEFAULT_CONTAINER_IMAGE="${NATIVE_PROFILER_CONTAINER_IMAGE:-busybox:1.36.1}"
readonly DOCKER_IMAGE="${NATIVE_PROFILER_DOCKER_IMAGE:-${DEFAULT_CONTAINER_IMAGE}}"
readonly CONTAINERD_IMAGE="${NATIVE_PROFILER_CONTAINERD_IMAGE:-${DEFAULT_CONTAINER_IMAGE}}"
readonly CONTAINERD_NAMESPACE="k8s.io"
readonly PROFILER_DURATION=5
readonly CHAIN_PATTERN=';f1;f2;f3 [0-9]+$'

WORK_DIR=${TOOL_WORK_DIR}
FIXTURE_BIN="${WORK_DIR}/callchain"
DOCKER_CONTAINER_ID=""
CONTAINERD_CONTAINER_ID=""
RUNTIMES_TESTED=0

cleanup() {
	if [[ -n "${DOCKER_CONTAINER_ID}" ]]; then
		docker rm -f "${DOCKER_CONTAINER_ID}" > /dev/null 2>&1 || true
	fi
	if [[ -n "${CONTAINERD_CONTAINER_ID}" ]]; then
		ctr --namespace "${CONTAINERD_NAMESPACE}" tasks delete --force \
			"${CONTAINERD_CONTAINER_ID}" > /dev/null 2>&1 || true
		ctr --namespace "${CONTAINERD_NAMESPACE}" containers delete \
			"${CONTAINERD_CONTAINER_ID}" > /dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

compile_user_fixture "${FIXTURE_SRC}" "${FIXTURE_BIN}" -static

run_profile_case() {
	local runtime=$1
	local container_id=$2
	local out_dir="${WORK_DIR}/${runtime}"
	local folded_file

	mkdir -p "${out_dir}"
	log_info "profiling native runtime=${runtime} container_id=${container_id}"
	if ! "${PROFILER_BIN}" \
		--type cpu \
		--language c \
		--container-id "${container_id}" \
		--duration "${PROFILER_DURATION}" \
		--aggr-interval "${PROFILER_DURATION}" \
		--freq 99 \
		--output-format collapsed \
		--output-path "${out_dir}" \
		> "${out_dir}/profiler.out" 2> "${out_dir}/profiler.err"; then
		fatal "native profiler failed for runtime=${runtime} container ID"
	fi

	folded_file=$(find "${out_dir}" -maxdepth 1 -name 'perf_*.folded' \
		-type f -size +0c -print -quit)
	[[ -n "${folded_file}" ]] \
		|| fatal "no folded output for runtime=${runtime} container ID"
	grep -qE "${CHAIN_PATTERN}" "${folded_file}" \
		|| fatal "native stack missing for runtime=${runtime} container ID"
}

assert_outside_pid_excluded() (
	local runtime=$1 container_id=$2 container_pid=$3 container_start_time=$4
	local outside_pid outside_start_time
	[[ ${container_pid} =~ ^[1-9][0-9]*$ ]] || fatal "invalid ${runtime} container PID: ${container_pid}"
	trap cleanup_piped_processes EXIT
	log_info "checking native PID/container intersection: ${runtime}"
	start_piped_process "${WORK_DIR}/${runtime}/outside" "${FIXTURE_BIN}"
	outside_pid=${PROCESS_PID}
	outside_start_time=${PROCESS_START_TIME}
	exec {PROCESS_INPUT_FD}>&-
	exec {PROCESS_OUTPUT_FD}<&-
	# Prove the outside workload is sampled before checking that CSS excludes it.
	start_exception_profiler "${WORK_DIR}/${runtime}/outside-control" c 30 1 --pid "${outside_pid}"
	assert_profiler_samples_before_deadline "${outside_pid}" f3
	kill -INT "${PROFILER_PID}"
	assert_profiler_exit 0
	start_exception_profiler "${WORK_DIR}/${runtime}/outside-excluded" c 2 2 \
		--pid "${outside_pid}" --container-id "${container_id}"
	assert_process_message "${PROFILER_OUTPUT_FD}" "${PROFILER_OUT}" 'data reading loop started'
	assert_profiler_exit 0
	assert_process_alive "${outside_pid}" "${outside_start_time}" "outside workload during filter check"
	assert_process_alive "${container_pid}" "${container_start_time}" "container workload during filter check"
	[[ ! -s ${PROFILER_STACKS} ]] || fatal "combined PID/container filters accepted a nonmember PID"
)

assert_destroyed_container() (
	local runtime=$1 container_id=$2 container_pid=$3
	trap cleanup_piped_processes EXIT
	log_info "destroying a profiled native container: ${runtime}"
	start_exception_profiler "${WORK_DIR}/${runtime}/destroyed" c 30 1 \
		--container-id "${container_id}" --pid "${container_pid}"
	assert_profiler_samples_before_deadline "${container_pid}" f3
	if [[ ${runtime} == docker ]]; then
		timeout 15 docker rm -f "${container_id}"
	else
		timeout 15 ctr --namespace "${CONTAINERD_NAMESPACE}" tasks delete --force "${container_id}"
		timeout 15 ctr --namespace "${CONTAINERD_NAMESPACE}" containers delete "${container_id}"
	fi
	kill -INT "${PROFILER_PID}"
	assert_profiler_exit 0
)

containerd_workload_available() {
	if ! command -v ctr > /dev/null; then
		log_warn "skipping containerd: ctr is not installed"
		return 1
	fi
	if ! timeout --kill-after=2 10 ctr --namespace "${CONTAINERD_NAMESPACE}" version > /dev/null 2>&1; then
		log_warn "skipping containerd: daemon is unavailable"
		return 1
	fi
	if ! timeout --kill-after=2 10 ctr --namespace "${CONTAINERD_NAMESPACE}" images list --quiet \
		| grep -Fx "${CONTAINERD_IMAGE}" > /dev/null; then
		log_warn "skipping containerd: image is unavailable: ${CONTAINERD_IMAGE}"
		return 1
	fi
}

assert_privileged_container() (
	local container_name="huatuo-native-privileged-${WORK_DIR##*/}" root_dir path status=0
	local runner_image="huatuo-native-privileged:${WORK_DIR##*/}"
	local started=${SECONDS} docker_root docker_containers expected_runtimes=1
	local containerd_selected=0 snapshotter=${CONTAINERD_SNAPSHOTTER:-} snapshotter_root
	local -a runner_mounts=()
	require_commands tar
	docker_root=$(timeout --kill-after=2 10 docker info --format '{{.DockerRootDir}}') \
		|| fatal "cannot determine Docker data root for privileged runner"
	[[ ${docker_root} == /* ]] || fatal "invalid Docker data root: ${docker_root}"
	docker_containers="${docker_root%/}/containers"
	[[ -d ${docker_containers} && -r ${docker_containers} && -x ${docker_containers} ]] \
		|| fatal "Docker container metadata directory is unavailable: ${docker_containers}"
	runner_mounts+=(--mount "type=bind,src=${docker_containers},dst=${docker_containers},readonly")
	# The standalone profiler's local fallback uses this default metadata path.
	if [[ ${docker_containers} != /var/lib/docker/containers ]]; then
		runner_mounts+=(--mount "type=bind,src=${docker_containers},dst=/var/lib/docker/containers,readonly")
	fi
	if containerd_workload_available; then
		if [[ -z ${snapshotter} ]]; then
			snapshotter=$(timeout --kill-after=2 10 ctr namespaces list \
				| awk -v target_namespace="${CONTAINERD_NAMESPACE}" '
					$1 == target_namespace {
						sub(/^[^[:space:]]+[[:space:]]*/, "")
						count = split($0, labels, ",")
						for (i = 1; i <= count; i++) {
							if (sub(/^containerd[.]io\/defaults\/snapshotter=/, "", labels[i])) {
								sub(/[[:space:]]+$/, "", labels[i]); print labels[i]
							}
						}
					}') || fatal "cannot determine containerd namespace snapshotter"
			snapshotter=${snapshotter:-overlayfs}
		fi
		# Query the running snapshotter: daemon root and root_path can both vary.
		snapshotter_root=$(timeout --kill-after=2 10 ctr plugins list --detailed \
			"type==io.containerd.snapshotter.v1,id==${snapshotter}" \
			| awk '$1 == "root" { sub(/^[[:space:]]*root[[:space:]]+/, ""); root = $0; count++ }
				END { if (count != 1) exit 1; print root }') \
			|| fatal "cannot determine containerd snapshotter root: ${snapshotter}"
		[[ ${snapshotter_root} == /* && -d ${snapshotter_root} && -r ${snapshotter_root} && -w ${snapshotter_root} && -x ${snapshotter_root} ]] \
			|| fatal "containerd snapshotter root is unavailable: ${snapshotter_root}"
		runner_mounts+=(--mount "type=bind,src=${snapshotter_root},dst=${snapshotter_root}")
		containerd_selected=1
		expected_runtimes=2
	fi
	root_dir=$(cd "${ROOT_DIR}" && pwd -P)
	# Shared paths expose Docker metadata and let ctr mount containerd snapshots
	# and the host runtimes bind fixtures created by this runner.
	for path in /bin /sbin /usr /lib /lib64 /etc; do
		[[ -e ${path} ]] || continue
		[[ -d ${path} ]] || fatal "privileged runner tool path is not a directory: ${path}"
		runner_mounts+=(--mount "type=bind,src=${path},dst=${path},readonly")
	done
	for path in "${root_dir}" /tmp /run /var/run /sys; do
		[[ -d ${path} ]] || fatal "privileged runner bind source is not a directory: ${path}"
	done
	mkdir "${WORK_DIR}/privileged"
	trap '
		timeout --kill-after=2 15 docker rm -f "${container_name}" > /dev/null 2>&1 || true
		timeout --kill-after=2 15 docker image rm "${runner_image}" > /dev/null 2>&1 || true
	' EXIT
	# BusyBox aliases /lib64 to /lib, so mounting both VM directories there can
	# hide the ELF loader. An empty root gives each mount its own destination.
	tar -cf - --files-from /dev/null \
		| timeout --kill-after=2 15 docker import - "${runner_image}" > "${WORK_DIR}/privileged/image.log"
	log_info "running native intersection and destruction cases inside a privileged container"
	timeout --kill-after=20 180 docker run --rm --name "${container_name}" \
		--privileged --pid=host --network=host --cgroupns=host --ulimit memlock=-1:-1 \
		"${runner_mounts[@]}" \
		--mount "type=bind,src=${root_dir},dst=${root_dir},readonly" \
		--mount type=bind,src=/tmp,dst=/tmp \
		--mount type=bind,src=/run,dst=/run \
		--mount type=bind,src=/var/run,dst=/var/run \
		--mount type=bind,src=/sys,dst=/sys \
		--workdir "${root_dir}" \
		--env "ROOT_DIR=${root_dir}" \
		--env "HUATUO_BAMAI_TEST_TMPDIR=${WORK_DIR}/privileged" \
		--env "NATIVE_PROFILER_DOCKER_IMAGE=${DOCKER_IMAGE}" \
		--env "NATIVE_PROFILER_CONTAINERD_IMAGE=${CONTAINERD_IMAGE}" \
		--env NATIVE_PROFILER_TEST_PRIVILEGED_CONTAINER=0 \
		--env "NATIVE_PROFILER_TEST_CONTAINERD=${containerd_selected}" \
		--env "CONTAINERD_SNAPSHOTTER=${snapshotter}" \
		--env "NATIVE_PROFILER_EXPECTED_RUNTIMES=${expected_runtimes}" \
		--env "DOCKER_HOST=${DOCKER_HOST:-unix:///var/run/docker.sock}" \
		--entrypoint /bin/bash "${runner_image}" -euc '
			source "$ROOT_DIR/integration/lib.sh"
			[[ -f /.dockerenv ]] || fatal "privileged validation did not start in a Docker container"
			bash "$ROOT_DIR/integration/run.sh" -j 1 test_profiler_native_cpu_container.sh 2>&1 \
				| tee "$HUATUO_BAMAI_TEST_TMPDIR/runner.log"
			grep -qF "summary: total=1 passed=1 skipped=0 failed=0" "$HUATUO_BAMAI_TEST_TMPDIR/runner.log" \
				|| fatal "privileged native container case did not pass without skips"
		' || status=$?
	assert_eq "${status}" 0 "privileged native container cases" \
		|| fatal "privileged native container cases failed or skipped (exit ${status})"
	record_test_timing native-container privileged "${started}"
)

test_docker() {
	local container_pid container_start_time
	if ! command -v docker > /dev/null; then
		log_warn "skipping docker: command is not installed"
		return
	fi
	if ! docker info > /dev/null 2>&1; then
		log_warn "skipping docker: daemon is unavailable"
		return
	fi
	if ! docker image inspect "${DOCKER_IMAGE}" > /dev/null 2>&1; then
		log_warn "skipping docker: image is unavailable: ${DOCKER_IMAGE}"
		return
	fi

	DOCKER_CONTAINER_ID=$(docker run --detach --rm \
		--volume "${FIXTURE_BIN}:/work/callchain:ro" \
		--entrypoint /work/callchain \
		"${DOCKER_IMAGE}")
	docker inspect --format '{{.State.Running}}' "${DOCKER_CONTAINER_ID}" | grep -qx true \
		|| fatal "docker native container exited before profiling: ${DOCKER_CONTAINER_ID}"
	container_pid=$(docker inspect --format '{{.State.Pid}}' "${DOCKER_CONTAINER_ID}")
	container_start_time=$(process_start_time "${container_pid}") || fatal "docker container identity unavailable"
	run_profile_case docker "${DOCKER_CONTAINER_ID}"
	assert_outside_pid_excluded docker "${DOCKER_CONTAINER_ID}" "${container_pid}" "${container_start_time}"
	assert_destroyed_container docker "${DOCKER_CONTAINER_ID}" "${container_pid}"
	DOCKER_CONTAINER_ID=""
	if [[ ${NATIVE_PROFILER_TEST_PRIVILEGED_CONTAINER:-0} == 1 ]]; then
		assert_privileged_container
	fi
	RUNTIMES_TESTED=$((RUNTIMES_TESTED + 1))
}

test_containerd() {
	local random_id container_pid container_start_time

	# The privileged runner must use the runtimes its parent mounted, even if
	# another test makes a previously missing image available in the meantime.
	[[ ${NATIVE_PROFILER_TEST_CONTAINERD:-1} == 1 ]] || return 0
	containerd_workload_available || return 0

	random_id=$(tr -d '-' < /proc/sys/kernel/random/uuid)
	CONTAINERD_CONTAINER_ID="${random_id}${random_id}"
	ctr --namespace "${CONTAINERD_NAMESPACE}" run --detach \
		--mount "type=bind,src=${FIXTURE_BIN},dst=/work/callchain,options=rbind:ro" \
		"${CONTAINERD_IMAGE}" \
		"${CONTAINERD_CONTAINER_ID}" \
		/work/callchain
	container_pid=$(ctr --namespace "${CONTAINERD_NAMESPACE}" tasks list \
		| awk -v id="${CONTAINERD_CONTAINER_ID}" \
			'$1 == id && $3 == "RUNNING" { print $2; found = 1 } END { exit !found }') \
		|| fatal "containerd native container exited before profiling: ${CONTAINERD_CONTAINER_ID}"
	container_start_time=$(process_start_time "${container_pid}") || fatal "containerd container identity unavailable"
	run_profile_case containerd "${CONTAINERD_CONTAINER_ID}"
	assert_outside_pid_excluded containerd "${CONTAINERD_CONTAINER_ID}" "${container_pid}" "${container_start_time}"
	assert_destroyed_container containerd "${CONTAINERD_CONTAINER_ID}" "${container_pid}"
	CONTAINERD_CONTAINER_ID=""
	RUNTIMES_TESTED=$((RUNTIMES_TESTED + 1))
}

test_docker
test_containerd

((RUNTIMES_TESTED > 0)) || skip "no docker or containerd runtime was testable"
if [[ -n ${NATIVE_PROFILER_EXPECTED_RUNTIMES:-} ]]; then
	assert_eq "${RUNTIMES_TESTED}" "${NATIVE_PROFILER_EXPECTED_RUNTIMES}" "privileged runner runtime count" \
		|| fatal "privileged runner did not execute every selected runtime"
fi
log_info "native container-ID profiling passed for ${RUNTIMES_TESTED} runtime(s)"
