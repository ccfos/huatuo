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

# Called by the runner inside a per-case mount namespace.
set -euo pipefail

suite=$1 namespaces=$2 test_script=$3
source "$ROOT_DIR/integration/lib.sh"
test_running=false test_exit_code=null e2e_service_ready=false worker_signal_code=0
worker_finalize() {
	local status=$? cleanup_status=0
	trap - EXIT
	trap '' INT TERM
	RUNNER_CLEANUP_REASON=""
	if [[ $e2e_service_ready == true ]]; then
		e2e_test_teardown || cleanup_status=$?
	else
		# Preparation can exit before a service log exists; still stop partial starts.
		integration_test_cleanup "$HUATUO_BAMAI_TEST_TMPDIR" || cleanup_status=$?
	fi
	# Preserve the test process exit code even when preparation or cleanup fails.
	printf 'exit_code=%s\ncleanup_status=%s\ncleanup_reason:\n%s' \
		"$test_exit_code" "$cleanup_status" "$RUNNER_CLEANUP_REASON" \
		> "$HUATUO_BAMAI_TEST_TMPDIR/execution.log.tmp" \
		&& mv "$HUATUO_BAMAI_TEST_TMPDIR/execution.log.tmp" "$HUATUO_BAMAI_TEST_TMPDIR/execution.log" || status=1
	((cleanup_status == 0)) || status=1
	exit "$status"
}
worker_cancel() {
	worker_signal_code=$1
	trap '' INT TERM
	# Foreground execution defers this trap until Bash has collected the raw
	# test status. The runner bounds shutdown by signalling the whole group.
	[[ $test_running == true ]] || exit "$worker_signal_code"
}

if [[ $suite == e2e ]]; then source "$ROOT_DIR/e2e/lib.sh"; fi
trap worker_finalize EXIT
trap 'worker_cancel 130' INT
trap 'worker_cancel 143' TERM
mount --make-rprivate /
runtime_dir="${HUATUO_BAMAI_TEST_TMPDIR}/run"
host_runtime_dir="${HUATUO_BAMAI_TEST_TMPDIR}/host-run"
mkdir -p "${runtime_dir}" "${host_runtime_dir}"
mount --rbind /var/run "${host_runtime_dir}"
# Preserve runtime sockets and other host services without sharing Huatuo locks.
shopt -s dotglob nullglob
for entry in "${host_runtime_dir}/"*; do
	name=${entry##*/}
	case ${name} in
	huatuo-bamai.pid | huatuo-apiserver.pid | huatuo-toolstream.sock) continue ;;
	esac
	ln -s "${entry}" "${runtime_dir}/${name}"
done
mount --bind "${runtime_dir}" /var/run

if [[ $namespaces == "mount net" ]]; then
	require_commands ip
	ip link set lo up || fatal "cannot enable loopback in case network namespace"
	# Host proxies are unreachable from a private network namespace.
	unset http_proxy https_proxy all_proxy ftp_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY FTP_PROXY no_proxy NO_PROXY
fi
prepare_execution_ports
unset HUATUO_BAMAI_DEFAULT_ADDR
huatuo_bamai_initialize_address
if [[ $suite == e2e ]]; then
	e2e_huatuo_bamai_start
	e2e_service_ready=true
fi
# Foreground execution avoids losing child status when a signal interrupts wait.
# An empty reason owner binds the channel to the test's main shell on lib.sh load.
test_running=true
test_exit_code=0
RUNNER_REASON_OWNER= bash "$test_script" || test_exit_code=$?
test_running=false
if ((worker_signal_code)); then exit "$worker_signal_code"; fi
exit "$test_exit_code"
