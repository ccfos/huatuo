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

(
	work="${HUATUO_BAMAI_TEST_TMPDIR}/vm-cache"
	mkdir -p "${work}/bin"
	printf 'VM_IP=192.0.2.1\nVM_SSH_KEY=/tmp/test-key\n' > "${work}/vm.env"
	cat > "${work}/bin/ssh" << 'STUB'
#!/bin/sh
if [ "$TEST_MODE" = key ]; then printf '%s\n' "$TEST_METADATA"; fi
STUB
	cat > "${work}/bin/rsync" << 'STUB'
#!/bin/sh
printf '%s\n' "$@" > "$RUNNER_TEMP/rsync-args"
STUB
	chmod +x "${work}/bin/"*
	export PATH="${work}/bin:${PATH}" VM_ENV_FILE="${work}/vm.env" RUNNER_TEMP="${work}"
	export TEST_MODE=key VM_IMAGE_REF=image-a TEST_METADATA='go1.24 gcc14'
	cache_script="${ROOT_DIR}/.github/workflows/scripts/vm-test/go-cache.sh"
	key=$(bash "${cache_script}" key)
	assert_eq "$(bash "${cache_script}" key)" "${key}" "stable cache key"
	[[ $(VM_IMAGE_REF=image-b bash "${cache_script}" key) != "${key}" ]] || fatal "cache key ignores VM image"
	[[ $(TEST_METADATA='go1.25 gcc14' bash "${cache_script}" key) != "${key}" ]] || fatal "cache key ignores toolchain"
	for TEST_MODE in restore save; do
		bash "${cache_script}" "${TEST_MODE}" > "${work}/${TEST_MODE}.log"
		mapfile -t args < "${work}/rsync-args"
		local_path="${work}/huatuo-go-build/" remote_path=root@192.0.2.1:/root/.cache/go-build/
		if [[ ${TEST_MODE} == restore ]]; then
			assert_eq "${args[-2]}" "${local_path}" "cache restore source"
			assert_eq "${args[-1]}" "${remote_path}" "cache restore destination"
			! grep -Fxq -- --delete "${work}/rsync-args" || fatal "restore deletes guest cache"
		else
			assert_eq "${args[-2]}" "${remote_path}" "cache save source"
			assert_eq "${args[-1]}" "${local_path}" "cache save destination"
			grep -Fxq -- --delete "${work}/rsync-args" || fatal "save retains stale cache files"
		fi
	done
)

# Call the real guest entry function; stub only operations requiring a VM.
for guest_mode in --prepare-only --test-only combined; do
	guest_work="${HUATUO_BAMAI_TEST_TMPDIR}/guest-${guest_mode}"
	mkdir -p "${guest_work}/_output"
	touch "${guest_work}/_output/portable-generated.commit"
	TEST_GUEST_WORK="${guest_work}" bash -eu -s -- "${guest_mode}" << 'SH'
source "$ROOT_DIR/.github/workflows/scripts/vm-test/run-in-vm.sh"
configure_proxy() { echo proxy >> "$TEST_GUEST_WORK/calls"; }
print_sys_info() { echo info >> "$TEST_GUEST_WORK/calls"; }
install_test_dependencies() { echo dependencies >> "$TEST_GUEST_WORK/calls"; }
grow_root_filesystem() { echo grow >> "$TEST_GUEST_WORK/calls"; }
install_temporary_go_tools() { exit 99; }
make() { echo "make $*" >> "$TEST_GUEST_WORK/calls"; }
git() { :; }
prefetch_storage_images() { :; }
cd() { builtin cd "$TEST_GUEST_WORK"; }
run_guest_tests "$@"
SH
	guest_expected=proxy
	if [[ ${guest_mode} != --test-only ]]; then guest_expected+=$'\ngrow\ninfo\ndependencies'; fi
	if [[ ${guest_mode} != --prepare-only ]]; then guest_expected+=$'\nmake install-tools\nmake test SKIP_PORTABLE_GENERATION=1'; fi
	assert_eq "$(cat "${guest_work}/calls")" "${guest_expected}" "${guest_mode}: guest phases"
