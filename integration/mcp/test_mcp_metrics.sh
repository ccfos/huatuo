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
source "${ROOT_DIR}/integration/mcp/lib_mcp.sh"
source "${ROOT_DIR}/integration/mcp/assert_metrics.sh"
readonly MCP_SUITE=metrics
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 5)

mcp_setup "${MCP_PROM_IMAGE}" "${MCP_EXPORTER_IMAGE}"
mkdir -p "${HUATUO_BAMAI_TEST_TMPDIR}/exporter"
cp "${MCP_TEST_DIR}/prometheus_metrics.txt" "${HUATUO_BAMAI_TEST_TMPDIR}/exporter/metrics"
chmod 755 "${HUATUO_BAMAI_TEST_TMPDIR}/exporter"
cat > "${HUATUO_BAMAI_TEST_TMPDIR}/prometheus.yml" << 'EOF'
global:
  scrape_interval: 1s
scrape_configs:
  - job_name: mcp-fixture
    static_configs:
      - targets: ['mcp-fixture:8080']
EOF
mcp_start_container exporter --network-alias mcp-fixture \
	--mount "type=bind,src=${HUATUO_BAMAI_TEST_TMPDIR}/exporter,dst=/www,readonly" \
	"${MCP_EXPORTER_IMAGE}" httpd -f -p 8080 -h /www
mcp_start_container prometheus --publish 127.0.0.1::9090 \
	--mount "type=bind,src=${HUATUO_BAMAI_TEST_TMPDIR}/prometheus.yml,dst=/etc/prometheus/prometheus.yml,readonly" \
	--tmpfs /prometheus:rw,mode=1777 "${MCP_PROM_IMAGE}" \
	--config.file=/etc/prometheus/prometheus.yml --storage.tsdb.path=/prometheus
MCP_BACKEND=$(mcp_container_address prometheus 9090)

mcp_metrics_ready() {
	curl --noproxy '*' -fsS "${CURL_TIMEOUT[@]}" --get \
		--data-urlencode 'query=count(huatuo_mcp_test_value)' "${MCP_BACKEND}/api/v1/query" \
		| jq -e '.status == "success" and .data.result[0].value[1] == "4"' > /dev/null
}
wait_until 60 1 mcp_metrics_ready || fatal "test Prometheus did not ingest the fixture"
integration_huatuo_apiserver_start write_mcp_apiserver_config
assert_mcp_metrics
mcp_assert_shutdown
