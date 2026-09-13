#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

[[ ${EUID} -eq 0 ]] || skip "requires root"
command -v ip > /dev/null 2>&1 || skip "iproute2 is required"
command -v go > /dev/null 2>&1 || skip "Go toolchain is required"

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/netns-cookie.XXXXXX")
PROBE_BIN="${WORK_DIR}/netnamespace-cookie"

cleanup_all() {
	process_network_namespace_cleanup
}
trap cleanup_all EXIT

log_info "compiling network namespace cookie probe"
go build -mod=vendor -tags=integration \
	-o "${PROBE_BIN}" \
	"${ROOT_DIR}/integration/testdata/test_netnamespace_cookie.go"

process_network_namespace_setup cookie

expected=$(ip netns exec "${PROCESS_NET_NAMESPACE}" "${PROBE_BIN}" current)
if [[ "${expected}" == "unsupported" ]]; then
	skip "SO_NETNS_COOKIE is not supported by this kernel"
fi

actual=$("${PROBE_BIN}" target "${PROCESS_NET_NAMESPACE_PID}")
assert_eq "${actual}" "${expected}" \
	"public helper must return the target namespace cookie" \
	|| fatal "network namespace cookie mismatch"

log_info "network namespace cookie integration test passed: ${actual}"