done
log_info "VM cache transport and guest phase checks passed"

# Real storage ownership and cleanup with Docker replaced by a local fixture.
cat > "${HUATUO_BAMAI_TEST_TMPDIR}/storage-fixture.sh" << 'SH'
set -euo pipefail
source "$ROOT_DIR/integration/lib.sh"
source "$ROOT_DIR/integration/lib_storage.sh"
if [[ $TEST_MODE != private ]]; then export STORAGE_SHARED_DIR="$HUATUO_BAMAI_TEST_TMPDIR/shared"; fi
docker() {
 case $1 in
 run) printf "%s\n" "$@" >> "$TEST_LOG.args"; [[ $TEST_MODE != concurrent ]] || sleep 0.05; printf 'start\n' >> "$TEST_LOG"; printf 'container-%s\n' "${HUATUO_BAMAI_TEST_TMPDIR##*/}";;
 port) printf '127.0.0.1:19200\n';;
 rm) printf 'stop\n' >> "$TEST_LOG"; [[ $TEST_MODE != stop_failure ]];;
 logs) printf 'shared storage diagnostics\n';;
 image|pull) return 0;;
 *) return 1;;
 esac
}
printf() {
 if [[ $TEST_MODE == state_failure && $1 == STORAGE_CONTAINER_ID=* ]]; then
  builtin printf "STORAGE_CONTAINER_ID='"
  return 1
 fi
 builtin printf "$@"
}
storage_ready() { return 0; }
chown() { :; }
timeout() { shift; "$@"; }
finish() {
 local status=$?
 [[ -z ${STORAGE_SHARED_DIR:-} ]] || stop_shared_storage "$status" || exit 1
}
trap finish EXIT
run_case() (
 export HUATUO_BAMAI_TEST_TMPDIR="$HUATUO_BAMAI_TEST_TMPDIR/$1"
 mkdir -p "$HUATUO_BAMAI_TEST_TMPDIR"
 trap storage_stop EXIT
 storage_start "${2:-default}" "${3:-}"
 if [[ -n ${3:-} ]]; then [[ $STORAGE_SHARED == false ]]; fi
 if [[ ${2:-default} == default ]]; then
  [[ $STORAGE_ADDR == http://* && -z $STORAGE_USERNAME && -z $STORAGE_PASSWORD ]]
 else
  [[ $STORAGE_SHARED == false && -n $STORAGE_USERNAME && -n $STORAGE_PASSWORD ]]
 fi
 storage_test_index profiles >> "$HUATUO_BAMAI_TEST_TMPDIR/index"
 [[ $TEST_MODE != failure ]] || exit 42
 [[ $TEST_MODE != skip ]] || exit 77
)
if [[ $TEST_MODE == concurrent ]]; then
 run_case case-one.Abc & first=$!
 run_case case-two.Xyz & second=$!
 wait "$first"
 wait "$second"
else
 run_case case-one.Abc
 if [[ $TEST_MODE == bound_data ]]; then
  run_case case-two.Xyz default "$HUATUO_BAMAI_TEST_TMPDIR/bound-data"
 else
  run_case case-two.Xyz
 fi
fi
if [[ $TEST_MODE == compatibility ]]; then
 for profile in elasticsearch_v7 elasticsearch_v8 opensearch_v2; do run_case "$profile" "$profile"; done
fi
SH
for storage_mode in shared concurrent private bound_data failure skip compatibility stop_failure state_failure; do
	storage_work="${HUATUO_BAMAI_TEST_TMPDIR}/storage-${storage_mode}"
	mkdir -p "${storage_work}"
	storage_expected_status=0 storage_expected_starts=1
	case ${storage_mode} in
	private | bound_data) storage_expected_starts=2 ;;
	failure) storage_expected_status=42 ;;
	skip) storage_expected_status=77 ;;
	compatibility) storage_expected_starts=4 ;;
	stop_failure | state_failure) storage_expected_status=1 ;;
	esac
	storage_status=0
	HUATUO_BAMAI_TEST_TMPDIR="${storage_work}" STORAGE_SHARED_DIR='' \
		TEST_LOG="${storage_work}/lifecycle.log" TEST_MODE="${storage_mode}" \
		bash "${HUATUO_BAMAI_TEST_TMPDIR}/storage-fixture.sh" > "${storage_work}/test.log" 2>&1 || storage_status=$?
	assert_eq "${storage_status}" "${storage_expected_status}" "${storage_mode}: storage status; see ${storage_work}/test.log"
	assert_eq "$(grep -c '^start$' "${storage_work}/lifecycle.log")" "${storage_expected_starts}" "${storage_mode}: starts"
	assert_eq "$(grep -c '^stop$' "${storage_work}/lifecycle.log")" "${storage_expected_starts}" "${storage_mode}: stops"
	case ${storage_mode} in
	failure | stop_failure) [[ -f ${storage_work}/shared/es.env ]] || fatal "${storage_mode}: diagnostics were removed" ;;
	*) [[ ! -e ${storage_work}/shared/es.env ]] || fatal "${storage_mode}: shared storage state survived cleanup" ;;
	esac
	if [[ ${storage_mode} == bound_data ]]; then
		grep -Fxq "type=bind,source=${storage_work}/bound-data,target=/usr/share/elasticsearch/data" "${storage_work}/lifecycle.log.args" || fatal "private ES lost its data directory"
	fi
	case ${storage_mode} in
	shared | concurrent | private)
		for storage_case in case-one.Abc case-two.Xyz; do
			assert_eq "$(cat "${storage_work}/${storage_case}/index")" "profiles_${storage_case,,}" "workspace index isolation"
		done
		;;
	esac
