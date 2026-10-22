#!/usr/bin/env bash

# Copyright 2026 The HuaTuo Authors.
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#     http://www.apache.org/licenses/LICENSE-2.0
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail
export PYTHONDONTWRITEBYTECODE=1

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/mcp/lib_client.sh"

readonly API_TOKEN="integration-mcp-token"
readonly MCP_PROM_IMAGE="prom/prometheus:v2.53.3"
readonly MCP_EXPORTER_IMAGE="busybox:1.36.1"
readonly MCP_TEST_DIR="${ROOT_DIR}/integration/mcp"
MCP_NETWORK=""
MCP_CIDFILES=()

mcp_cleanup() {
	local status=$? cidfile cid
	trap - EXIT
	huatuo_apiserver_stop || status=1
	if [[ -n ${STORAGE_CONTAINER_ID} ]]; then
		storage_dump_logs || true
		storage_stop || status=1
	fi
	for cidfile in "${MCP_CIDFILES[@]}"; do
		[[ -s ${cidfile} ]] || continue
		cid=$(< "${cidfile}")
		docker logs "${cid}" > "${cidfile%.cid}.log" 2>&1 || true
		docker rm -fv "${cid}" > /dev/null 2>&1 || status=1
	done
	if [[ -n ${MCP_NETWORK} ]]; then
		docker network rm "${MCP_NETWORK}" > /dev/null || status=1
	fi
	if ((status != 0 && status != 77)); then
		dump_text_files "${HUATUO_BAMAI_TEST_TMPDIR}" || true
	fi
	exit "${status}"
}

mcp_setup() {
	require_commands docker curl jq awk date ss timeout
	[[ ${MCP_SUITE} != events ]] || require_commands python3
	docker info > "${HUATUO_BAMAI_TEST_TMPDIR}/docker-info.log" 2>&1 \
		|| skip "dockerd is unavailable"
	local image
	for image in "$@"; do
		if ! docker image inspect "${image}" > /dev/null 2>&1; then
			timeout 5m docker pull "${image}" > "${HUATUO_BAMAI_TEST_TMPDIR}/image-pull.log" 2>&1 \
				|| skip "cannot pull ${image}; see image-pull.log"
		fi
	done
	trap mcp_cleanup EXIT
	# Only the Prometheus exporter needs container-to-container networking.
	if [[ ${MCP_SUITE} == metrics ]]; then
		MCP_NETWORK=$(docker network create "huatuo-mcp-${BASHPID}-${RANDOM}")
	fi
	APISERVER_PORT=$(allocate_available_port) || fatal "cannot allocate apiserver port"
	APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"
}

mcp_load_fixtures() {
	local stem="${HUATUO_BAMAI_TEST_TMPDIR}/$1" index definition
	[[ ${STORAGE_ADDR} =~ ^http://127\.0\.0\.1:[0-9]+$ ]] || fatal "refusing fixture writes outside the local test ES"
	assert_mcp_json "${stem}-indices.json" 'type == "object" and length > 0' "missing fixture indices"
	while IFS= read -r index; do
		definition=$(jq -c --arg index "${index}" '.[$index]' "${stem}-indices.json")
		storage_curl --noproxy '*' --fail -X PUT -H 'Content-Type: application/json' \
			--data-binary "${definition}" "${STORAGE_ADDR}/${index}" > "${stem}-${index}.response.json" \
			|| fatal "cannot create fixture index ${index}"
		assert_mcp_json "${stem}-${index}.response.json" '.acknowledged == true' "fixture index ${index} not acknowledged"
	done < <(jq -r 'keys[]' "${stem}-indices.json")
	storage_curl --noproxy '*' --fail -X POST -H 'Content-Type: application/x-ndjson' \
		--data-binary "@${stem}-bulk.ndjson" "${STORAGE_ADDR}/_bulk?refresh=true" > "${stem}-bulk.response.json" \
		|| fatal "cannot load fixture bulk input: ${stem}"
	assert_mcp_json "${stem}-bulk.response.json" '.errors == false' "fixture bulk write failed"
}

mcp_start_container() {
	local name=$1
	shift
	local cidfile="${HUATUO_BAMAI_TEST_TMPDIR}/${name}.cid"
	MCP_CIDFILES+=("${cidfile}")
	docker run --detach --network "${MCP_NETWORK}" --cidfile "${cidfile}" "$@" \
		> "${HUATUO_BAMAI_TEST_TMPDIR}/${name}-run.log" 2>&1 \
		|| fatal "cannot start ${name}; see ${name}-run.log"
}

mcp_container_address() {
	local name=$1 port=$2 published
	published=$(docker port "$(< "${HUATUO_BAMAI_TEST_TMPDIR}/${name}.cid")" "${port}/tcp")
	[[ ${published} == 127.0.0.1:* ]] || fatal "${name} is not published exclusively on loopback"
	printf 'http://%s\n' "${published}"
}

mcp_apiserver_exited() {
	! kill -0 "${MCP_APISERVER_PID}" 2> /dev/null
}

mcp_assert_shutdown() {
	MCP_APISERVER_PID=$(< "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-apiserver.pid")
	kill -TERM "${MCP_APISERVER_PID}"
	wait_until 10 0.2 mcp_apiserver_exited || fatal "MCP apiserver did not shut down in time"
	wait "${MCP_APISERVER_PID}" || fatal "MCP apiserver shutdown failed"
	rm -f "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo-apiserver.pid"
}

write_mcp_apiserver_config() {
	write_apiserver_apis_config
	cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf" << 'EOF'

[APIServer.RateLimit]
RequestsPerSecond = 1000
Burst = 1000
[MCP]
Enabled = true
UserRateLimit = 0
EOF
	if [[ ${MCP_SUITE} == events ]]; then
		cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf" << EOF
[MCP.Events]
BackendRateLimit = 0
ToolTimeout = 10
[MCP.Events.ESCompatible]
Address = "${MCP_BACKEND}"
AuthType = "none"
Index = "huatuo_apiserver_mcp"
[[MCP.Events.Regions]]
NamePattern = "failed"
[MCP.Events.Regions.ESCompatible]
Index = "huatuo_apiserver_mcp_missing"
EOF
		local region
		for region in alias plain source overflow millis payload mixed-payload scoped-payload payload-overflow; do
			cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf" << EOF
[[MCP.Events.Regions]]
NamePattern = "^${region}-test\$"
[MCP.Events.Regions.ESCompatible]
Index = "huatuo_apiserver_mcp_compat_${region}-test"
EOF
		done
	else
		cat >> "${HUATUO_BAMAI_TEST_TMPDIR}/apiserver.conf" << EOF
[MCP.Prometheus]
Address = "${MCP_BACKEND}"
AuthType = "none"
MetricPrefix = "huatuo_mcp_"
BackendRateLimit = 0
MaxSeriesPerQuery = 2
ToolTimeout = 10
[[MCP.Prometheus.Regions]]
NamePattern = "failed"
Address = "${MCP_BACKEND}/missing"
EOF
	fi
}
