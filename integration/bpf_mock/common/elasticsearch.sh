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

source "${ROOT_DIR}/integration/lib_storage.sh"
# Bash cannot export the runner's timeout array to the test subprocess.
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

start_bpf_mock_es() {
	local workspace_name=${HUATUO_BAMAI_TEST_TMPDIR##*/}
	readonly STORAGE_INDEX="huatuo_bpf_mock_${workspace_name,,}"
	require_commands docker curl jq timeout
	docker info > "${HUATUO_BAMAI_TEST_TMPDIR}/docker-info.log" 2>&1 \
		|| skip "dockerd is unavailable or the Docker socket is inaccessible"
	storage_start elasticsearch_v8
	storage_curl --fail -X PUT -H 'Content-Type: application/json' \
		--data '{"settings":{"number_of_shards":1,"number_of_replicas":0}}' \
		"${STORAGE_ADDR}/${STORAGE_INDEX}" > "${HUATUO_BAMAI_TEST_TMPDIR}/es-index.json"
	jq -e '.acknowledged == true' "${HUATUO_BAMAI_TEST_TMPDIR}/es-index.json" > /dev/null \
		|| fatal "Elasticsearch did not acknowledge mock index creation"
}

# The runner owns shared storage; direct script execution owns its own instance.
stop_bpf_mock_es() {
	storage_stop
}

# ES-backed tests compose core cleanup with storage diagnostics and shutdown.
cleanup_bpf_mock_es() {
	local status=$1 loaded_module=${2:-}
	cleanup_bpf_mock "${status}" "${loaded_module}" || status=$?
	if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
		storage_dump_logs || true
	fi
	stop_bpf_mock_es || status=1
	return "${status}"
}

write_bpf_mock_es_config() {
	write_single_event_config "$1" ""
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/bamai.conf" << EOF

[Storage.Elasticsearch]
    Address = "${STORAGE_ADDR}"
    Username = "${STORAGE_USERNAME}"
    Password = "${STORAGE_PASSWORD}"
    Index = "${STORAGE_INDEX}"
EOF
}

# Refresh also exposes writes flushed by the daemon during shutdown.
read_bpf_mock_es_events() {
	local output=$1 response="${HUATUO_BAMAI_TEST_TMPDIR}/es-search.json"
	storage_curl --fail -X POST "${STORAGE_ADDR}/${STORAGE_INDEX}/_refresh" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/es-refresh.json" || return 1
	jq -e '._shards.failed == 0' "${HUATUO_BAMAI_TEST_TMPDIR}/es-refresh.json" > /dev/null || return 1
	storage_curl --fail "${STORAGE_ADDR}/${STORAGE_INDEX}/_search?size=100&track_total_hits=true" \
		> "$response" || return 1
	jq -e \
		'.timed_out == false and ._shards.failed == 0 and .hits.total.relation == "eq"
		and .hits.total.value == (.hits.hits | length)' \
		"$response" > /dev/null || return 1
	jq -c '.hits.hits[]._source' "$response" > "$output"
}

# Poll with the feature checker; repeat after shutdown to catch late documents.
# The checker receives the ES file first, followed by feature-specific arguments.
verify_bpf_mock_es_events() {
	local feature=$1 output=$2
	shift 2
	local checker=${BPF_MOCK_CHECKER_BIN:-${HUATUO_BAMAI_TEST_TMPDIR}/${feature}-mock-checker}
	build_bpf_mock_checker "${feature}" "${checker}"
	wait_until 30 0.5 bpf_mock_es_verified "${checker}" "${output}" "$@" || fatal "${feature} ES verification failed; see mock-event-checker.log and es-search.json"
	cat "${HUATUO_BAMAI_TEST_TMPDIR}/mock-event-checker.log"
}

bpf_mock_es_verified() {
	local checker=$1 output=$2
	shift 2
	read_bpf_mock_es_events "${output}" || return 1
	"${checker}" "${output}" "$@" > "${HUATUO_BAMAI_TEST_TMPDIR}/mock-event-checker.log" 2>&1
}
