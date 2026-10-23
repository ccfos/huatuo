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
require_commands python3 jq

assert_process_shutdown() (
	local mode=$1 expected_status=$2 signal=${3:-TERM} pid="" status=0
	local ready="${HUATUO_BAMAI_TEST_TMPDIR}/${mode}.ready"
	trap 'stop_and_wait_by_pid "${pid}" 1 || true' EXIT
	python3 - "${ready}" "${mode}" << 'PY' &
import pathlib
import signal
import sys
import time

signal.signal(signal.SIGTERM, signal.SIG_IGN if sys.argv[2] == "forced" else lambda *_: sys.exit(7))
signal.signal(signal.SIGUSR2, lambda *_: sys.exit(0))
pathlib.Path(sys.argv[1]).touch()
while True:
    time.sleep(0.01)
PY
	pid=$!
	wait_until 5 0.05 test -f "${ready}" || fatal "${mode}: child did not become ready"
	stop_and_wait_by_pid "${pid}" 1 "${signal}" || status=$?
	assert_eq "${status}" "${expected_status}" "${mode}: child exit status"
	! kill -0 "${pid}" 2> /dev/null || fatal "${mode}: child survived shutdown"
	pid=""
)

assert_streaming_result() {
	local actual_status=$1 expected_error=$2 log_file=$3
	if [[ -z ${expected_error} ]]; then
		assert_eq "${actual_status}" 0 "streaming failed; see ${log_file}"
	else
		[[ ${actual_status} != 0 ]] || fatal "invalid streaming session passed; see ${log_file}"
		grep -Fq "${expected_error}" "${log_file}" \
			|| fatal "expected streaming failure missing: ${expected_error}; see ${log_file}"
	fi
}

stop_and_wait_by_pid ""
assert_process_shutdown graceful_failure 7
assert_process_shutdown forced 137
assert_process_shutdown custom_signal 0 USR2

cat > "${HUATUO_BAMAI_TEST_TMPDIR}/profiler.py" << 'PY'
import json
import signal
import sys
import time

def finish(*_):
    print(json.dumps({"final": True, "profile": {"sample": []}}), flush=True)
    sys.exit(int(sys.argv[2]))

signal.signal(signal.SIGINT, finish)
for _ in range(int(sys.argv[1])):
    print(json.dumps({"profile": {"sample": [1]}}), flush=True)
    time.sleep(0.05)
while True:
    time.sleep(0.01)
PY

for helper_case in complete shutdown_failure missing_window; do
	helper_windows=2 helper_exit=0 helper_error=""
	case ${helper_case} in
	shutdown_failure) helper_exit=7 helper_error='exited 7' ;;
	missing_window) helper_windows=1 helper_error='two Java windows not published' ;;
	esac
	helper_status=0
	bash -euc '
source "$ROOT_DIR/integration/lib.sh"
# Bound the missing-window fixture without changing its completion predicate.
wait_until() {
 shift 2
 local i
 for ((i=0; i<20; i++)); do
  if "$@"; then return 0; fi
  sleep 0.05
 done
 return 1
}
assert_java_profiler_streams "$1" python3 "$2" "$3" "$4"
jq -se "last.final == true" "$1/windows.json"
' bash "${HUATUO_BAMAI_TEST_TMPDIR}/${helper_case}" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/profiler.py" "${helper_windows}" "${helper_exit}" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/${helper_case}.log" 2>&1 || helper_status=$?
	assert_streaming_result "${helper_status}" "${helper_error}" "${HUATUO_BAMAI_TEST_TMPDIR}/${helper_case}.log"
done

