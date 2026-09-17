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

# Compare the BTF-derived subsystem mapping with identities read independently
# from live kernel objects by a test-only BPF program.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly SUBSYS_ID_FIXTURE="${ROOT_DIR}/integration/testdata/cgroup_subsys_id.bpf.c"
readonly SUBSYS_ID_OBJECT="${HUATUO_BAMAI_TEST_TMPDIR}/cgroup_subsys_id.bpf.o"
readonly SUBSYS_ID_TEST_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/cgroup-subsys-id.log"
readonly SUBSYS_ID_GO_CACHE_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-cache"
readonly SUBSYS_ID_GO_TMP_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-tmp"
readonly SUBSYS_ID_CGROUP_NAME="cgroup-subsys-id-probe"

subsys_id_cgroup_parent=""

cleanup() {
	rm -rf -- "${SUBSYS_ID_GO_CACHE_DIR}" "${SUBSYS_ID_GO_TMP_DIR}"
	[[ -n "${subsys_id_cgroup_parent}" ]] || return 0
	rmdir "${subsys_id_cgroup_parent}/${SUBSYS_ID_CGROUP_NAME}" 2> /dev/null || true
	rmdir "${subsys_id_cgroup_parent}" 2> /dev/null || true
}
trap cleanup EXIT

[[ $EUID -eq 0 ]] || skip "requires root"
command -v go > /dev/null || skip "go command is not installed"
command -v clang > /dev/null || skip "clang command is not installed"
[[ -x "${ROOT_DIR}/build/clang.sh" ]] \
	|| fatal "BPF compiler wrapper is not executable: ${ROOT_DIR}/build/clang.sh"
[[ -r /sys/kernel/btf/vmlinux ]] \
	|| skip "kernel BTF is not readable: /sys/kernel/btf/vmlinux"

if [[ -r /sys/fs/cgroup/cgroup.controllers ]]; then
	readonly SUBSYS_ID_CGROUP_ROOT="/sys/fs/cgroup"
	readonly SUBSYS_ID_SYNC_SYMBOL="memory_current_read"
	readonly SUBSYS_ID_NOTIFY_FILE="memory.current"
elif [[ -d /sys/fs/cgroup/memory ]]; then
	readonly SUBSYS_ID_CGROUP_ROOT="/sys/fs/cgroup/memory"
	readonly SUBSYS_ID_SYNC_SYMBOL="cgroup_clone_children_read"
	readonly SUBSYS_ID_NOTIFY_FILE="cgroup.clone_children"
else
	skip "supported cgroup v1/v2 hierarchy is not available"
fi

kprobe_available "${SUBSYS_ID_SYNC_SYMBOL}" \
	|| skip "${SUBSYS_ID_SYNC_SYMBOL} is not available for kprobe"

subsys_id_cgroup_parent="${SUBSYS_ID_CGROUP_ROOT}/huatuo-subsys-id-${BASHPID}-${RANDOM}"
if ! mkdir "${subsys_id_cgroup_parent}" 2> "${HUATUO_BAMAI_TEST_TMPDIR}/cgroup-mkdir.log"; then
	skip "cgroup hierarchy is not writable: ${SUBSYS_ID_CGROUP_ROOT}"
fi
if [[ ${SUBSYS_ID_SYNC_SYMBOL} == memory_current_read ]]; then
	# Enable memory so the child exposes memory.current.
	echo +memory > "${subsys_id_cgroup_parent}/cgroup.subtree_control" \
		|| skip "memory controller cannot be enabled in test cgroup"
fi
mkdir "${subsys_id_cgroup_parent}/${SUBSYS_ID_CGROUP_NAME}"
readonly SUBSYS_ID_NOTIFY_PATH="${subsys_id_cgroup_parent}/${SUBSYS_ID_CGROUP_NAME}/${SUBSYS_ID_NOTIFY_FILE}"
[[ -r "${SUBSYS_ID_NOTIFY_PATH}" ]] \
	|| skip "cgroup notification file is not readable: ${SUBSYS_ID_NOTIFY_PATH}"

compile_bpf_fixture "${SUBSYS_ID_FIXTURE}" "${SUBSYS_ID_OBJECT}"

mkdir -p "${SUBSYS_ID_GO_CACHE_DIR}" "${SUBSYS_ID_GO_TMP_DIR}"

if ! HUATUO_CGROUP_SUBSYS_ID_OBJECT="${SUBSYS_ID_OBJECT}" \
	HUATUO_CGROUP_SUBSYS_ID_NOTIFY_PATH="${SUBSYS_ID_NOTIFY_PATH}" \
	HUATUO_CGROUP_SUBSYS_ID_SYMBOL="${SUBSYS_ID_SYNC_SYMBOL}" \
	GOCACHE="${SUBSYS_ID_GO_CACHE_DIR}" \
	GOTMPDIR="${SUBSYS_ID_GO_TMP_DIR}" \
	go test -mod=vendor -tags=integration ./internal/pod \
	-run '^TestCgroupSubsysIDIntegration$' -count=1 \
	> "${SUBSYS_ID_TEST_LOG}" 2>&1; then
	sed -n '1,240p' "${SUBSYS_ID_TEST_LOG}" >&2
	fatal "cgroup subsystem ID integration test failed"
fi

log_info "cgroup subsystem ID mapping verified"
