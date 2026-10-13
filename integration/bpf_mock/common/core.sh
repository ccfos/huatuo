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

is_bpf_mock_test() {
	case ${1##*/} in
	test_*_mock.sh | test_*_mock_container.sh) return 0 ;;
	*) return 1 ;;
	esac
}

require_bpf_mock_platform() {
	if kernel_version_le 4 17; then
		skip "BPF mock fixtures require Linux 4.18 or newer"
	fi
	case $(uname -m) in
	x86_64 | aarch64) ;;
	*) skip "BPF mock fixtures require x86_64 or aarch64" ;;
	esac
}

# This Ubuntu release rejects mock module kprobes with EADDRNOTAVAIL.
skip_known_mock_kprobe_kernel() {
	if [[ $(uname -r) == 5.4.0-216-generic ]]; then
		skip "mock module kprobe attachment is unavailable on Ubuntu kernel 5.4.0-216-generic (EADDRNOTAVAIL); see ebpf-integration-mock_en.md"
	fi
}

build_bpf_mock_module() {
	local feature=$1 fixture_dir=$2
	shift 2
	local source_dir="${ROOT_DIR}/integration/bpf_mock/${feature}"
	local kernel_build_dir=${KERNEL_BUILD_DIR:-/lib/modules/$(uname -r)/build}
	local build_log="${HUATUO_BAMAI_TEST_TMPDIR}/${feature}-mock-build.log"
	[[ -d "${kernel_build_dir}" ]] || skip "kernel build directory is missing: ${kernel_build_dir}"
	mkdir -p "${fixture_dir}"
	cp "${ROOT_DIR}/integration/bpf_mock/common/Makefile" "${source_dir}/Kbuild" \
		"${source_dir}/${feature}_mock_kmod.c" \
		"${source_dir}/${feature}_mock.h" "$@" "${fixture_dir}/"
	make -C "${fixture_dir}" KERNEL_BUILD_DIR="${kernel_build_dir}" > "${build_log}" 2>&1 \
		|| fatal "${feature} fixture module build failed; see ${build_log}"
}

# Record loaded_module after insmod so EXIT cleanup also owns readiness failures.
load_bpf_mock_module() {
	local feature=$1 fixture_dir=$2
	insmod "${fixture_dir}/${feature}_mock.ko"
	loaded_module="${feature}_mock"
	wait_until 5 0.1 test -e "/dev/${feature}_mock" || fatal "${feature} mock device missing"
}

create_bpf_mock_namespace() {
	kubectl --request-timeout=10s create namespace "$1"
	bpf_mock_namespace=$1
	bpf_mock_pod=$2
}

cleanup_bpf_mock() {
	local status=$1 loaded_module=${2:-}
	if [[ ${status} -ne 0 && ${status} -ne 77 && -n ${bpf_mock_namespace:-} ]]; then
		describe_bpf_mock_pod "${bpf_mock_namespace}" "${bpf_mock_pod}" || true
	fi
	huatuo_bamai_stop || status=1
	if [[ -n ${bpf_mock_namespace:-} ]]; then
		delete_bpf_mock_namespace "${bpf_mock_namespace}" || status=1
	fi
	if [[ -n ${loaded_module} ]]; then rmmod "${loaded_module}" || status=1; fi
	return "${status}"
}

bpf_mock_hook_attached() {
	local kind=$1 target=$2
	awk -v kind="${kind}" -v target="${target}" '
  kind == "tracepoint" && index($0, "attached BPF tracepoint") && index($0, "attach_target=\"" target "\"") { found = 1 }
  kind != "tracepoint" && index($0, "attached BPF program") && index($0, "attach_type=\"" kind "\"") && index($0, "symbol=\"" target "\"") { found = 1 }
  END { exit !found }
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
}

await_bpf_mock_attached() {
	local map=$1 hook
	shift
	for hook in "$@"; do
		wait_until 15 0.1 bpf_mock_hook_attached "${hook%%:*}" "${hook#*:}" || fatal "BPF did not attach to ${hook}"
	done
	[[ -n ${map} ]] || return 0
	wait_until 15 0.1 grep -q "attached BPF and created event pipe.*map_name=\"${map}\"" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" || fatal "${map} event pipe did not start"
}

# Wait for collector work and storage flushing before checking final events.
stop_bpf_mock_daemon() {
	local pid
	pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	stop_and_wait_by_pid "${pid}" 60 || fatal "huatuo-bamai did not shut down cleanly"
	rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
}

# Comments for reviewer:
# Typed Go comparisons preserve exact 64-bit CSS values and distinguish missing
# fields from valid zero values. Checkers validate saved outputs; scripts own
# setup, triggering, output collection, cleanup, and propagation of failures.
# Checkers are built with the product so test machines do not need Go.
bpf_mock_checker_path() {
	local feature=$1
	local checker=${BPF_MOCK_CHECKER_BIN:-${ROOT_DIR}/_output/tests/${feature}-mock-checker}
	[[ -f ${checker} && -x ${checker} ]] \
		|| fatal "mock checker is missing or not executable: ${checker}; run make build on the build machine and transfer _output/tests/"
	printf '%s\n' "${checker}"
}

run_bpf_mock_checker() {
	local feature=$1
	shift
	local checker
	checker=$(bpf_mock_checker_path "${feature}") || return
	local checker_log="${HUATUO_BAMAI_TEST_TMPDIR}/mock-event-checker.log"
	"${checker}" "$@" > "${checker_log}" 2>&1 || fatal "${feature} event verification failed; see ${checker_log}"
	cat "${checker_log}"
}
