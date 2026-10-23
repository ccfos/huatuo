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

# Exercise watchdog -> tracepoint -> BPF -> bamai storage without physical NICs.
# The fixture stalls one queue on link-up; it never injects a synthetic event.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly TX_TIMEOUT_MODULE_NAME=ht_tx_timeout_t
readonly TX_TIMEOUT_NETDEV="httx$$"
readonly TX_TIMEOUT_KERNEL_BUILD="/lib/modules/$(uname -r)/build"
readonly TX_TIMEOUT_MODULE_BUILD="${HUATUO_BAMAI_TEST_TMPDIR}/tx-timeout-module"
readonly TX_TIMEOUT_CONTROL="/sys/module/${TX_TIMEOUT_MODULE_NAME}/parameters"
readonly TX_TIMEOUT_EVENT_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/events/netdev_txqueue_timeout"
readonly TX_TIMEOUT_SAMPLE_FILE="${HUATUO_BAMAI_TEST_TMPDIR}/tx-timeout-event.json"
readonly TX_TIMEOUT_PARSE_ERROR="${HUATUO_BAMAI_TEST_TMPDIR}/event-parse.err"
tx_timeout_module_loaded=false

tx_timeout_cleanup() {
	local tx_timeout_status=$?
	trap - EXIT
	huatuo_bamai_stop || tx_timeout_status=1
	if [[ ${tx_timeout_module_loaded} == true ]]; then
		# Taking the device down cancels any pending watchdog before unload.
		ip link set dev "${TX_TIMEOUT_NETDEV}" down || tx_timeout_status=1
		rmmod "${TX_TIMEOUT_MODULE_NAME}" || tx_timeout_status=1
	fi
	exit "${tx_timeout_status}"
}

tx_timeout_observer_attached() {
	# This config enables no other perf-event observer; HTTP readiness is not enough.
	grep -q 'attached BPF and created event pipe' "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
}

tx_timeout_fired() {
	[[ $(< "${TX_TIMEOUT_CONTROL}/timeout_count") == 1 ]]
}

tx_timeout_event_is_valid() {
	: > "${TX_TIMEOUT_PARSE_ERROR}"
	[[ -s "${TX_TIMEOUT_EVENT_FILE}" ]] || return 1
	jq -s -e --arg dev "${TX_TIMEOUT_NETDEV}" --arg driver "${TX_TIMEOUT_MODULE_NAME}" '
		first(.[] | select(
			.tracer_name == "netdev_txqueue_timeout"
			and .tracer_type == "event"
			and (.tracer_id | type == "string" and length > 0)
			and .tracer_data.queue_index == 0
			and .tracer_data.device_name == $dev
			and .tracer_data.driver_name == $driver
			and (.observed_timestamp | type == "string" and length > 0)
		))
	' "${TX_TIMEOUT_EVENT_FILE}" > "${TX_TIMEOUT_SAMPLE_FILE}" 2> "${TX_TIMEOUT_PARSE_ERROR}"
}

assert_tx_timeout_count() {
	local tx_timeout_expected=$1 tx_timeout_actual
	tx_timeout_actual=$(cat "${TX_TIMEOUT_CONTROL}/timeout_count" 2>&1) \
		|| fatal "expected watchdog timeout count ${tx_timeout_expected}; cannot read ${TX_TIMEOUT_CONTROL}/timeout_count: ${tx_timeout_actual}"
	[[ ${tx_timeout_actual} == "${tx_timeout_expected}" ]] \
		|| fatal "watchdog timeout count: expected=${tx_timeout_expected}, actual=${tx_timeout_actual}"
}

assert_tx_timeout_event() {
	local tx_timeout_actual="missing or empty event file"
	tx_timeout_event_is_valid && return 0
	if [[ -s "${TX_TIMEOUT_PARSE_ERROR}" ]]; then
		fatal "expected a valid netdev_txqueue_timeout event; parser: $(< "${TX_TIMEOUT_PARSE_ERROR}")"
	fi
	if [[ -s "${TX_TIMEOUT_EVENT_FILE}" ]]; then
		tx_timeout_actual=$(jq -c -s '.[0:3]' "${TX_TIMEOUT_EVENT_FILE}")
	fi
	fatal "expected netdev_txqueue_timeout event with type=event, device=${TX_TIMEOUT_NETDEV}, queue=0, driver=${TX_TIMEOUT_MODULE_NAME}, nonempty ID and timestamp; actual=${tx_timeout_actual}"
}

