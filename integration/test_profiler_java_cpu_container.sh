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

# Verify standalone container discovery through every available runtime. Each
# runtime must resolve both full and short IDs and capture the Java workload.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly TOOL_BIN="${ROOT_DIR}/_output/bin/profiler"
readonly FIXTURE_SRC="${ROOT_DIR}/integration/testdata/TestProfilerJavaMultiPID.java"
readonly DEFAULT_CONTAINER_IMAGE="${JAVA_PROFILER_CONTAINER_IMAGE:-eclipse-temurin:17-jdk}"
readonly DOCKER_IMAGE="${JAVA_PROFILER_DOCKER_IMAGE:-${DEFAULT_CONTAINER_IMAGE}}"
readonly CONTAINERD_IMAGE="${JAVA_PROFILER_CONTAINERD_IMAGE:-${DEFAULT_CONTAINER_IMAGE}}"
readonly CONTAINERD_NAMESPACE="k8s.io"
readonly PROFILER_DURATION=5
readonly EXPECTED_METHOD="TestProfilerJavaMultiPID.alphaHotMethod"

[[ -x "${TOOL_BIN}" ]] || fatal "profiler binary missing: ${TOOL_BIN}"
[[ -x "${JAVA_PROFILER_TOOL_PATH}/bin/asprof" ]] \
	|| skip "asprof missing: ${JAVA_PROFILER_TOOL_PATH}/bin/asprof"
[[ -r "${JAVA_PROFILER_TOOL_PATH}/lib/libasyncProfiler.so" ]] \
	|| skip "async-profiler library missing: ${JAVA_PROFILER_TOOL_PATH}/lib/libasyncProfiler.so"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/profiler-java-container.XXXXXX")
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

run_profile_case() {
	local runtime=$1
	local id_kind=$2
	local target_id=$3
	local out_dir="${WORK_DIR}/${runtime}/${id_kind}"
	local folded_file

	mkdir -p "${out_dir}"
	log_info "profiling Java runtime=${runtime} ${id_kind}_id=${target_id}"
	if ! "${TOOL_BIN}" \
		--type cpu \
		--language java \
		--container-id "${target_id}" \
		--tool-path "${JAVA_PROFILER_TOOL_PATH}" \
		--duration "${PROFILER_DURATION}" \
		--aggr-interval "${PROFILER_DURATION}" \
		--freq 99 \
		--output-format collapsed \
		--output-path "${out_dir}" \
		> "${out_dir}/profiler.out" 2> "${out_dir}/profiler.err"; then
		fatal "profiler failed for runtime=${runtime} ${id_kind} container ID"
	fi

	folded_file=$(find "${out_dir}" -maxdepth 1 -name 'perf_*.folded' \
		-type f -size +0c -print -quit)
	[[ -n "${folded_file}" ]] \
		|| fatal "no folded output for runtime=${runtime} ${id_kind} container ID"
	grep -q "${EXPECTED_METHOD}" "${folded_file}" \
		|| fatal "Java stack missing for runtime=${runtime} ${id_kind} container ID"
}

run_runtime_cases() {
	local runtime=$1
	local container_id=$2

	run_profile_case "${runtime}" full "${container_id}"
	run_profile_case "${runtime}" short "${container_id:0:12}"
	RUNTIMES_TESTED=$((RUNTIMES_TESTED + 1))
}

test_docker() {
	local runtime_dir="${WORK_DIR}/docker"

	if ! command -v docker > /dev/null; then
		log_warn "skipping Docker: command is not installed"
		return
	fi
	if ! docker info > /dev/null 2>&1; then
		log_warn "skipping Docker: daemon is unavailable"
		return
	fi
	if ! docker image inspect "${DOCKER_IMAGE}" > /dev/null 2>&1; then
		log_warn "skipping Docker: image is unavailable: ${DOCKER_IMAGE}"
		return
	fi

	mkdir -p "${runtime_dir}"
	DOCKER_CONTAINER_ID=$(docker run --detach --rm \
		--volume "${FIXTURE_SRC}:/src/TestProfilerJavaMultiPID.java:ro" \
		--volume "${runtime_dir}:/work" \
		"${DOCKER_IMAGE}" \
		sh -c 'javac -d /work /src/TestProfilerJavaMultiPID.java &&
			touch /work/java.ready &&
			exec java -XX:CompileCommand=dontinline,TestProfilerJavaMultiPID.alphaHotMethod \
			-cp /work TestProfilerJavaMultiPID alpha')

	wait_until 30 1 test -f "${runtime_dir}/java.ready" \
		|| fatal "Docker Java container did not become ready: ${DOCKER_CONTAINER_ID}"
	docker inspect --format '{{.State.Running}}' "${DOCKER_CONTAINER_ID}" | grep -qx true \
		|| fatal "Docker Java container exited before profiling: ${DOCKER_CONTAINER_ID}"
	run_runtime_cases docker "${DOCKER_CONTAINER_ID}"
	docker rm -f "${DOCKER_CONTAINER_ID}" > /dev/null
	DOCKER_CONTAINER_ID=""
}

test_containerd() {
	local runtime_dir="${WORK_DIR}/containerd"
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

	mkdir -p "${runtime_dir}"
	random_id=$(tr -d '-' < /proc/sys/kernel/random/uuid)
	CONTAINERD_CONTAINER_ID="${random_id}${random_id}"
	ctr --namespace "${CONTAINERD_NAMESPACE}" run --detach \
		--mount "type=bind,src=${FIXTURE_SRC},dst=/src/TestProfilerJavaMultiPID.java,options=rbind:ro" \
		--mount "type=bind,src=${runtime_dir},dst=/work,options=rbind:rw" \
		"${CONTAINERD_IMAGE}" \
		"${CONTAINERD_CONTAINER_ID}" \
		sh -c 'javac -d /work /src/TestProfilerJavaMultiPID.java &&
			touch /work/java.ready &&
			exec java -XX:CompileCommand=dontinline,TestProfilerJavaMultiPID.alphaHotMethod \
			-cp /work TestProfilerJavaMultiPID alpha'

	wait_until 30 1 test -f "${runtime_dir}/java.ready" \
		|| fatal "containerd Java container did not become ready: ${CONTAINERD_CONTAINER_ID}"
	ctr --namespace "${CONTAINERD_NAMESPACE}" tasks list \
		| awk -v id="${CONTAINERD_CONTAINER_ID}" '$1 == id && $3 == "RUNNING" { found = 1 } END { exit !found }' \
		|| fatal "containerd Java container exited before profiling: ${CONTAINERD_CONTAINER_ID}"
	run_runtime_cases containerd "${CONTAINERD_CONTAINER_ID}"
	ctr --namespace "${CONTAINERD_NAMESPACE}" tasks delete --force \
		"${CONTAINERD_CONTAINER_ID}" > /dev/null 2>&1
	ctr --namespace "${CONTAINERD_NAMESPACE}" containers delete \
		"${CONTAINERD_CONTAINER_ID}" > /dev/null 2>&1
	CONTAINERD_CONTAINER_ID=""
}

test_docker
test_containerd

((RUNTIMES_TESTED > 0)) || skip "no Docker or containerd runtime was testable"
log_info "container-id profiling passed for ${RUNTIMES_TESTED} runtime(s)"