done
log_info "shared and private storage lifecycles, isolation and failure cleanup passed"

(
	source "${ROOT_DIR}/integration/config.sh"
	source "${ROOT_DIR}/integration/lib_storage.sh"
	config_root="${HUATUO_BAMAI_TEST_TMPDIR}/profiling-config"
	APISERVER_PORT=21100 NODE_PORT=21001 PROFILE_INTERVAL=2
	HUATUO_BAMAI_ADDR="http://127.0.0.1:${NODE_PORT}"
	API_TOKEN=admin OTHER_API_TOKEN=reader ES_PASSWORD=test STORAGE_ADDR=http://127.0.0.1:9200
	MAX_HOST=2 MAX_TOTAL=4 NODE_CAPACITY=4 PROFILER_TOOL_DIR=/fixture/tools POD_NAMESPACE=''
	for workspace in case-one.Abc case-two.Xyz; do
		HUATUO_BAMAI_TEST_TMPDIR="${config_root}/${workspace}"
		mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
		expected_index="huatuo_continuous_profiling_test_${workspace,,}"
		for config_mode in continuous workflow_storage workflow_no_storage; do
			WITH_STORAGE=true
			if [[ ${config_mode} == continuous ]]; then
				write_continuous_profiling_bamai_config
				write_continuous_profiling_apiserver_config
			else
				if [[ ${config_mode} == workflow_no_storage ]]; then WITH_STORAGE=false; fi
				write_apiserver_profiling_bamai_config
				write_apiserver_profiling_config
			fi
			assert_eq "$(grep -cFx "    Index = \"${expected_index}\"" "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf")" 1 "${config_mode}: node index"
			if [[ ${WITH_STORAGE} == true ]]; then
				assert_eq "$(grep -cFx "    Index = \"${expected_index}\"" "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf")" 1 "${config_mode}: apiserver index"
			else
				if grep -Fxq '[Elasticsearch]' "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf"; then fatal "storage disabled but configured"; fi
			fi
		done
	done
)
log_info "profiling node and apiserver storage configs agree and isolate workspaces"

