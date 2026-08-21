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

if [[ -n "${__HUATUO_LIB_STORAGE_SH_LOADED:-}" ]]; then
	return 0
fi
readonly __HUATUO_LIB_STORAGE_SH_LOADED=1

readonly DEFAULT_ELASTICSEARCH_TEST_IMAGE="docker.elastic.co/elasticsearch/elasticsearch:8.15.5"

ELASTICSEARCH_CONTAINER_ID=""
ELASTICSEARCH_ADDR=""

elasticsearch_start() {
	local image="${HUATUO_ES_TEST_IMAGE:-${DEFAULT_ELASTICSEARCH_TEST_IMAGE}}"
	if ! docker image inspect "${image}" > /dev/null 2>&1; then
		log_info "pulling Elasticsearch image: ${image}"
		if ! timeout 5m docker pull "${image}" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-pull.log" 2>&1; then
			skip "failed to pull Elasticsearch image: ${image}"
		fi
	fi

	ELASTICSEARCH_CONTAINER_ID=$(docker run --detach --rm \
		--publish 127.0.0.1::9200 \
		--env discovery.type=single-node \
		--env xpack.security.enabled=false \
		--env ES_JAVA_OPTS=-Xms512m\ -Xmx512m \
		"${image}" \
		2> "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-run.log")
	local port
	port=$(docker port "${ELASTICSEARCH_CONTAINER_ID}" 9200/tcp \
		| awk -F: 'NR == 1 { print $NF }')
	[[ -n "${port}" ]] || fatal "failed to resolve Elasticsearch port"

	ELASTICSEARCH_ADDR="http://127.0.0.1:${port}"
	wait_until 120 2 elasticsearch_ready \
		|| fatal "Elasticsearch did not become ready at ${ELASTICSEARCH_ADDR}"
}

elasticsearch_ready() {
	[[ -n "${ELASTICSEARCH_ADDR}" ]] || return 1
	curl -sf "${CURL_TIMEOUT[@]}" \
		"${ELASTICSEARCH_ADDR}/_cluster/health?wait_for_status=yellow&timeout=2s" \
		| jq -e '.timed_out == false and (.status == "yellow" or .status == "green")' \
			> /dev/null
}

elasticsearch_is_running() {
	[[ -n "${ELASTICSEARCH_CONTAINER_ID}" ]] || return 1
	docker inspect --format '{{.State.Running}}' \
		"${ELASTICSEARCH_CONTAINER_ID}" 2> /dev/null | grep -qx true
}

elasticsearch_dump_logs() {
	local output=${1:-"${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch.log"}
	[[ -n "${ELASTICSEARCH_CONTAINER_ID}" ]] || return 0
	docker logs "${ELASTICSEARCH_CONTAINER_ID}" > "${output}" 2>&1
}

elasticsearch_stop() {
	[[ -n "${ELASTICSEARCH_CONTAINER_ID}" ]] || return 0
	docker rm -f "${ELASTICSEARCH_CONTAINER_ID}" > /dev/null 2>&1
	ELASTICSEARCH_CONTAINER_ID=""
	ELASTICSEARCH_ADDR=""
}
