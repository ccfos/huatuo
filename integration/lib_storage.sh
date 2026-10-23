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
STORAGE_SHARED=false

storage_test_index() {
	local workspace=${HUATUO_BAMAI_TEST_TMPDIR##*/}
	printf '%s_%s' "$1" "${workspace,,}"
}

storage_start() {
	if [[ ${1:-default} == default && -z ${2:-} && -n ${STORAGE_SHARED_DIR:-} ]]; then
		require_commands flock
		mkdir -p "${STORAGE_SHARED_DIR}"
		storage_start_locked "$@" 9> "${STORAGE_SHARED_DIR}/start.lock"
	else
		storage_start_locked "$@"
	fi
}

storage_start_locked() {
	local profile=${1:-default} data_dir=${2:-} scheme=http started=${SECONDS}
	local -a mounts=()
	if [[ ${profile} == default && -z ${data_dir} && -n ${STORAGE_SHARED_DIR:-} ]]; then flock 9; fi
	record_test_timing "storage:${profile}" lock_wait "${started}"
	STORAGE_SHARED=false
	if [[ ${profile} == default && -z ${data_dir} && -n ${STORAGE_SHARED_DIR:-} && -f ${STORAGE_SHARED_DIR}/es.env ]]; then
		source "${STORAGE_SHARED_DIR}/es.env"
		STORAGE_SHARED=true
		return 0
	fi
	local image="${STORAGE_TEST_IMAGE:-${STORAGE_DEFAULT_IMAGE}}"
	# Tiny test indexes need fixed free-space reserves on shared Docker disks.
	local -a environment=(
		--env discovery.type=single-node
		--env cluster.routing.allocation.disk.watermark.low=1gb
		--env cluster.routing.allocation.disk.watermark.high=512mb
		--env cluster.routing.allocation.disk.watermark.flood_stage=256mb
	)
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
	started=${SECONDS}
	if [[ ${profile} != default ]] || ! docker image inspect "${image}" > /dev/null 2>&1; then
		log_info "pulling storage image: ${image}"
		if ! timeout 5m docker pull "${image}" \
			> "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-pull.log" 2>&1; then
			cat "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-pull.log" >&2
			skip "failed to pull storage image: ${image}"
		fi
	fi
	record_test_timing "storage:${profile}" image_pull "${started}"

	if [[ -n ${data_dir} ]]; then
		[[ ${profile} == default ]] || fatal "data directory binding requires the default storage profile"
		mkdir -p "${data_dir}"
		chown 1000:0 "${data_dir}"
		chmod 0770 "${data_dir}"
		mounts=(--mount "type=bind,source=${data_dir},target=/usr/share/elasticsearch/data")
	fi

	# Keep failed containers available for diagnostics until explicit cleanup.
	started=${SECONDS}
	STORAGE_CONTAINER_ID=$(docker run --detach \
		--publish 127.0.0.1::9200 \
		"${environment[@]}" "${mounts[@]}" \
		"${image}" \
		2> "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-run.log") || {
		cat "${HUATUO_BAMAI_TEST_TMPDIR}/elasticsearch-run.log" >&2
		fatal "failed to start storage image: ${image}"
	}
	local port
	port=$(docker port "${STORAGE_CONTAINER_ID}" 9200/tcp \
		| awk -F: 'NR == 1 { print $NF }')
	[[ -n "${port}" ]] || fatal "failed to resolve storage port"
	record_test_timing "storage:${profile}" container_start "${started}"

	STORAGE_ADDR="${scheme}://127.0.0.1:${port}"
	# Cold JVM startup can exceed two minutes in the distro VMs. Poll actual
	# readiness so faster hosts proceed immediately within this bounded wait.
	started=${SECONDS}
	wait_until 300 2 storage_ready \
		|| fatal "storage did not become ready at ${STORAGE_ADDR}"
	record_test_timing "storage:${profile}" readiness "${started}"
	if [[ ${profile} == default && -z ${data_dir} && -n ${STORAGE_SHARED_DIR:-} ]]; then
		mkdir -p "${STORAGE_SHARED_DIR}" || return 1
		printf 'STORAGE_CONTAINER_ID=%q\nSTORAGE_ADDR=%q\nSTORAGE_USERNAME=%q\nSTORAGE_PASSWORD=%q\n' \
			"${STORAGE_CONTAINER_ID}" "${STORAGE_ADDR}" "${STORAGE_USERNAME}" "${STORAGE_PASSWORD}" \
			> "${STORAGE_SHARED_DIR}/es.env.tmp" || return 1
		mv "${STORAGE_SHARED_DIR}/es.env.tmp" "${STORAGE_SHARED_DIR}/es.env" || return 1
		STORAGE_SHARED=true
	fi
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
	# Case cleanup releases its handle; the runner owns the shared container.
	if [[ ${STORAGE_SHARED} != true ]]; then
		docker rm -fv "${STORAGE_CONTAINER_ID}" > /dev/null 2>&1 || return 1
	fi
	STORAGE_CONTAINER_ID=""
	STORAGE_ADDR=""
	STORAGE_USERNAME=""
	STORAGE_PASSWORD=""
	STORAGE_SHARED=false
}

stop_shared_storage() {
	local status=$1
	[[ -d ${STORAGE_SHARED_DIR} ]] || return 0
	if [[ -f ${STORAGE_SHARED_DIR}/es.env ]]; then
		source "${STORAGE_SHARED_DIR}/es.env"
		STORAGE_SHARED=false
		if [[ ${status} -ne 0 && ${status} -ne 77 ]]; then
			storage_dump_logs "${STORAGE_SHARED_DIR}/elasticsearch.log" || true
			log_error "shared storage artifacts: ${STORAGE_SHARED_DIR}"
		fi
		storage_stop || return 1
	fi
	if [[ ${status} -eq 0 || ${status} -eq 77 ]]; then
		rm -rf -- "${STORAGE_SHARED_DIR}"
	fi
}
