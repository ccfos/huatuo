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

# Verify NetNamespaceCookieByPID across a real network namespace boundary: the
# helper must return the target namespace cookie and leave the calling thread
# in its original namespace. The unit tests inject a fake setns, so this case
# is the one that exercises the real syscall path.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

require_commands ip
require_commands go

_namespace="htcookie$$"
_sleeper_pid=""
WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/netns-cookie.XXXXXX")

cleanup_all() {
	if [[ -n "${_sleeper_pid}" ]]; then
		stop_by_pid "${_sleeper_pid}" 2 || true
	fi
	ip netns del "${_namespace}" 2> /dev/null || true
	rm -rf "${WORK_DIR}"
}
trap cleanup_all EXIT

namespace_setup "${_namespace}"

# ip netns exec stays in the root pid namespace, so /proc/<pid>/ns/net points
# at the target namespace while the probe runs in the caller's namespace.
ip netns exec "${_namespace}" sleep 300 &
_sleeper_pid=$!
sleep 0.2

if [[ "$(readlink "/proc/${_sleeper_pid}/ns/net")" == "$(readlink /proc/self/ns/net)" ]]; then
	fatal "sleeper ${_sleeper_pid} is still in the caller namespace"
fi

COOKIE_BIN="${WORK_DIR}/test_netnamespace_cookie"
go build -mod=vendor -o "${COOKIE_BIN}" \
	"${ROOT_DIR}/integration/testdata/test_netnamespace_cookie.go"

output=$("${COOKIE_BIN}" -pid "${_sleeper_pid}")
log_info "cookie probe output: ${output}"

if [[ ${output} != *"restored_namespace="* ]]; then
	fatal "probe did not report the restored namespace: ${output}"
fi

log_info "NetNamespaceCookieByPID restored the caller namespace"
