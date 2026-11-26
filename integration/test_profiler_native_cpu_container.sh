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

# Verify container-ID filtering and target deletion without PID filters.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_profiler.sh"
source "${ROOT_DIR}/integration/lib_profiler_native_container.sh"

# Symbolization reads procfs using kernel sample PIDs.
require_native_pid_profiling
require_commands jq mkfifo setsid timeout awk findmnt
require_native_container_profiling
bpf_tool_setup profiler native_oncpu_profiler profiler-native-container
readonly NATIVE_CONTAINER_DOCKER_IMAGE="${NATIVE_PROFILER_DOCKER_IMAGE:-${NATIVE_PROFILER_CONTAINER_IMAGE:-busybox:1.36.1}}"
readonly NATIVE_CONTAINER_CONTAINERD_IMAGE="${NATIVE_PROFILER_CONTAINERD_IMAGE:-${NATIVE_PROFILER_CONTAINER_IMAGE:-busybox:1.36.1}}"
readonly NATIVE_CONTAINER_WORK=${TOOL_WORK_DIR}
readonly NATIVE_CONTAINER_FIXTURE="${NATIVE_CONTAINER_WORK}/callchain"
native_container_profiler_pid=""
native_container_runtimes_tested=0

cleanup() {
	local status=$?
	trap - EXIT
	[[ -z $native_container_profiler_pid ]] || stop_by_pid "$native_container_profiler_pid" 5 || true
	cleanup_running_jobs || status=1
	native_container_stop || status=1
	exit "$status"
}
trap cleanup EXIT

compile_user_fixture "${ROOT_DIR}/integration/testdata/test_profiler_callchain.user.c" "$NATIVE_CONTAINER_FIXTURE" -static

readonly NATIVE_CONTAINER_DURATION=5
readonly NATIVE_CONTAINER_CHAIN_PATTERN=';f1;f2;f3 [0-9]+$'

run_profile_case() {
	local runtime=$1
	local container_id=$2
	local out_dir="${NATIVE_CONTAINER_WORK}/${runtime}"
	local folded_file

	mkdir -p "${out_dir}"
	log_info "profiling native runtime=${runtime} container_id=${container_id}"
	if ! "${TOOL_BIN}" \
		--type cpu \
		--language c \
		--container-id "${container_id}" \
		--duration "${NATIVE_CONTAINER_DURATION}" \
		--aggr-interval "${NATIVE_CONTAINER_DURATION}" \
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
	assert_folded_stack present "$NATIVE_CONTAINER_CHAIN_PATTERN" "$folded_file" \
		|| fatal "native stack missing for runtime=${runtime} container ID"
}
native_container_samples_ready() {
	jq -se 'any(.[]; (.profile.sample | length) > 0)' "$1" > /dev/null 2>&1
}

run_removed_target_case() {
	local target_id=${native_container_id}
	local out_dir="${NATIVE_CONTAINER_WORK}/docker-removed"
	local output="${out_dir}/windows.json"
	mkdir -p "${out_dir}"
	"${TOOL_BIN}" --type cpu --language c --container-id "${target_id}" \
		--duration 10 --aggr-interval 1 --freq 99 \
		--output-format json --output-path "${output}" \
		> "${out_dir}/profiler.out" 2> "${out_dir}/profiler.err" &
	native_container_profiler_pid=$!
	wait_until 15 0.2 native_container_samples_ready "${output}" \
		|| fatal "target produced no samples before container deletion"

	native_container_stop || fatal "cannot remove profiled docker target"
	# A distinct comm exposes samples attributed to the deleted target.
	native_container_start docker "${NATIVE_CONTAINER_DOCKER_IMAGE}" "${NATIVE_CONTAINER_FIXTURE}" replacement
	if ! wait "${native_container_profiler_pid}"; then
		fatal "native profiler failed after target container deletion"
	fi
	native_container_profiler_pid=""
	jq -se --arg target "${target_id}" '
		any(.[]; (.profile.sample | length) > 0) and
		all(.[]; .container_id == $target) and
		all(.[]; .profile as $p | $p.sample[].label[]? |
			select($p.string_table[.key] == "comm") |
			$p.string_table[.str] != "replacement")
	' "${output}" > /dev/null \
		|| fatal "deleted target lost its samples or captured the replacement container"
	log_info "native profiler preserved deleted target samples without replacement samples"
}
for native_container_runtime in docker containerd; do
	case $native_container_runtime in
	docker) native_container_image=$NATIVE_CONTAINER_DOCKER_IMAGE ;;
	containerd)
		[[ ${NATIVE_PROFILER_TEST_CONTAINERD:-1} == 1 ]] || continue
		native_container_image=$NATIVE_CONTAINER_CONTAINERD_IMAGE
		;;
	esac
	native_container_runtime_available "$native_container_runtime" "$native_container_image" || continue
	native_container_start "$native_container_runtime" "$native_container_image" "$NATIVE_CONTAINER_FIXTURE"
	run_profile_case "$native_container_runtime" "$native_container_id"
	if [[ $native_container_runtime == docker ]]; then
		run_removed_target_case
	fi
	native_container_stop || fatal "profiling workload cleanup failed"
	native_container_runtimes_tested=$((native_container_runtimes_tested + 1))
done
((native_container_runtimes_tested > 0)) || skip "no docker or containerd runtime was testable"
log_info "native container-ID profiling passed for $native_container_runtimes_tested runtime(s)"