(
	work="${HUATUO_BAMAI_TEST_TMPDIR}/worker-ports"
	mkdir -p "${work}"
	ss() { echo 'LISTEN 0 128 127.0.0.1:20000 0.0.0.0:*'; }
	export INTEGRATION_PORT_BASE=20000 INTEGRATION_PORT_DIR="${work}/ports"
	pids=()
	for ((i = 0; i < 12; i++)); do
		allocate_available_port > "${work}/port-${i}" &
		pids+=("$!")
	done
	for pid in "${pids[@]}"; do wait "${pid}"; done
	assert_eq "$(sort -u "${work}/"port-* | wc -l)" 12 "concurrent port uniqueness"
	for file in "${work}/"port-*; do
		port=$(cat "${file}")
		((port > 20000 && port < 21000)) || fatal "worker port outside its block: ${port}"
	done
	assert_eq "$(INTEGRATION_PORT_BASE=21000 INTEGRATION_PORT_DIR="${work}/other" allocate_available_port)" 21000 "separate worker block"
	ss() { return 1; }
	if allocate_available_port; then fatal "allocation ignored failed socket query"; fi
)

for config_mode in auth_only empty_server explicit_address; do
	config_work="${HUATUO_BAMAI_TEST_TMPDIR}/worker-config-${config_mode}"
	mkdir -p "${config_work}"
	config_address=127.0.0.1:21001
	{
		if [[ ${config_mode} != auth_only ]]; then echo '[HTTPServer]'; fi
		if [[ ${config_mode} == explicit_address ]]; then
			config_address=127.0.0.1:23456
			echo 'ListenAddress = "127.0.0.1:23456"'
		fi
		printf '[HTTPServer.Auth]\nBearerToken = "secret"\n'
	} > "${config_work}/bamai.conf"
	HUATUO_BAMAI_TEST_TMPDIR="${config_work}" HUATUO_BAMAI_ADDR="http://${config_address}" bash -eu -s << 'SH'
source "$ROOT_DIR/integration/lib.sh"
source "$ROOT_DIR/integration/config.sh"
source "$ROOT_DIR/integration/lib_storage.sh"
configure_worker_bamai_address
APISERVER_PORT=21100 API_TOKEN=admin OTHER_API_TOKEN=reader ES_PASSWORD=test STORAGE_ADDR=http://127.0.0.1:9200 PROFILE_FILTER_INDEX=profile-filter
for writer in write_apiserver_apis_config write_apiserver_without_profile_storage_config write_continuous_profiling_apiserver_config write_profile_query_apiserver_config; do
 "$writer"
 cp "$HUATUO_BAMAI_TEST_TMPDIR/apiserver.conf" "$HUATUO_BAMAI_TEST_TMPDIR/$writer.conf"
