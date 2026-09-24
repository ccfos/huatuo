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

# Verify that net_rx_latency loads the entry point its kernel supports and
# still reports real latency stages:
#   - the kprobe-only object attaches exactly the kprobe entry point,
#   - the object carrying both entry points selects fentry on a kernel that
#     supports it and kprobe otherwise,
#   - both attach exactly one link per hook and report RX_STAGE_TCPV4 for real
#     veth traffic.
# The fixture sets nanosecond thresholds, so the test asserts the stage itself
# instead of waiting for a latency the virtual network may never reach.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_namespace.sh"

# The slow server fixture binds 10.200.1.2, so this test uses the same pair as
# test_events_net_rx_latency.sh. Each test owns its own namespaces, so the
# sequential runner does not conflict.
SERVER_IP="10.200.1.2"
CLIENT_IP="10.200.1.1"
TEST_PORT=19876
EVENT_TIMEOUT=15s

FIXTURE_BIN=""
_server_pid=""
WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/net-rx-tracing.XXXXXX")
readonly GO_CACHE_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-cache"
readonly GO_TMP_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/go-tmp"
cleanup_all() {
	[[ -n "${_server_pid}" ]] && stop_by_pid "${_server_pid}" 2 || true
	tcp_namespace_cleanup
	rm -rf -- "${GO_CACHE_DIR}" "${GO_TMP_DIR}"
}
trap cleanup_all EXIT

tcp_namespace_setup rxlatfb "${SERVER_IP}" "${CLIENT_IP}"
sleep 0.5

FIXTURE_BIN="${WORK_DIR}/net_rx_tracing"
compile_go_fixture() {
	local build_log="${WORK_DIR}/fixture.build.log"

	mkdir -p "${GO_CACHE_DIR}" "${GO_TMP_DIR}"
	log_info "building fixture: test_net_rx_tracing.go"
	GOCACHE="${GO_CACHE_DIR}" \
		GOTMPDIR="${GO_TMP_DIR}" \
		go build -mod=vendor -tags=integration -o "${FIXTURE_BIN}" \
		"${ROOT_DIR}/integration/testdata/test_net_rx_tracing.go" \
		> "${build_log}" 2>&1 \
		|| fatal "failed to build net_rx_tracing:"$'\n'"$(< "${build_log}")"
}

SLOW_TCP_SERVER="${WORK_DIR}/slow-tcp-server"
compile_user_fixture \
	"${ROOT_DIR}/integration/testdata/test_net_rx_latency_user.c" \
	"${SLOW_TCP_SERVER}"

ip netns exec "${TCP_NS_SERVER}" "${SLOW_TCP_SERVER}" \
	> "${WORK_DIR}/testserver.log" 2>&1 &
server_pid=$!
_server_pid="${server_pid}"
sleep 0.5

generate_traffic() {
	for i in $(seq 1 5); do
		ip netns exec "${TCP_NS_CLIENT}" curl -s --connect-timeout 1 --max-time 2 \
			"http://${TCP_NS_SERVER_ADDR}:${TEST_PORT}/" \
			>> "${WORK_DIR}/curl.log" 2>&1 || true
	done
}

# kernel_selects_fentry reports whether the running kernel can attach fentry
# programs to tcp_v4_rcv. It mirrors the loader's own probe: a kernel without
# BTF, or older than the tracing program type, cannot.
kernel_selects_fentry() {
	[[ -r /sys/kernel/btf/vmlinux ]] || return 1
	! kernel_version_le 5 4
}

# run_fixture <mode> <expected-entry-point>
run_fixture() {
	local mode=$1 expected=$2
	local out="${WORK_DIR}/fixture-${mode}.json"
	local err="${WORK_DIR}/fixture-${mode}.err"

	log_info "running fixture in mode ${mode}, expecting ${expected}"
	"${FIXTURE_BIN}" \
		-bpf-dir "${ROOT_DIR}/_output/bpf" \
		-mode "${mode}" \
		-timeout "${EVENT_TIMEOUT}" \
		> "${out}" 2> "${err}" &
	local fixture_pid=$!
	sleep 1

	generate_traffic

	if ! wait "${fixture_pid}"; then
		cat "${err}" >&2 || true
		fatal "net_rx_tracing mode ${mode} failed"
	fi

	local entry_point
	entry_point=$(jq -r 'select(.summary == true) | .entry_point' "${out}")
	[[ "${entry_point}" == "${expected}" ]] \
		|| fatal "mode ${mode} selected ${entry_point}, want ${expected}"

	local duplicates
	duplicates=$(jq -r 'select(.summary == true) | .duplicates' "${out}")
	[[ "${duplicates}" == "0" ]] \
		|| fatal "mode ${mode} reported ${duplicates} repeated packets, want one link per hook"

	local tcpv4_events
	tcpv4_events=$(jq -s --arg saddr "${TCP_NS_CLIENT_ADDR}" --arg daddr "${TCP_NS_SERVER_ADDR}" \
		'[.[] | select(.summary != true)
			| select(.stage == "RX_STAGE_TCPV4")
			| select((.saddr == $saddr and .daddr == $daddr) or (.saddr == $daddr and .daddr == $saddr))] | length' \
		"${out}")
	[[ "${tcpv4_events}" -gt 0 ]] \
		|| fatal "mode ${mode} reported no RX_STAGE_TCPV4 event for ${TCP_NS_CLIENT_ADDR} <-> ${TCP_NS_SERVER_ADDR}"

	log_info "mode ${mode}: entry_point=${entry_point} tcpv4_events=${tcpv4_events} duplicates=${duplicates}"
}

compile_go_fixture

if kernel_selects_fentry; then
	log_info "kernel supports fentry, expecting the fentry entry point"
	kernel_entry_point="tcp_v4_rcv_fentry_prog"
else
	log_info "kernel does not support fentry, expecting the kprobe entry point"
	kernel_entry_point="tcp_v4_rcv_prog"
fi

# The kprobe-only object never carries an fentry program: this is the path
# kernels without fentry support take, and it must keep working.
run_fixture kprobe tcp_v4_rcv_prog

# The object carrying both entry points must select the one the kernel
# supports and still report the hook's stage.
run_fixture auto "${kernel_entry_point}"

if kernel_selects_fentry; then
	run_fixture fentry tcp_v4_rcv_fentry_prog
else
	log_info "skipping the forced fentry mode: the kernel cannot attach fentry programs"
fi

log_info "net_rx_latency tracing variant test passed"
