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

# Compare localfile event counts with three remote storage backends.
# Dropwatch supplies input; packet decoding and drop reasons have their own tests.
# The runner executes cases serially on a host without another bamai instance.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/config.sh"

# Bash does not export the runner's timeout array to child shells.
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)
readonly STORAGE_INDEX=huatuo_storage_consistency

require_commands docker curl jq timeout ss
# These dependencies apply to all backends; a missing one skips the whole matrix.
docker info > "${HUATUO_BAMAI_TEST_TMPDIR}/docker-info.log" 2>&1 \
	|| skip "dockerd is unavailable or the Docker socket is inaccessible"
require_readable /sys/kernel/btf/vmlinux
tracepoint_available skb kfree_skb || skip "skb/kfree_skb tracepoint is unavailable"

storage_curl_status=0
storage_http_status=""

storage_cleanup() {
	local status=$?
	trap - EXIT
	huatuo_bamai_stop || status=1
	if ((status != 0 && status != 77)); then
		storage_dump_logs || true
	fi
	storage_stop || status=1
	exit "${status}"
}

storage_local_event_ready() {
	if [[ -s ${STORAGE_EVENT} ]] \
		&& jq -se 'length > 0' "${STORAGE_EVENT}" > /dev/null 2>&1; then
		return 0
	fi
	# Keep supplying input until storage receives an event, including collector startup.
	printf 'huatuo-storage-drop' 2>> "${HUATUO_BAMAI_TEST_TMPDIR}/traffic.log" \
		> "/dev/udp/127.0.0.99/${storage_target_port}" \
		|| fatal "failed to send UDP packet to 127.0.0.99:${storage_target_port}"
	return 1
}

assert_storage_request() {
	local operation=$1 method=$2 path=$3 body=${4:-}
	local status=0 http_status
	local -a arguments=(-X "${method}" -H 'Content-Type: application/json')
	[[ -z ${body} ]] || arguments+=(--data "${body}")
	http_status=$(storage_curl "${arguments[@]}" \
		-o "${STORAGE_RESPONSE}" -w '%{http_code}' \
		"${STORAGE_ADDR}/${path}" 2> "${STORAGE_ERRORS}") || status=$?
	report_http_response "${operation}" "${STORAGE_RESPONSE}" "${STORAGE_ERRORS}"
	assert_eq "${status}" 0 "${operation} curl status" || fatal "${operation} request failed"
	assert_eq "${http_status}" 200 "${operation} HTTP status" || fatal "${operation} returned HTTP ${http_status}"
}

assert_storage_shutdown() {
	local bamai_pid
	bamai_pid=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-bamai.pid")
	huatuo_bamai_stop
	wait "${bamai_pid}" || fatal "huatuo-bamai did not flush storage and exit successfully"
}

storage_remote_has_events() {
	storage_curl_status=0
	storage_http_status=$(storage_curl -o "${STORAGE_RESPONSE}" -w '%{http_code}' \
		"${STORAGE_ADDR}/${STORAGE_INDEX}/_count" 2> "${STORAGE_ERRORS}") || storage_curl_status=$?
	((storage_curl_status == 0)) && [[ ${storage_http_status} == 200 ]] || return 1
	jq -e '._shards.failed == 0 and .count > 0' "${STORAGE_RESPONSE}" > /dev/null 2>&1
}

assert_storage_async_write() {
	if ! wait_until 30 0.5 storage_remote_has_events; then
		report_http_response "async write count" "${STORAGE_RESPONSE}" "${STORAGE_ERRORS}"
		fatal "no events visible while bamai is running: curl=${storage_curl_status}, HTTP=${storage_http_status}"
	fi
	log_info "async writes visible before shutdown: $(< "${STORAGE_RESPONSE}")"
}

assert_storage_event_counts() {
	local local_count
	local_count=$(jq -s 'length' "${STORAGE_EVENT}") || fatal "failed to count localfile JSON events"
	((local_count > 0)) || fatal "localfile contains no events; cannot verify storage writes"
	# The writer has exited and flushed BulkIndexer; refresh exposes all accepted writes.
	assert_storage_request refresh POST "${STORAGE_INDEX}/_refresh"
	jq -e '._shards.failed == 0' "${STORAGE_RESPONSE}" > /dev/null \
		|| fatal "index refresh has failed shards"
	assert_storage_request count GET "${STORAGE_INDEX}/_count"
	jq -e --argjson count "${local_count}" \
		'._shards.failed == 0 and .count == $count' "${STORAGE_RESPONSE}" > /dev/null \
		|| fatal "remote count differs from local count ${local_count}, or count has failed shards"
	log_info "localfile and remote storage event counts match: ${local_count}"
}

storage_run_backend() {
	local backend=$1
	HUATUO_BAMAI_TEST_TMPDIR="${HUATUO_BAMAI_TEST_TMPDIR}/${backend}"
	mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}"
	readonly STORAGE_EVENT="${HUATUO_BAMAI_TEST_TMPDIR}/events/dropwatch"
	readonly STORAGE_RESPONSE="${HUATUO_BAMAI_TEST_TMPDIR}/storage-response.json"
	readonly STORAGE_ERRORS="${HUATUO_BAMAI_TEST_TMPDIR}/storage-request.err"
	trap storage_cleanup EXIT
	storage_start "${backend}"
	log_info "checking storage consistency: ${backend} at ${STORAGE_ADDR}"
	storage_http_port=$(allocate_available_port) || fatal "failed to allocate bamai HTTP port"
	storage_target_port=$(allocate_available_port udp) || fatal "failed to allocate UDP target port"
	HUATUO_BAMAI_ADDR="http://127.0.0.1:${storage_http_port}"
	HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

	assert_storage_request "create index" PUT "${STORAGE_INDEX}" \
		'{"settings":{"number_of_shards":1,"number_of_replicas":0}}'
	jq -e '.acknowledged == true' "${STORAGE_RESPONSE}" > /dev/null \
		|| fatal "storage did not acknowledge index creation"

	integration_huatuo_bamai_start write_storage_consistency_config \
		--region integration --disable-kubelet --log-debug
	wait_until 20 0.2 storage_local_event_ready || fatal "localfile storage did not receive an event"
	assert_storage_async_write
	assert_storage_shutdown
	assert_storage_event_counts
	assert_log_has_no_failure "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "huatuo-bamai"
}

storage_result=0
for storage_backend in opensearch_v2 elasticsearch_v8 elasticsearch_v7; do
	# An if/|| around the subshell would disable errexit inside its functions.
	set +e
	(
		set -e
		storage_run_backend "${storage_backend}"
	)
	storage_status=$?
	set -e
	case ${storage_status} in
	0)
		log_info "PASS: ${storage_backend}"
		;;
	77)
		log_info "SKIP: ${storage_backend}"
		if ((storage_result == 0)); then
			storage_result=77
		fi
		;;
	*)
		log_error "FAIL: ${storage_backend} (exit ${storage_status})"
		storage_result=1
		;;
	esac
done
exit "${storage_result}"