done
NODE_PORT=${HUATUO_BAMAI_ADDR##*:} MAX_HOST=2 MAX_TOTAL=4
for WITH_STORAGE in false true; do
 write_apiserver_profiling_config
 cp "$HUATUO_BAMAI_TEST_TMPDIR/apiserver.conf" "$HUATUO_BAMAI_TEST_TMPDIR/write_apiserver_profiling_${WITH_STORAGE}.conf"
done
SH
	assert_eq "$(grep -cF "ListenAddress = \"${config_address}\"" "${config_work}/bamai.conf")" 1 "worker listen address"
	grep -Fxq 'BearerToken = "secret"' "${config_work}/bamai.conf" || fatal "config update lost authentication"
	for config_file in "${config_work}/"write_*.conf; do
		assert_eq "$(grep -cFx '[Agent]' "${config_file}")" 1 "Agent section count: ${config_file}"
		assert_eq "$(grep -cFx "    HTTPPort = ${config_address##*:}" "${config_file}")" 1 "Agent port: ${config_file}"
	done
done
log_info "concurrent port reservations and worker config checks passed"

for prefetch_mode in success pull_failure test_failure; do
	prefetch_work="${HUATUO_BAMAI_TEST_TMPDIR}/prefetch-${prefetch_mode}"
	mkdir -p "${prefetch_work}/bin" "${prefetch_work}/integration"
	cp "${ROOT_DIR}/integration/lib_storage.sh" "${prefetch_work}/integration/"
	cat > "${prefetch_work}/bin/docker" << 'STUB'
#!/bin/bash
echo "$$" >> "$TEST_STATE/children"
touch "$TEST_STATE/${2##*/}.started"
while [[ ! -f $TEST_STATE/release ]]; do sleep 0.01; done
[[ $TEST_MODE != pull_failure ]]
STUB
	chmod +x "${prefetch_work}/bin/docker"
	prefetch_status=0
	PATH="${prefetch_work}/bin:${PATH}" TEST_STATE="${prefetch_work}" TEST_MODE="${prefetch_mode}" \
		bash -eu -s > "${prefetch_work}/test.log" 2>&1 << 'SH' || prefetch_status=$?
source "$ROOT_DIR/integration/lib.sh"
source "$ROOT_DIR/.github/workflows/scripts/vm-test/run-in-vm.sh"
cd "$TEST_STATE"
storage_prefetch_pids=()
trap stop_storage_image_prefetch EXIT
prefetch_storage_images
wait_until 5 0.05 test -f "$TEST_STATE/${STORAGE_ES7_IMAGE##*/}.started"
wait_until 5 0.05 test -f "$TEST_STATE/${STORAGE_OPENSEARCH_IMAGE##*/}.started"
assert_eq "${#storage_prefetch_pids[@]}" 2 "parallel image downloads"
[[ $TEST_MODE != test_failure ]] || exit 42
touch "$TEST_STATE/release"
for pid in "${storage_prefetch_pids[@]}"; do
 status=0
 wait "$pid" || status=$?
 expected_status=0
 if [[ $TEST_MODE == pull_failure ]]; then expected_status=1; fi
 assert_eq "$status" "$expected_status" "download status"
done
SH
	prefetch_expected_status=0
	if [[ ${prefetch_mode} == test_failure ]]; then prefetch_expected_status=42; fi
	assert_eq "${prefetch_status}" "${prefetch_expected_status}" "${prefetch_mode}: optional prefetch; see ${prefetch_work}/test.log"
	while read -r prefetch_pid; do
		! kill -0 "${prefetch_pid}" 2> /dev/null || fatal "prefetch left Docker process ${prefetch_pid}"
	done < "${prefetch_work}/children"
done
log_info "optional image prefetch preserves status and cleans up owned downloads"

# Exercise scheduling with local cases; namespace isolation is checked separately.
runner_work="${HUATUO_BAMAI_TEST_TMPDIR}/scheduler"
mkdir -p "${runner_work}/bin"
cat > "${runner_work}/bin/unshare" << 'STUB'
#!/bin/sh
[ "$1" = --mount ] || exit 99
shift
exec "$@"
STUB
printf '#!/bin/sh\nexit 0\n' > "${runner_work}/bin/mount"
chmod +x "${runner_work}/bin/"*
cat > "${runner_work}/fixture.sh" << 'SH'
set -euo pipefail
source "$ROOT_DIR/integration/lib.sh"
name=${0##*/}
wait_for() {
 for ((i=0;i<300;i++)); do [[ -f $1 ]] && return 0; sleep 0.01; done
 echo "timed out: $1" >&2; return 1
}
if [[ $TEST_MODE == profiler ]]; then
 if [[ $name == test_profiler_* && $name != test_profiler_native_cpu_hardware_pmu.sh ]]; then
  exec 8> "$TEST_STATE/profiler-slot-1"
  exec 9> "$TEST_STATE/profiler-slot-2"
  flock -n 8 || flock -n 9 || fatal "more than two profiler fixtures overlap"
 fi
 case $name in
  test_metrics.sh) touch "$TEST_STATE/pool-done";;
  test_profiler_java_cpu_multi_pid.sh)
   [[ -f $TEST_STATE/pool-done ]]
   touch "$TEST_STATE/first-started"
   wait_for "$TEST_STATE/second-started"
   touch "$TEST_STATE/first-done";;
  test_profiler_native_cpu.sh)
   [[ -f $TEST_STATE/pool-done ]]
   touch "$TEST_STATE/second-started"
   wait_for "$TEST_STATE/first-started"
   touch "$TEST_STATE/second-done";;
  test_profiler_java_memory_usage_alloc.sh)
   [[ -f $TEST_STATE/first-done ]]
   touch "$TEST_STATE/third-done";;
  test_profiler_native_cpu_cpp_symbols.sh)
   [[ -f $TEST_STATE/first-done || -f $TEST_STATE/second-done ]]
   touch "$TEST_STATE/fourth-done";;
  test_profiler_native_cpu_hardware_pmu.sh)
   [[ -f $TEST_STATE/first-done && -f $TEST_STATE/second-done && -f $TEST_STATE/third-done && -f $TEST_STATE/fourth-done ]]
   touch "$TEST_STATE/exclusive-done";;
 esac
 exit 0