source "${ROOT_DIR}/integration/lib_network_capture.sh"
(
	OUTPUT_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/network-capture"
	mkdir -p "${OUTPUT_DIR}"
	TOOL_OUT="${OUTPUT_DIR}/events.json" EVENTS_FILE="${OUTPUT_DIR}/events.json"
	IPV4_OPTIONS_SOURCE=127.0.0.2 IPV4_OPTIONS_DESTINATION=127.0.0.1
	ipv4_options_source_port=20001 ipv4_options_destination_port=20002
	NETNS_CLIENT_ADDR=192.0.2.1 NETNS_SERVER_ADDR=192.0.2.2 TEST_PORT=19998
	ipv4_tcp='{"drop_source":"software","netdev_name":"lo","layers":{"label":"IPv4/TCP","ipv4":{"version":4,"ihl":6,"len":44,"tos":16,"id":4885,"flags":"DF","ttl":64,"saddr":"127.0.0.2","daddr":"127.0.0.1","protocol":"TCP"},"tcp":{"sport":20001,"dport":20002,"flags":"SYN","seq":1,"data_offset":5}}}'
	ipv4_udp='{"drop_source":"software","netdev_name":"lo","layers":{"label":"IPv4/UDP","ipv4":{"version":4,"ihl":6,"len":32,"tos":16,"id":4885,"flags":"DF","ttl":64,"saddr":"127.0.0.2","daddr":"127.0.0.1","protocol":"UDP"},"udp":{"sport":20001,"dport":20002,"len":8}}}'
	icmp_request='{"layers":{"label":"IPv6/ICMPv6","icmp":{"type":"EchoRequest","id":4660,"seq":7},"ipv6":{"saddr":"2001:db8::2","daddr":"2001:db8::1"}}}'
	icmp_zero='{"layers":{"label":"IPv6/ICMPv6","icmp":{"type":"EchoRequest"},"ipv6":{"saddr":"2001:db8::2","daddr":"2001:db8::1"}}}'
	latency_event='{"tracer_data":{"tcp_saddr":"192.0.2.1","tcp_daddr":"192.0.2.2","latency_stage":"receive","latency_ms":2,"latency_threshold_ms":1,"packet_len_bytes":64,"net_namespace_inum":1,"net_namespace_cookie":1}}'
	ipv4_max_tcp=${ipv4_tcp/\"ihl\":6/\"ihl\":15}
	ipv4_max_tcp=${ipv4_max_tcp/\"len\":44/\"len\":80}
	ipv4_packets="${ipv4_tcp}"$'\n'"${ipv4_udp}"$'\n'"${ipv4_max_tcp}"
	icmp_text='timestamp IPv6/ICMPv6 2001:db8::1 > 2001:db8::2 type=EchoReply id=4660 seq=7 stack'
	fast_event='{"tcp_sport":19998,"tcp_reason":"fast_retransmit"}'
	assert_capture() {
		local content=$1 expected_match=$2 status=0
		shift 2
		printf '%s\n' "${content}" > "${TOOL_OUT}"
		("$@") > "${OUTPUT_DIR}/matcher.log" 2>&1 || status=$?
		if [[ ${expected_match} == yes ]]; then
			assert_eq "${status}" 0 "${1}: valid capture; see ${OUTPUT_DIR}/matcher.log"
		else
			[[ ${status} != 0 ]] || fatal "${1}: incomplete or incorrect capture accepted: ${content}"
		fi
	}
	assert_capture "${ipv4_packets}" yes assert_ipv4_options_capture
	assert_capture "${ipv4_tcp}" no assert_ipv4_options_capture
	assert_capture "${icmp_request}" yes assert_icmpv6_echo_identity json "${TOOL_OUT}" EchoRequest 2001:db8::2 2001:db8::1 4660 7
	assert_capture "${icmp_request}" no assert_icmpv6_echo_identity json "${TOOL_OUT}" EchoRequest 2001:db8::2 2001:db8::1 4661 7
	assert_capture "${icmp_zero}" yes assert_icmpv6_echo_identity json "${TOOL_OUT}" EchoRequest 2001:db8::2 2001:db8::1 0 0
	assert_capture "${icmp_text}" yes assert_icmpv6_echo_identity text "${TOOL_OUT}" EchoReply 2001:db8::1 2001:db8::2 4660 7
	assert_capture '' no assert_icmpv6_echo_identity text "${TOOL_OUT}" EchoReply 2001:db8::1 2001:db8::2 4660 7

	for capture in ipv4 icmp_json icmp_zero icmp_text fast latency; do
		case ${capture} in
		ipv4)
			valid=${ipv4_packets}
			matcher=(ipv4_options_capture_is_complete)
			;;
		icmp_json)
			valid=${icmp_request}
			matcher=(icmpv6_json_matches "${TOOL_OUT}" EchoRequest 2001:db8::2 2001:db8::1 4660 7)
			;;
		icmp_zero)
			valid=${icmp_zero}
			matcher=(icmpv6_json_matches "${TOOL_OUT}" EchoRequest 2001:db8::2 2001:db8::1 0 0)
			;;
		icmp_text)
			valid=${icmp_text}
			matcher=(icmpv6_text_matches "${TOOL_OUT}" EchoReply 2001:db8::1 2001:db8::2 4660 7)
			;;
		fast)
			valid=${fast_event}
			matcher=(fast_retransmit_is_stored)
			;;
		latency)
			valid=${latency_event}
			matcher=(net_rx_latency_event_is_stored)
			;;
		esac
		assert_capture "${valid}" yes "${matcher[@]}"
		assert_capture '' no "${matcher[@]}"
		if [[ ${capture} != icmp_text ]]; then assert_capture "${valid}"$'\n{' no "${matcher[@]}"; fi
		case ${capture} in
		ipv4)
			assert_capture "${ipv4_tcp}"$'\n'"${ipv4_udp}" no "${matcher[@]}"
			assert_capture "${ipv4_tcp}"$'\n'"${ipv4_max_tcp}" no "${matcher[@]}"
			assert_capture "${valid//20002/20003}" no "${matcher[@]}"
			;;
		icmp_json)
			assert_capture "${valid/4660/4661}" no "${matcher[@]}"
			assert_capture "${valid/EchoRequest/EchoReply}" no "${matcher[@]}"
			;;
		icmp_zero) assert_capture "$(jq '.layers.icmp.id=0' <<< "${valid}")" no "${matcher[@]}" ;;
		icmp_text) assert_capture 'timestamp IPv6/ICMPv6 2001:db8::2 > 2001:db8::1 type=EchoReply id=4660 seq=7 stack' no "${matcher[@]}" ;;
		fast)
			assert_capture "${valid/19998/19999}" no "${matcher[@]}"
			assert_capture "${valid/fast_retransmit/unknown}" no "${matcher[@]}"
			assert_capture "${valid/fast_retransmit/RTO}" no "${matcher[@]}"
			assert_capture '{"tcp_sport":19998,"ca_state":3}' no "${matcher[@]}"
			;;
		latency)
			assert_capture "$(jq '.tracer_data.latency_stage=null' <<< "${valid}")" no "${matcher[@]}"
			assert_capture "${valid/192.0.2.1/192.0.2.3}" no "${matcher[@]}"
			;;
		esac
	done
)

