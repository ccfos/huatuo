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

# Verify huatuo-bamai's local-only correlation contract:
# - correlation works while standalone dropwatch is blacklisted;
# - enabling standalone dropwatch stores raw drops independently of finalized
#   retransmission results;
# - cross-netns software evidence remains unknown with an explicit reason;
# - SIGTERM asks tcpshark to finalize a pending local result before it exits.

set -euo pipefail

source "$(dirname "$0")/env.sh"
source "${ROOT_DIR}/integration/lib.sh"

BPF_OBJ="${ROOT_DIR}/_output/bpf/tcp_retransmit.o"
DROPWATCH_OBJ="${ROOT_DIR}/_output/bpf/dropwatch.o"
TEST_PORT=19997
PAYLOAD_SIZE=2097152 # 2 MB

NS_S="ts_drop_s"
NS_C="ts_drop_c"
VETH_S="vs_drop"
VETH_C="vc_drop"
S_ADDR="10.99.0.1"
C_ADDR="10.99.0.2"
NET_MASK="24"

[[ ${EUID} -eq 0 ]] || skip "requires root"
command -v jq > /dev/null 2>&1 || skip "jq is required"
command -v tc > /dev/null 2>&1 || skip "tc is required"
require_python3
[[ -x "${HUATUO_BAMAI_BIN}" ]] || fatal "huatuo-bamai binary not found: ${HUATUO_BAMAI_BIN}"
[[ -f "${BPF_OBJ}" ]] || fatal "tcpshark BPF object not found: ${BPF_OBJ}"
[[ -f "${DROPWATCH_OBJ}" ]] || fatal "dropwatch BPF object not found: ${DROPWATCH_OBJ}"

if ! iptables -m connbytes -h 2>&1 | grep -q connbytes; then
	log_info "SKIP: iptables connbytes module not available on this kernel"
	exit 0
fi

OUTPUT_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/tcp-retrans-correlation.XXXXXX")
CONFIG_FILE="${OUTPUT_DIR}/correlation.conf"
BAMAI_PID=""
SRV_PID=""
CLI_PID=""
ACTIVE_DROP_NS=""
ACTIVE_DROP_CHAIN=""
ACTIVE_DROP_RANGE=""
ACTIVE_NETEM_NS=""
ACTIVE_NETEM_DEV=""

remove_drop_rule() {
	[[ -n "${ACTIVE_DROP_NS}" ]] || return 0
	ip netns exec "${ACTIVE_DROP_NS}" iptables -D "${ACTIVE_DROP_CHAIN}" \
		-p tcp --sport "${TEST_PORT}" \
		-m connbytes --connbytes "${ACTIVE_DROP_RANGE}" --connbytes-dir reply \
		--connbytes-mode packets -j DROP 2> /dev/null || true
	ACTIVE_DROP_NS=""
	ACTIVE_DROP_CHAIN=""
	ACTIVE_DROP_RANGE=""
}

remove_netem_loss() {
	[[ -n "${ACTIVE_NETEM_NS}" ]] || return 0
	ip netns exec "${ACTIVE_NETEM_NS}" tc qdisc del dev "${ACTIVE_NETEM_DEV}" root \
		2> /dev/null || true
	ACTIVE_NETEM_NS=""
	ACTIVE_NETEM_DEV=""
}

cleanup() {
	local code=$?
	[[ -n "${BAMAI_PID:-}" ]] && stop_by_pid "${BAMAI_PID}" 10 || true
	[[ -n "${SRV_PID:-}" ]] && kill "${SRV_PID}" 2> /dev/null || true
	[[ -n "${CLI_PID:-}" ]] && kill "${CLI_PID}" 2> /dev/null || true
	remove_drop_rule
	remove_netem_loss
	ip netns del "${NS_S}" 2> /dev/null || true
	ip netns del "${NS_C}" 2> /dev/null || true
	if ((code == 0)); then
		rm -rf "${OUTPUT_DIR}"
	else
		log_error "artifacts preserved at ${OUTPUT_DIR}"
	fi
}
trap cleanup EXIT