fi
case $name in
 test_metrics.sh|test_dropwatch_devlink.sh|test_basic_storage_opensearch_elasticsearch.sh)
  if [[ $TEST_MODE == failure ]]; then for ((n=0;n<180;n++)); do echo fixture-progress; done; fi
  echo diagnostic-from-first-case
  record_test_timing fixture verification "$SECONDS"
  touch "$TEST_STATE/first-started"
  if [[ $TEST_MODE == refill || $TEST_MODE == failure ]]; then wait_for "$TEST_STATE/second-started"; fi
  if [[ $TEST_MODE == failure ]]; then exit 42; fi
  if [[ $TEST_MODE == refill ]]; then wait_for "$TEST_STATE/third-done"; fi
  if [[ $TEST_MODE == resource ]]; then wait_for "$TEST_STATE/third-started"; fi
  if [[ $TEST_MODE == storage ]]; then
   wait_for "$TEST_STATE/third-started"
   wait_for "$TEST_STATE/fourth-started"
   [[ ! -f $TEST_STATE/second-started ]]
  fi
  touch "$TEST_STATE/first-done"
  if [[ $TEST_MODE == skip ]]; then exit 77; fi
  ;;
 test_node_config_api.sh|test_dropwatch_reason.sh|test_dropwatch_icmpv6.sh)
  if [[ $TEST_MODE == serial || $TEST_MODE == resource || $TEST_MODE == storage ]]; then [[ -f $TEST_STATE/first-done ]]; fi
  touch "$TEST_STATE/second-started"
  trap 'touch "$TEST_STATE/second-cleaned"' EXIT
  if [[ $TEST_MODE == failure ]]; then wait_for "$TEST_STATE/first-cleaned"; fi
  touch "$TEST_STATE/second-done"
  ;;
 test_metrics_include_filter.sh|test_metrics_exclude_filter.sh)
  [[ $TEST_MODE != failure ]] || touch "$TEST_STATE/unexpected-start"
  touch "$TEST_STATE/third-started"
  if [[ $TEST_MODE == storage ]]; then wait_for "$TEST_STATE/fourth-started"; fi
  if [[ $TEST_MODE == refill ]]; then [[ -f $TEST_STATE/second-done && ! -f $TEST_STATE/first-done ]]; fi
  touch "$TEST_STATE/third-done"
  ;;
 test_node_cloud_events_api.sh)
  touch "$TEST_STATE/fourth-started"
  wait_for "$TEST_STATE/third-started"
  touch "$TEST_STATE/fourth-done"
  ;;
 test_unknown.sh)
  [[ -f $TEST_STATE/first-done && -f $TEST_STATE/second-done && -f $TEST_STATE/third-done ]]
  if [[ $TEST_MODE == storage ]]; then [[ -f $TEST_STATE/fourth-done ]]; fi
  touch "$TEST_STATE/exclusive-done"
  ;;
esac
SH
cat > "${runner_work}/launch.sh" << 'SH'
set -euo pipefail
source "$ROOT_DIR/integration/lib.sh"
source "$ROOT_DIR/integration/lib_runner.sh"
if [[ $TEST_MODE == failure ]]; then
 huatuo_bamai_stop() {
  if [[ $1 == */test_metrics.* ]]; then touch "$TEST_STATE/first-cleaned"; fi
 }