log_info "process shutdown, Java streaming and network capture completion checks passed"

# Exercise generation guards without compiling or touching the checkout.
# Exercise the Makefile entry point without compiling or touching the checkout.
(
	for mode in valid stale_revision missing_source missing_marker dirty_source; do
		work="${HUATUO_BAMAI_TEST_TMPDIR}/generation-${mode}"
		mkdir -p "${work}"/{build,bpf,cmd,bin,_output}
		cp "${ROOT_DIR}/Makefile" "${work}/Makefile"
		: > "${work}/build/clang.sh"
		printf '_output/\nbin/\npkg/\ninternal/\n' > "${work}/.gitignore"
		(
			cd "${work}"
			make APP_COMMIT=fixture --no-print-directory \
				'--eval=print-portable:;@echo $(PORTABLE_GENERATED_FILES)' print-portable > sources
			read -r -a generated_files < sources
			((${#generated_files[@]})) || fatal "portable source list is empty"
			for file in "${generated_files[@]}"; do
				mkdir -p "$(dirname "${file}")"
				touch "${file}"
			done
			git init -q
			git add .
			git -c user.name=Test -c user.email=test@example.com commit -qm fixture
			git rev-parse HEAD > _output/portable-generated.commit
			case ${mode} in
			stale_revision) echo stale > _output/portable-generated.commit ;;
			missing_source) rm "${generated_files[-1]}" ;;
			missing_marker) rm _output/portable-generated.commit ;;
			dirty_source) echo '# modified' > build/clang.sh ;;
			esac
			cat > bin/go << 'STUB'
#!/bin/sh
[ "$*" = 'run ./build/bpfabi-tool' ] || exit 9
echo abi >> _output/calls
STUB
			chmod +x bin/go
			touch _output/.bpf-build-stamp-0
			status=0
			PATH="${work}/bin:${PATH}" make gen-build SKIP_PORTABLE_GENERATION=1 > output.log 2>&1 || status=$?
			if [[ ${mode} == valid ]]; then
				assert_eq "${status}" 0 "${mode}: generation guard; see ${work}/output.log"
				assert_eq "$(cat _output/calls)" abi "guest ABI generation"
			else
				[[ ${status} != 0 && ! -e _output/calls ]] || fatal "${mode}: invalid sources reached ABI generation"
			fi
		)
	done
)
log_info "portable generated source guards passed"