setup_topology() {
	ip netns add "${NS_S}" || fatal "failed to create server netns"
	ip netns add "${NS_C}" || fatal "failed to create client netns"

	ip link add "${VETH_S}" type veth peer name "${VETH_C}"
	ip link set "${VETH_S}" netns "${NS_S}"
	ip link set "${VETH_C}" netns "${NS_C}"

	ip netns exec "${NS_S}" ip addr add "${S_ADDR}/${NET_MASK}" dev "${VETH_S}"
	ip netns exec "${NS_S}" ip link set "${VETH_S}" up
	ip netns exec "${NS_S}" ip link set lo up
	ip netns exec "${NS_C}" ip addr add "${C_ADDR}/${NET_MASK}" dev "${VETH_C}"
	ip netns exec "${NS_C}" ip link set "${VETH_C}" up
	ip netns exec "${NS_C}" ip link set lo up

	ip netns exec "${NS_S}" ip link set dev "${VETH_S}" gso_max_segs 1
	ip netns exec "${NS_C}" ethtool -K "${VETH_C}" gro off 2> /dev/null || true
}

install_drop_rule() {
	local namespace=$1 chain=$2 packet_range=$3
	remove_drop_rule
	ip netns exec "${namespace}" iptables -I "${chain}" 1 \
		-p tcp --sport "${TEST_PORT}" \
		-m connbytes --connbytes "${packet_range}" --connbytes-dir reply \
		--connbytes-mode packets -j DROP
	ACTIVE_DROP_NS="${namespace}"
	ACTIVE_DROP_CHAIN="${chain}"
	ACTIVE_DROP_RANGE="${packet_range}"
}

install_netem_loss() {
	local namespace=$1 device=$2 percent=$3
	remove_netem_loss
	ip netns exec "${namespace}" tc qdisc replace dev "${device}" root \
		netem loss "${percent}"
	ACTIVE_NETEM_NS="${namespace}"
	ACTIVE_NETEM_DEV="${device}"
}

start_transfer() {
	ip netns exec "${NS_S}" timeout 10 python3 \
		"${ROOT_DIR}/integration/testdata/tcp_server.py" \
		--listen-address "${S_ADDR}" --port "${TEST_PORT}" \
		--payload-bytes "${PAYLOAD_SIZE}" > /dev/null 2>&1 &
	SRV_PID=$!
	sleep 0.5

	ip netns exec "${NS_C}" timeout 8 bash -c "exec 3<>/dev/tcp/${S_ADDR}/${TEST_PORT}; cat <&3 >/dev/null" 2> /dev/null &
	CLI_PID=$!
}

finish_transfer() {
	wait "${SRV_PID}" 2> /dev/null || true
	wait "${CLI_PID}" 2> /dev/null || true
	SRV_PID=""
	CLI_PID=""
}

run_transfer() {
	start_transfer
	sleep 10
	finish_transfer
}

tcp_retrans_count() {
	ip netns exec "${NS_S}" nstat -az 2> /dev/null \
		| awk '$1 == "TcpRetransSegs" { print $2; found = 1 } END { if (!found) print 0 }'
}

wait_for_retrans_after() {
	local baseline=$1 attempt
	for ((attempt = 0; attempt < 400; attempt++)); do
		[[ "$(tcp_retrans_count)" -gt "${baseline}" ]] && return 0
		sleep 0.02
	done
	return 1
}

write_bamai_config() {
	local events_dir=$1 standalone_dropwatch=$2 dropwatch_blacklist=""
	if [[ "${standalone_dropwatch}" != "true" ]]; then
		dropwatch_blacklist=', "dropwatch"'
	fi

	cat > "${CONFIG_FILE}" <<- EOF
		BlackList = [
		    "ascend_npu", "cpuidle", "cpusys", "dload", "ethtool",
		    "hungtask", "iolatency", "iotracing", "memburst",
		    "memory_free", "memory_reclaim", "memory_reclaim_events",
		    "metax_gpu", "net_rx_latency", "netdev_bonding_lacp",
		    "netdev_events", "netdev_hw", "netdev_txqueue_timeout",
		    "netstat_hw", "oom", "ras", "reschedipi", "runqlat", "softirq",
		    "softirq_tracing", "softlockup"${dropwatch_blacklist}
		]

		[EventTracing.Dropwatch]
		    Filter = "tcp and port ${TEST_PORT}"
		    MaxEventsPerSecond = 0

		[EventTracing.TCPRetransmit]
		    EnableDropwatchCorrelation = true

		[Storage.LocalFile]
		    Path = "${events_dir}"
	EOF
}

