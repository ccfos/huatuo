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

readonly STORAGE_DEFAULT_IMAGE="docker.elastic.co/elasticsearch/elasticsearch:8.15.5"

STORAGE_CONTAINER_ID=""
STORAGE_ADDR=""
STORAGE_USERNAME=""
STORAGE_PASSWORD=""

storage_start() {
	local profile=${1:-default} scheme=http
	local image="${STORAGE_TEST_IMAGE:-${STORAGE_DEFAULT_IMAGE}}"
	local -a environment=(--env discovery.type=single-node)
	STORAGE_USERNAME=""
	STORAGE_PASSWORD=""
	case ${profile} in
	default)
		environment+=(--env xpack.security.enabled=false --env ES_JAVA_OPTS=-Xms512m\ -Xmx512m)
		;;
	elasticsearch_v7)
		image="docker.elastic.co/elasticsearch/elasticsearch:7.10.1"
		STORAGE_USERNAME=elastic
		STORAGE_PASSWORD=123456
		environment+=(--env xpack.security.enabled=true --env ELASTIC_PASSWORD=123456
			--env ES_JAVA_OPTS=-Xms512m\ -Xmx512m)
		;;
	elasticsearch_v8)
		image="${STORAGE_DEFAULT_IMAGE}"
		scheme=https
		STORAGE_USERNAME=elastic
		STORAGE_PASSWORD=123456
		environment+=(--env ELASTIC_PASSWORD=123456 --env ES_JAVA_OPTS=-Xms512m\ -Xmx512m)
		;;
	opensearch_v2)
		image="opensearchproject/opensearch:2.6.0"
		scheme=https
		STORAGE_USERNAME=admin
		STORAGE_PASSWORD=admin
		environment+=(--env OPENSEARCH_JAVA_OPTS=-Xms512m\ -Xmx512m)
		;;
	*) fatal "unknown storage test profile: ${profile}" ;;
	esac

	# Compatibility cases verify the published images, including their pullability.
	if [[ ${profile} != default ]] || ! docker image inspect "${image}" > /dev/null 2>&1; then
		log_info "pulling storage image: ${image}"
		if ! timeout 5m docker pull "${image}" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-pull.log" 2>&1; then
			cat "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-pull.log" >&2
			skip "failed to pull storage image: ${image}"
		fi
	fi

	# Keep failed containers available for diagnostics until explicit cleanup.
	STORAGE_CONTAINER_ID=$(docker run --detach \
		--publish 127.0.0.1::9200 \
		"${environment[@]}" \
		"${image}" \
		2> "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-run.log") || {
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-run.log" >&2
		fatal "failed to start storage image: ${image}"
	}
	local port
	port=$(docker port "${STORAGE_CONTAINER_ID}" 9200/tcp \
		| awk -F: 'NR == 1 { print $NF }')
	[[ -n "${port}" ]] || fatal "failed to resolve storage port"

	STORAGE_ADDR="${scheme}://127.0.0.1:${port}"
	wait_until 120 2 storage_ready \
		|| fatal "storage did not become ready at ${STORAGE_ADDR}"
}

storage_curl() {
	local -a authentication=()
	if [[ -n ${STORAGE_USERNAME} ]]; then
		authentication+=(--user "${STORAGE_USERNAME}:${STORAGE_PASSWORD}")
	fi
	# Test images generate their own certificates; credentials still exercise auth.
	if [[ ${STORAGE_ADDR} == https://* ]]; then
		authentication+=(--insecure)
	fi
	curl -sS "${CURL_TIMEOUT[@]}" "${authentication[@]}" "$@"
}

storage_ready() {
	[[ -n "${STORAGE_ADDR}" ]] || return 1
	storage_curl --fail \
		"${STORAGE_ADDR}/_cluster/health?wait_for_status=yellow&timeout=2s" \
		2> "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-ready.err" \
		| jq -e '.timed_out == false and (.status == "yellow" or .status == "green")' \
			> /dev/null
}

storage_dump_logs() {
	local output=${1:-"${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch.log"}
	[[ -n "${STORAGE_CONTAINER_ID}" ]] || return 0
	docker logs "${STORAGE_CONTAINER_ID}" > "${output}" 2>&1
}

storage_stop() {
	[[ -n "${STORAGE_CONTAINER_ID}" ]] || return 0
	docker rm -fv "${STORAGE_CONTAINER_ID}" > /dev/null 2>&1 || return 1
	STORAGE_CONTAINER_ID=""
	STORAGE_ADDR=""
	STORAGE_USERNAME=""
	STORAGE_PASSWORD=""
}
