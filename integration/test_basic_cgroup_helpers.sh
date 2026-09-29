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

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_cgroup.sh"

readonly CGROUP_HELPER_ROOT="${HUATUO_BAMAI_TEST_TMPDIR}/cgroup helpers"
readonly CGROUP_HELPER_CONTAINER_ID=$(printf '%064d' 1)
require_commands find

# Model mount availability without changing the host cgroup hierarchy.
findmnt() {
	case " $* " in
	*' -t cgroup '*)
		[[ -n "${cgroup_helper_v1}" ]] || return 1
		printf '%s\n' "${cgroup_helper_v1}"
		;;
	*' -t cgroup2 '*)
		[[ -n "${cgroup_helper_v2}" ]] || return 1
		printf '%s\n' "${cgroup_helper_v2}"
		;;
	*) return 1 ;;
	esac
}

cgroup_helper_v1="${CGROUP_HELPER_ROOT}/memory"
cgroup_helper_v2="${CGROUP_HELPER_ROOT}/unified"
mkdir -p "${cgroup_helper_v1}" "${cgroup_helper_v2}"
assert_eq "$(cgroup_memory_root)" "${cgroup_helper_v1}" "hybrid host prefers v1 memory controller"
assert_eq "$(cgroup_create fixture)" "${cgroup_helper_v1}/fixture" "create uses memory mount"
cgroup_delete "${cgroup_helper_v1}/fixture"
cgroup_helper_v1=""
assert_eq "$(cgroup_memory_root)" "${cgroup_helper_v2}" "v2 fallback"
cgroup_helper_v2=""
if cgroup_memory_root; then
	fatal "missing memory mount was accepted"
fi

for cgroup_helper_name in "${CGROUP_HELPER_CONTAINER_ID}" \
	"docker-${CGROUP_HELPER_CONTAINER_ID}.scope" \
	"cri-containerd-${CGROUP_HELPER_CONTAINER_ID}.scope" \
	"crio-${CGROUP_HELPER_CONTAINER_ID}.scope"; do
	mkdir "${CGROUP_HELPER_ROOT}/${cgroup_helper_name}"
	assert_eq "$(cgroup_find_container "${CGROUP_HELPER_ROOT}" "${CGROUP_HELPER_CONTAINER_ID}")" \
		"${CGROUP_HELPER_ROOT}/${cgroup_helper_name}" "container directory lookup"
	rmdir "${CGROUP_HELPER_ROOT}/${cgroup_helper_name}"
done

for cgroup_helper_id in "${CGROUP_HELPER_CONTAINER_ID}" '' '*'; do
	if cgroup_find_container "${CGROUP_HELPER_ROOT}" "${cgroup_helper_id}"; then
		fatal "missing or invalid container ID was accepted: ${cgroup_helper_id}"
	fi
done

log_info "memory mount selection and container cgroup lookup passed"