trap tx_timeout_cleanup EXIT

require_commands make gcc insmod rmmod ip tc jq curl ss
tracepoint_available net net_dev_xmit_timeout \
	|| skip "net/net_dev_xmit_timeout tracepoint is unavailable"
[[ -r "${TX_TIMEOUT_KERNEL_BUILD}/Makefile" ]] \
	|| skip "install matching kernel headers: ${TX_TIMEOUT_KERNEL_BUILD}/Makefile is missing"
[[ $(< /proc/sys/kernel/modules_disabled) == 0 ]] \
	|| skip "kernel module loading is disabled"
[[ $(< /proc/sys/kernel/panic_on_warn) == 0 ]] \
	|| skip "watchdog may WARN on older kernels; panic_on_warn must be 0"
[[ ! -d /sys/module/${TX_TIMEOUT_MODULE_NAME} ]] \
	|| skip "${TX_TIMEOUT_MODULE_NAME} is already loaded; refusing to modify an existing fixture"

# The listener and readiness URL must share the same test-owned port.
TX_TIMEOUT_HTTP_PORT=$(allocate_available_port) || fatal "failed to allocate TX timeout bamai HTTP port"
readonly TX_TIMEOUT_HTTP_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${TX_TIMEOUT_HTTP_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

mkdir -p "${TX_TIMEOUT_MODULE_BUILD}"
cp "${ROOT_DIR}/integration/testdata/netdev_txqueue_timeout/test_module.mk" "${TX_TIMEOUT_MODULE_BUILD}/Makefile"
cp "${ROOT_DIR}/integration/testdata/netdev_txqueue_timeout/test_tx_timeout.c" \
	"${TX_TIMEOUT_MODULE_BUILD}/${TX_TIMEOUT_MODULE_NAME}.c"
make -C "${TX_TIMEOUT_KERNEL_BUILD}" M="${TX_TIMEOUT_MODULE_BUILD}" modules \
	> "${TX_TIMEOUT_MODULE_BUILD}/build.log" 2>&1 \
	|| fatal "watchdog fixture build failed; see ${TX_TIMEOUT_MODULE_BUILD}/build.log"

if ! insmod "${TX_TIMEOUT_MODULE_BUILD}/${TX_TIMEOUT_MODULE_NAME}.ko" ifname="${TX_TIMEOUT_NETDEV}" \
	2> "${TX_TIMEOUT_MODULE_BUILD}/insmod.err"; then
	if grep -qiE 'operation not permitted|permission denied|key was rejected|required key not available' \
		"${TX_TIMEOUT_MODULE_BUILD}/insmod.err"; then
		skip "module loading denied by permissions or signature policy: $(cat "${TX_TIMEOUT_MODULE_BUILD}/insmod.err")"
	fi
	fatal "watchdog fixture load failed: $(cat "${TX_TIMEOUT_MODULE_BUILD}/insmod.err")"
fi
tx_timeout_module_loaded=true
[[ -r "${TX_TIMEOUT_CONTROL}/timeout_count" ]] \
	|| fatal "watchdog fixture module parameters are missing: ${TX_TIMEOUT_CONTROL}"

# Pin the qdisc rather than depending on the host's default queueing policy.
tc qdisc replace dev "${TX_TIMEOUT_NETDEV}" root pfifo limit 16

integration_huatuo_bamai_start write_netdev_txqueue_timeout_config \
	--region dev --disable-kubelet --log-debug
wait_until 15 0.2 tx_timeout_observer_attached \
	|| fatal "netdev_txqueue_timeout did not attach its BPF event pipe"

assert_tx_timeout_count 0
ip link set dev "${TX_TIMEOUT_NETDEV}" up
wait_until 10 0.1 tx_timeout_fired || assert_tx_timeout_count 1
wait_until 15 0.2 tx_timeout_event_is_valid || assert_tx_timeout_event

# Reparse after stopping the writer so a partial JSON append cannot pass.
assert_huatuo_bamai_shutdown
assert_tx_timeout_count 1
assert_tx_timeout_event
assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
log_info "valid netdev_txqueue_timeout event: $(jq -c . "${TX_TIMEOUT_SAMPLE_FILE}")"
