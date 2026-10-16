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

# Exercise direct-only task attribution across a real cgroup v2 hierarchy.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly LOAD_STATS_TEST_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/cgroup-v2-load-stats.log"
readonly LOAD_STATS_GO_CACHE_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-cache"
readonly LOAD_STATS_GO_TMP_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-tmp"

cleanup() {
	rm -rf -- "${LOAD_STATS_GO_CACHE_DIR}" "${LOAD_STATS_GO_TMP_DIR}"
}
trap cleanup EXIT

[[ ${EUID} -eq 0 ]] || skip "requires root"
# A temporary v2 mount can leave unified entries in /proc/PID/cgroup on v1 hosts.
[[ -r /sys/fs/cgroup/cgroup.controllers ]] || skip "requires native cgroup v2"
command -v go > /dev/null || skip "go command is not installed"
[[ -r /sys/kernel/btf/vmlinux ]] \
	|| skip "kernel BTF is not readable: /sys/kernel/btf/vmlinux"
kernel_version_le 5 7 && skip "BPF task iterator requires Linux 5.8 or newer"
[[ -r "${ROOT_DIR}/bpf/cgroup_v2_load_stats.o" ]] \
	|| fatal "cgroup v2 load stats BPF object is missing"

mkdir -p "${LOAD_STATS_GO_CACHE_DIR}" "${LOAD_STATS_GO_TMP_DIR}"

if ! HUATUO_CGROUP_V2_LOAD_BPF_DIR="${ROOT_DIR}/bpf" \
	HUATUO_CGROUP_V2_LOAD_ROOT=/sys/fs/cgroup \
	GOCACHE="${LOAD_STATS_GO_CACHE_DIR}" \
	GOTMPDIR="${LOAD_STATS_GO_TMP_DIR}" \
	go test -mod=vendor -tags=integration \
	./integration/testdata/cgroup_v2_load_stats_test.go \
	-run '^TestLoadStatsLiveTaskIterator$' -count=1 \
	> "${LOAD_STATS_TEST_LOG}" 2>&1; then
	sed -n '1,240p' "${LOAD_STATS_TEST_LOG}" >&2
	fatal "cgroup v2 task iterator integration test failed"
fi

log_info "cgroup v2 task iterator load stats verified"
