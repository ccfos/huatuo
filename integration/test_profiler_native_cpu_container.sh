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

is_container && skip "native CPU profiler requires bare-metal cgroup/PMU access"

readonly PROFILER_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly PROFILER_BPF="${ROOT_DIR}/_output/bpf/native_cpu_profiler.o"
readonly CSS_BPF="${ROOT_DIR}/_output/bpf/cgroup_css_sync.o"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c"
readonly DEFAULT_CONTAINER_IMAGE="${NATIVE_PROFILER_CONTAINER_IMAGE:-busybox:1.36.1}"
readonly DOCKER_IMAGE="${NATIVE_PROFILER_DOCKER_IMAGE:-${DEFAULT_CONTAINER_IMAGE}}"
readonly CONTAINERD_IMAGE="${NATIVE_PROFILER_CONTAINERD_IMAGE:-${DEFAULT_CONTAINER_IMAGE}}"
readonly CONTAINERD_NAMESPACE="k8s.io"
readonly PROFILER_DURATION=5
readonly CHAIN_PATTERN=';f1;f2;f3 [0-9]+$'

[[ -x "${PROFILER_BIN}" ]] || fatal "profiler binary missing: ${PROFILER_BIN}"
[[ -r "${PROFILER_BPF}" ]] || fatal "native BPF object missing: ${PROFILER_BPF}"
[[ -r "${CSS_BPF}" ]] || fatal "cgroup CSS BPF object missing: ${CSS_BPF}"
[[ -r /proc/sys/kernel/perf_event_paranoid ]] \
	|| skip "perf_event_paranoid not readable: perf unavailable"
readonly PARANOID=$(cat /proc/sys/kernel/perf_event_paranoid)
[[ "${PARANOID}" -le 2 ]] \
	|| skip "kernel.perf_event_paranoid=${PARANOID} (>2) blocks perf sampling"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-native-container.XXXXXX")
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

test_docker() {
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
	run_profile_case docker "${DOCKER_CONTAINER_ID}"
	docker rm -f "${DOCKER_CONTAINER_ID}" > /dev/null
	DOCKER_CONTAINER_ID=""
	RUNTIMES_TESTED=$((RUNTIMES_TESTED + 1))
}

test_containerd() {
	local random_id

	if ! command -v ctr > /dev/null; then
		log_warn "skipping containerd: ctr is not installed"
		return
	fi
	if ! ctr --namespace "${CONTAINERD_NAMESPACE}" version > /dev/null 2>&1; then
		log_warn "skipping containerd: daemon is unavailable"
		return
	fi
	if ! ctr --namespace "${CONTAINERD_NAMESPACE}" images list --quiet \
		| grep -Fqx "${CONTAINERD_IMAGE}"; then
		log_warn "skipping containerd: image is unavailable: ${CONTAINERD_IMAGE}"
		return
	fi

	random_id=$(tr -d '-' < /proc/sys/kernel/random/uuid)
	CONTAINERD_CONTAINER_ID="${random_id}${random_id}"
	ctr --namespace "${CONTAINERD_NAMESPACE}" run --detach \
		--mount "type=bind,src=${FIXTURE_BIN},dst=/work/callchain,options=rbind:ro" \
		"${CONTAINERD_IMAGE}" \
		"${CONTAINERD_CONTAINER_ID}" \
		/work/callchain
	ctr --namespace "${CONTAINERD_NAMESPACE}" tasks list \
		| awk -v id="${CONTAINERD_CONTAINER_ID}" \
			'$1 == id && $3 == "RUNNING" { found = 1 } END { exit !found }' \
		|| fatal "containerd native container exited before profiling: ${CONTAINERD_CONTAINER_ID}"
	run_profile_case containerd "${CONTAINERD_CONTAINER_ID}"
	ctr --namespace "${CONTAINERD_NAMESPACE}" tasks delete --force \
		"${CONTAINERD_CONTAINER_ID}" > /dev/null 2>&1
	ctr --namespace "${CONTAINERD_NAMESPACE}" containers delete \
		"${CONTAINERD_CONTAINER_ID}" > /dev/null 2>&1
	CONTAINERD_CONTAINER_ID=""
	RUNTIMES_TESTED=$((RUNTIMES_TESTED + 1))
}

test_docker
test_containerd

((RUNTIMES_TESTED > 0)) || skip "no docker or containerd runtime was testable"
log_info "native container-ID profiling passed for ${RUNTIMES_TESTED} runtime(s)"