elif [[ $TEST_MODE == cleanup_failure ]]; then huatuo_bamai_stop() { return 1; }; fi
run_test_suite "$TEST_REPEATS" integration "$TEST_JOBS" "$@"
SH
for runner_mode in refill resource failure skip serial repeated cleanup_failure; do
	runner_state="${runner_work}/${runner_mode}"
	mkdir -p "${runner_state}/workspace"
	runner_names=(test_metrics.sh test_node_config_api.sh test_metrics_include_filter.sh test_unknown.sh)
	runner_jobs=2 runner_repeats=1 runner_expected_status=0 runner_skipped=0
	case ${runner_mode} in
	resource) runner_names=(test_dropwatch_devlink.sh test_dropwatch_reason.sh test_metrics_exclude_filter.sh test_unknown.sh) ;;
	storage)
		runner_jobs=3
		runner_names=(test_basic_storage_opensearch_elasticsearch.sh test_dropwatch_icmpv6.sh test_metrics_include_filter.sh test_node_cloud_events_api.sh test_unknown.sh)
		;;
	profiler)
		runner_jobs=3
		runner_names=(test_metrics.sh test_profiler_java_cpu_multi_pid.sh test_profiler_java_memory_usage_alloc.sh test_profiler_native_cpu.sh test_profiler_native_cpu_cpp_symbols.sh test_profiler_native_cpu_hardware_pmu.sh)
		;;
	serial) runner_jobs=1 ;;
	repeated) runner_repeats=2 ;;
	failure | cleanup_failure) runner_expected_status=1 ;;
	skip) runner_skipped=1 ;;
	esac
	runner_cases=()
	for runner_name in "${runner_names[@]}"; do
		cp "${runner_work}/fixture.sh" "${runner_state}/${runner_name}"
		runner_cases+=("${runner_state}/${runner_name}")
	done
	runner_status=0
	HUATUO_BAMAI_TEST_TMPDIR="${runner_state}/workspace" TEST_STATE="${runner_state}" TEST_MODE="${runner_mode}" \
		TEST_JOBS="${runner_jobs}" TEST_REPEATS="${runner_repeats}" STORAGE_SHARED_DIR='' PATH="${runner_work}/bin:${PATH}" \
		timeout 20 bash "${runner_work}/launch.sh" "${runner_cases[@]}" > "${runner_state}/test.log" 2>&1 || runner_status=$?
	assert_eq "${runner_status}" "${runner_expected_status}" "${runner_mode}: runner exit; see ${runner_state}/test.log"
	if [[ ${runner_expected_status} == 1 ]]; then
		[[ ! -e ${runner_state}/unexpected-start ]] || fatal "${runner_mode}: scheduled after failure"
		if [[ ${runner_mode} == failure ]]; then
			grep -Fq 'total=2 passed=1 skipped=0 failed=1' "${runner_state}/test.log" || fatal "incorrect failure summary"
			grep -Fq diagnostic-from-first-case "${runner_state}/test.log" || fatal "failure log lost early diagnostics"
			[[ -f ${runner_state}/second-cleaned ]] || fatal "failure bypassed in-flight cleanup"
		fi
	else
		runner_count=$((${#runner_names[@]} * runner_repeats))
		grep -Fq "total=${runner_count} passed=$((runner_count - runner_skipped)) skipped=${runner_skipped} failed=0" \
			"${runner_state}/test.log" || fatal "${runner_mode}: incorrect summary"
		if [[ ${runner_mode} != profiler ]]; then
			grep -Fq 'subject=fixture phase=verification duration=' "${runner_state}/test.log" || fatal "timing log missing"
		fi
		[[ -f ${runner_state}/exclusive-done && ! -e ${runner_state}/workspace ]] || fatal "${runner_mode}: exclusive case or cleanup missing"
	fi
done
log_info "scheduler refill, resource exclusion, failure draining, skips and cleanup passed"

# INT/TERM must cancel both serial and parallel cases, including their children.
cat > "${runner_work}/cancel-case.sh" << 'SH'
set -euo pipefail
name=${0##*/}
echo "$BASHPID" > "$TEST_STATE/$name.pid"
sleep 30 &
child=$!
echo "$child" > "$TEST_STATE/$name.child"
if [[ $TEST_CANCEL_MODE == forced ]]; then
 trap '' TERM
 mkfifo "$TEST_STATE/$name.pipe"
 exec 9<> "$TEST_STATE/$name.pipe"
fi
cleanup() {
 kill "$child" 2>/dev/null || true
 wait "$child" 2>/dev/null || true
 touch "$TEST_STATE/$name.cleaned"
}
trap cleanup EXIT
if [[ $TEST_CANCEL_MODE == graceful ]]; then trap 'exit 143' TERM; fi
touch "$TEST_STATE/$name.ready"
wait "$child" || true
# Keep the stubborn shell alive without orphaning an un-reapable sleep process.
if [[ $TEST_CANCEL_MODE == forced ]]; then read -r -t 30 -u 9 unused || true; fi
SH

assert_runner_cancellation() (
	local jobs=$1 signal=$2 mode=$3 pid='' status=0 started name case_pid
	local state="${runner_work}/cancel-${jobs}-${signal}-${mode}"
	mkdir -p "${state}/workspace"
	# Reclaim only processes created by this fixture, even if an assertion fails.
	trap '
		if [[ -n ${pid} ]]; then kill -KILL -- "-${pid}" 2>/dev/null || true; wait "${pid}" 2>/dev/null || true; fi
		for file in "${state}/"*.pid; do
			[[ -f ${file} ]] || continue
			kill -KILL -- "-$(cat "${file}")" 2>/dev/null || true
		done
	' EXIT
	local -a names=(test_metrics.sh) cases=()
	if ((jobs == 2)); then names+=(test_node_config_api.sh); fi
	for name in "${names[@]}"; do
		cp "${runner_work}/cancel-case.sh" "${state}/${name}"
		cases+=("${state}/${name}")
	done
	set -m
	HUATUO_BAMAI_TEST_TMPDIR="${state}/workspace" TEST_STATE="${state}" TEST_MODE=cancellation \
		TEST_CANCEL_MODE="${mode}" TEST_JOBS="${jobs}" TEST_REPEATS=1 STORAGE_SHARED_DIR='' PATH="${runner_work}/bin:${PATH}" \
		bash "${runner_work}/launch.sh" "${cases[@]}" > "${state}/test.log" 2>&1 &
	pid=$!
	set +m
	for name in "${names[@]}"; do wait_until 5 0.05 test -f "${state}/${name}.ready" > "${state}/wait.log" 2>&1 || fatal "cancellation fixture did not start"; done
	started=${SECONDS}
	if [[ ${signal} == INT ]]; then kill -INT -- "-${pid}"; else kill -TERM "${pid}"; fi
	wait_until 8 0.05 process_exited "${pid}" > "${state}/wait.log" 2>&1 || fatal "runner cancellation exceeded grace period; see ${state}/test.log"
	wait "${pid}" || status=$?
	pid=''
	grep -Fq "total=${jobs} passed=0 skipped=0 failed=${jobs}" "${state}/test.log" || fatal "incorrect cancellation summary"
	local expected_status=143
	if [[ ${signal} == INT ]]; then expected_status=130; fi
	assert_eq "${status}" "${expected_status}" "${jobs} workers / ${signal} / ${mode}: cancellation exit"
	if [[ ${mode} == graceful ]]; then
		((SECONDS - started < 5)) || fatal "cooperative cancellation consumed the force-kill grace period"
	fi
	for name in "${names[@]}"; do
		case_pid=$(cat "${state}/${name}.pid")
		! kill -0 "${case_pid}" 2> /dev/null || fatal "cancelled case survived: ${case_pid}"
		! kill -0 "$(cat "${state}/${name}.child")" 2> /dev/null || fatal "cancelled workload survived"
		! kill -0 -- "-${case_pid}" 2> /dev/null || fatal "cancelled process group survived"
		if [[ ${mode} == graceful ]]; then
			[[ -f ${state}/${name}.cleaned ]] || fatal "cancellation bypassed case EXIT cleanup"
		fi
	done
)
for cancel_jobs in 1 2; do
	for cancel_signal in INT TERM; do
		assert_runner_cancellation "${cancel_jobs}" "${cancel_signal}" graceful
	done
done
assert_runner_cancellation 1 TERM forced
assert_runner_cancellation 2 INT forced
log_info "serial and parallel cancellation, exit status, cleanup and forced termination passed"
