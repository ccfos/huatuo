#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

readonly MEMORY_BUNDLE="${PROFILER_TOOL_DIR}/memray"
readonly MEMORY_FIXTURE="${ROOT_DIR}/integration/testdata/test_profiler_python_memory.py"
readonly MEMORY_FIXTURE_LOG="${HUATUO_BAMAI_TEST_TMPDIR}/memory-fixture.log"
readonly MEMORY_FIXTURE_ERR="${HUATUO_BAMAI_TEST_TMPDIR}/memory-fixture.err"
readonly MEMORY_LIBRARY="${HUATUO_BAMAI_TEST_TMPDIR}/memory-fixture.so"

memory_require_runtime() {
	local version=$1 bundle=${2:-${MEMORY_BUNDLE}}
	# Only unsupported versions may skip; a missing supported runtime is a
	# packaging failure, not an unavailable external prerequisite.
	case "${version}" in
	3.7 | 3.8 | 3.9 | 3.10 | 3.11 | 3.12 | 3.13 | 3.14) ;;
	*) skip "Python ${version} is not supported by the pinned Memray bundle" ;;
	esac
	local runtime="${bundle}/runtimes/py${version}/python/memray"
	compgen -G "${runtime}/_memray*.so" > /dev/null \
		|| fatal "Memray runtime for Python ${version} missing: ${runtime}; run make prep-memray-bundle"
	compgen -G "${runtime}/_inject*.so" > /dev/null \
		|| fatal "Memray injector for Python ${version} missing: ${runtime}; run make prep-memray-bundle"
}

# Node names can differ from the runner's isolated UTS hostname.
memory_node_for_local_addresses() {
	local nodes_file=$1 addresses_file=$2
	jq -er --slurpfile addresses "${addresses_file}" '
		[$addresses[0][] | .addr_info[]? | .local] as $local_addresses
		| [.items[] | select(any(.status.addresses[]?;
			.type == "InternalIP" and
			(.address as $ip | $local_addresses | index($ip) != null)))
			| .metadata.name]
		| if length == 1 then .[0]
		  else error("expected exactly one Kubernetes node matching local interface addresses") end
	' "${nodes_file}"
}

memory_compile_fixture() {
	compile_user_fixture \
		"${ROOT_DIR}/integration/testdata/test_profiler_python_memory.user.c" \
		"${MEMORY_LIBRARY}" -shared -fPIC
}

memory_fixture_has_ack() {
	[[ "${MEMORY_TARGET_PID}" =~ ^[1-9][0-9]*$ ]] \
		|| fatal "invalid Python fixture PID: ${MEMORY_TARGET_PID}"
	kill -0 "${MEMORY_TARGET_PID}" 2> /dev/null \
		|| fatal "Python fixture exited: $(cat "${MEMORY_FIXTURE_ERR}")"
	grep -qx "$1" "${MEMORY_FIXTURE_LOG}"
}

memory_wait_for_ack() {
	wait_until 10 0.1 memory_fixture_has_ack "$1" \
		|| fatal "missing fixture acknowledgement '$1': $(cat "${MEMORY_FIXTURE_LOG}" "${MEMORY_FIXTURE_ERR}")"
}

memory_request_phase() {
	kill -USR1 "${MEMORY_TARGET_PID}"
	memory_wait_for_ack "$1"
}