producers_ready() {
	local standalone_dropwatch=$1 log_file="${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"
	grep -q "connected tool=tcpshark" "${log_file}" || return 1
	if [[ "${standalone_dropwatch}" == "true" ]]; then
		grep -q "connected tool=dropwatch" "${log_file}"
	else
		! grep -q "connected tool=dropwatch" "${log_file}"
	fi
}

start_bamai() {
	local events_dir=$1 standalone_dropwatch=$2
	write_bamai_config "${events_dir}" "${standalone_dropwatch}"
	huatuo_bamai_start \
		--config-dir "${OUTPUT_DIR}" \
		--config "$(basename "${CONFIG_FILE}")" \
		--bpf-dir "${ROOT_DIR}/_output/bpf" \
		--tools-bin-dir "${ROOT_DIR}/_output/bin" \
		--region integration \
		--disable-kubelet
	BAMAI_PID=$(cat "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	wait_until 20 1 producers_ready "${standalone_dropwatch}" \
		|| fatal "expected tcpshark/dropwatch producer set did not start"
}

stop_bamai() {
	local status=0 deadline=$((SECONDS + 10)) state=""
	kill -TERM "${BAMAI_PID}" 2> /dev/null || true
	while kill -0 "${BAMAI_PID}" 2> /dev/null; do
		state=$(ps -o stat= -p "${BAMAI_PID}" 2> /dev/null || true)
		[[ -z "${state}" || "${state}" == Z* ]] && break
		if ((SECONDS >= deadline)); then
			kill -KILL "${BAMAI_PID}" 2> /dev/null || true
			wait "${BAMAI_PID}" 2> /dev/null || true
			rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
			BAMAI_PID=""
			return 1
		fi
		sleep 0.1
	done
	wait "${BAMAI_PID}" || status=$?
	rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid"
	BAMAI_PID=""
	return "${status}"
}

matched_retrans_count() {
	local events_dir=$1
	local events_file="${events_dir}/tcp_retransmit"
	if [[ ! -s "${events_file}" ]]; then
		echo 0
		return
	fi
	jq -s --argjson port "${TEST_PORT}" '
		[.[]
			 | select(.tracer_name == "tcp_retransmit")
			 | .tracer_data
			 | select(.tcp_sport == $port)
			 | select(.drop_location == "host_software")
			 | select(((.drop_stack // "") | length) > 0)]
		| length
	' "${events_file}"
}

finalized_retrans_count() {
	local events_dir=$1
	local events_file="${events_dir}/tcp_retransmit"
	if [[ ! -s "${events_file}" ]]; then
		echo 0
		return
	fi
	jq -s --argjson port "${TEST_PORT}" '
		[.[]
			 | select(.tracer_name == "tcp_retransmit")
			 | .tracer_data
			 | select(.tcp_sport == $port)
			 | select(
			     (.drop_location == "host_software"
			       and ((.drop_stack // "") | length > 0)
			       and (has("dropwatch_perf_status") | not))
			     or
			     (.drop_location == "unknown"
			       and (((.correlation_reasons // []) | length) > 0)
			       and has("dropwatch_perf_status")
			       and (.dropwatch_perf_status.drained_through_ktime_ns
			         >= .ktime_ns)
			       and ((.correlation_reasons // [])
			         | index("perf_frontier_incomplete") == null)))]
		| length
	' "${events_file}"
}

cross_netns_unknown_count() {
	local events_dir=$1
	local events_file="${events_dir}/tcp_retransmit"
	if [[ ! -s "${events_file}" ]]; then
		echo 0
		return
	fi
	jq -s --argjson port "${TEST_PORT}" '
		[.[]
			 | select(.tracer_name == "tcp_retransmit")
			 | .tracer_data
			 | select(.tcp_sport == $port)
			 | select(.drop_location == "unknown")
			 | select((.correlation_reasons // [])
			     | index("cross_netns_candidate") != null)
			 | select(.dropwatch_perf_status.drained_through_ktime_ns
			     >= .ktime_ns)
			 | select((.correlation_reasons // [])
			     | index("perf_frontier_incomplete") == null)]
		| length
	' "${events_file}"
}

raw_drop_count() {
	local events_dir=$1
	local events_file="${events_dir}/dropwatch"
	if [[ ! -s "${events_file}" ]]; then
		echo 0
		return
	fi
	jq -s --argjson port "${TEST_PORT}" '
		[.[]
		 | select(.tracer_name == "dropwatch")
		 | .tracer_data
		 | select(.layers.tcp.sport == $port or .layers.tcp.dport == $port)]
		| length
	' "${events_file}"
}

count_greater_than() {
	local counter=$1 events_dir=$2 baseline=$3
	[[ "$("${counter}" "${events_dir}")" -gt "${baseline}" ]]
}

setup_topology

CASE1_EVENTS="${OUTPUT_DIR}/dropwatch-blacklisted-events"
log_info "correlation with standalone dropwatch blacklisted"
start_bamai "${CASE1_EVENTS}" false
sleep 1
if grep -q "connected tool=dropwatch" "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log"; then
	fatal "standalone dropwatch connected while blacklisted"
fi
CASE1_BEFORE=$(matched_retrans_count "${CASE1_EVENTS}")
install_netem_loss "${NS_S}" "${VETH_S}" 2%
run_transfer
remove_netem_loss
wait_until 10 1 count_greater_than matched_retrans_count "${CASE1_EVENTS}" "${CASE1_BEFORE}" \
	|| fatal "tcpshark local did not store a host_software match with a drop stack"
CASE1_RAW=$(raw_drop_count "${CASE1_EVENTS}")
((CASE1_RAW == 0)) \
	|| fatal "embedded dropwatch emitted a raw drop document"
stop_bamai || fatal "dropwatch-blacklisted case did not stop cleanly"
huatuo_bamai_log_check || fatal "dropwatch-blacklisted case logged an unexpected error or panic"
log_info "PASS: local correlation does not depend on standalone dropwatch"

CASE2_EVENTS="${OUTPUT_DIR}/parallel-events"
log_info "parallel standalone raw dropwatch and tcpshark local"
start_bamai "${CASE2_EVENTS}" true
CASE2_RETRANS_BEFORE=$(finalized_retrans_count "${CASE2_EVENTS}")
CASE2_RAW_BEFORE=$(raw_drop_count "${CASE2_EVENTS}")
install_netem_loss "${NS_S}" "${VETH_S}" 2%
run_transfer
remove_netem_loss
wait_until 10 1 count_greater_than finalized_retrans_count "${CASE2_EVENTS}" "${CASE2_RETRANS_BEFORE}" \
	|| fatal "parallel case did not store a finalized retransmission"
wait_until 10 1 count_greater_than raw_drop_count "${CASE2_EVENTS}" "${CASE2_RAW_BEFORE}" \
	|| fatal "parallel case did not store a standalone raw drop"
stop_bamai || fatal "parallel case did not stop cleanly"
huatuo_bamai_log_check || fatal "parallel case logged an unexpected error or panic"
log_info "PASS: raw drops and finalized retransmissions are independent outputs"

CASE3_EVENTS="${OUTPUT_DIR}/shutdown-events"
log_info "SIGTERM asks tcpshark to finalize a pending local result before exit"
start_bamai "${CASE3_EVENTS}" false
CASE3_BEFORE=$(cross_netns_unknown_count "${CASE3_EVENTS}")

# The client-side INPUT drop is outside the server socket namespace. It causes
# a retransmission without giving the local matcher a valid strict candidate.
install_drop_rule "${NS_C}" INPUT 30:30
RETRANS_BEFORE=$(tcp_retrans_count)
start_transfer
wait_for_retrans_after "${RETRANS_BEFORE}" \
	|| fatal "shutdown scenario did not produce a retransmission"
remove_drop_rule

CASE3_PRE_SIGNAL=$(cross_netns_unknown_count "${CASE3_EVENTS}")
((CASE3_PRE_SIGNAL == CASE3_BEFORE)) \
	|| fatal "shutdown result was stored before SIGTERM; final-drain wiring was not exercised"

stop_bamai || fatal "SIGTERM did not complete graceful correlation shutdown"
finish_transfer
CASE3_AFTER=$(cross_netns_unknown_count "${CASE3_EVENTS}")
((CASE3_AFTER > CASE3_BEFORE)) \
	|| fatal "SIGTERM did not persist the cross-netns unknown tcpshark result"
huatuo_bamai_log_check || fatal "shutdown case logged an unexpected error or panic"
log_info "PASS: final tcpshark result was observed after shutdown"
