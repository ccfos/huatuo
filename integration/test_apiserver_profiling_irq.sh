#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

# Exercise the real IRQ CLI through the profiling API, publication and queries.
# Stack decoding is covered by irqtracing tests; this case checks service contracts.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/lib_storage.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/lib_continuous_profiling.sh"
source "${ROOT_DIR}/integration/lib_apiserver_query.sh"

readonly IRQ_PROFILE_TYPE='irqtracing:irq:count:irq:count'
readonly IRQ_PROFILE_DURATION=8
readonly IRQ_PROFILE_TOOL_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/irq-tools"
readonly IRQ_PROFILE_BPF_DIR="${HUATUO_BAMAI_TEST_TMPDIR}/irq-bpf"
# Existing configuration writers and status helpers consume these shared inputs.
API_TOKEN=integration-admin
OTHER_API_TOKEN=integration-other
ES_PASSWORD=huatuo-integration

require_commands curl jq ss docker python3 timeout
docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"
tracepoint_available irq softirq_raise || skip "irq/softirq_raise is unavailable"
tracepoint_available irq softirq_entry || skip "irq/softirq_entry is unavailable"

APISERVER_PORT=$(allocate_available_port) || fatal "failed to allocate an apiserver port"
APISERVER_ADDR="http://127.0.0.1:${APISERVER_PORT}"
irq_profile_load_pid=""

cleanup() {
	local status=$?
	if [[ -n ${irq_profile_load_pid} ]]; then
		stop_by_pid "${irq_profile_load_pid}" 5 || true
		wait "${irq_profile_load_pid}" || true
	fi
	continuous_profiling_cleanup "${status}"
}
trap cleanup EXIT

irq_profile_request() {
	local method=$1 path=$2 expected=$3 body=${4:-} status curl_status=0
	local -a args=()
	irq_profile_response="${HUATUO_BAMAI_TEST_TMPDIR}/response.json"
	[[ -z ${body} ]] || args=(-H 'Content-Type: application/json' --data-binary "${body}")
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -X "${method}" \
		-H "Authorization: Bearer ${API_TOKEN}" "${args[@]}" \
		-o "${irq_profile_response}" -w '%{http_code}' \
		"${APISERVER_ADDR}${path}" 2> "${irq_profile_response}.err") || curl_status=$?
	report_http_response "${method} ${path}" "${irq_profile_response}" "${irq_profile_response}.err"
	[[ ${curl_status} == 0 && ${status} == "${expected}" ]] \
		|| fatal "${method} ${path}: curl=${curl_status}, HTTP=${status}, expected ${expected}"
}

assert_irq_profile_window() {
	jq -e --arg type "${IRQ_PROFILE_TYPE}" '
        def valid_losses:
            type == "object" and all(.[];
                type == "number" and . >= 0 and floor == .);
        .data.items | length == 1 and all(.[];
            .profile_type == $type and .profile != null and
            .profile.duration_nanos > 0 and
            ((has("dropped_samples") | not) or (.dropped_samples | valid_losses)))
    ' "$1" > /dev/null || return 1
}

write_config_bamai_irq_profiling() {
	write_config_blacklist_except
	write_config_bamai_http
	write_config_bamai_auth "integration-node-token"
	write_config_bamai_elasticsearch "${STORAGE_ADDR}" "elastic" "${ES_PASSWORD}" \
		"$(storage_test_index huatuo_continuous_profiling_test)"
	write_config_storage_local ""
}

mkdir -p "${IRQ_PROFILE_TOOL_DIR}"
cp "${ROOT_DIR}/_output/bin/irqtracing" "${IRQ_PROFILE_TOOL_DIR}/irqtracing"
cp -a "${ROOT_DIR}/_output/bpf" "${IRQ_PROFILE_BPF_DIR}"
storage_start
integration_huatuo_bamai_start write_config_bamai_irq_profiling \
	--region integration --disable-kubelet \
	--tools-bin-dir "${IRQ_PROFILE_TOOL_DIR}" --bpf-dir "${IRQ_PROFILE_BPF_DIR}"
integration_huatuo_apiserver_start write_config_apiserver_continuous_profiling

# Keep loopback softirq activity present throughout collection without requiring PMU sampling.
timeout 120 python3 -c '
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind(("127.0.0.1", 0))
destination = s.getsockname()
while True:
    s.sendto(b"irq", destination)
' > "${HUATUO_BAMAI_TEST_TMPDIR}/irq-load.log" 2>&1 &
irq_profile_load_pid=$!

irq_profile_request POST /v1/profiling 201 \
	"{\"hostname\":\"127.0.0.1\",\"duration_seconds\":${IRQ_PROFILE_DURATION},\"scope\":\"host\",\"type\":\"irq\",\"mode\":\"softirq\"}"
cp "${irq_profile_response}" "${HUATUO_BAMAI_TEST_TMPDIR}/create-irq.json"
irq_profile_id=$(jq -er '.data.request_id' "${irq_profile_response}")
jq -e '.data | .type == "irq" and .mode == "softirq" and (has("language") | not)' \
	"${irq_profile_response}" > /dev/null || fatal "IRQ creation metadata is invalid"
irq_profile_started=${SECONDS}
wait_until 90 1 continuous_profile_status_is "${irq_profile_id}" completed \
	"${HUATUO_BAMAI_TEST_TMPDIR}/irq-status.json" \
	|| fatal "IRQ did not complete: ${continuous_profile_status_diagnostic}; inspect irq-status.json"
record_test_timing irq-profiling collection-and-publication "${irq_profile_started}"

irq_profile_request GET "/v1/profiling/${irq_profile_id}" 200
jq -e '.data.result_url | contains("irqtracing%3Airq%3Acount%3Airq%3Acount")' \
	"${irq_profile_response}" > /dev/null || fatal "IRQ result URL is missing its profile type"
irq_profile_request GET "/v1/profiling/${irq_profile_id}/raw" 200
cp "${irq_profile_response}" "${HUATUO_BAMAI_TEST_TMPDIR}/irq-raw.json"
assert_irq_profile_window "${irq_profile_response}" \
	|| fatal "IRQ window or loss diagnostics are invalid"

irq_profile_selector=$(jq -nr --arg id "${irq_profile_id}" --arg type "${IRQ_PROFILE_TYPE}" \
	'"{scope=\"host\",id="+($id|tojson)+",__profile_type__="+($type|tojson)+"}"')
irq_profile_query=$(jq -nc --arg s "${irq_profile_selector}" '{matchers:[$s],end:"4102444800000"}')
assert_profile_query "${APISERVER_ADDR}" "${API_TOKEN}" LabelNames "${irq_profile_query}" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/irq-labels.json"
jq -e '.names | index("irq") != null and index("softirq_vector") != null and index("cpu") != null' \
	"${HUATUO_BAMAI_TEST_TMPDIR}/irq-labels.json" > /dev/null || fatal "IRQ sample labels are missing"
for irq_profile_label in irq softirq_vector cpu; do
	irq_profile_query=$(jq -nc --arg s "${irq_profile_selector}" --arg name "${irq_profile_label}" \
		'{name:$name,matchers:[$s],end:"4102444800000"}')
	assert_profile_query "${APISERVER_ADDR}" "${API_TOKEN}" LabelValues "${irq_profile_query}" \
		"${HUATUO_BAMAI_TEST_TMPDIR}/irq-${irq_profile_label}.json"
	jq -e '.names | length > 0' "${HUATUO_BAMAI_TEST_TMPDIR}/irq-${irq_profile_label}.json" > /dev/null \
		|| fatal "IRQ label ${irq_profile_label} has no values"
done
irq_profile_query=$(jq -nc --arg type "${IRQ_PROFILE_TYPE}" --arg id "${irq_profile_id}" \
	'{profileTypeID:$type,labelSelector:("{scope=\"host\",id="+($id|tojson)+",irq=\"source\"}"),end:"4102444800000"}')
assert_profile_query "${APISERVER_ADDR}" "${API_TOKEN}" SelectMergeStacktraces "${irq_profile_query}" \
	"${HUATUO_BAMAI_TEST_TMPDIR}/irq-flamegraph.json"
jq -e '.flamegraph.total | tonumber > 0' "${HUATUO_BAMAI_TEST_TMPDIR}/irq-flamegraph.json" > /dev/null \
	|| fatal "IRQ source filter returned no samples"

storage_request "${HUATUO_BAMAI_TEST_TMPDIR}/irq-metadata.json" \
	-H 'Content-Type: application/json' \
	"${STORAGE_ADDR}/$(storage_test_index huatuo_continuous_profiling_test)*/_search" \
	-d "$(jq -nc --arg id "${irq_profile_id}" '{query:{term:{"tracer_id.keyword":$id}}}')"
jq -e --slurpfile raw "${HUATUO_BAMAI_TEST_TMPDIR}/irq-raw.json" '
    (.hits.hits | length == 1 and all(.[]._source;
        .tracer_name == "irqtracing" and .tracer_type == "profiling")) and
    ((.hits.hits[0]._source.profile_data.metrics.dropped_samples // {}) ==
        ($raw[0].data.items[0].dropped_samples // {}))
' "${HUATUO_BAMAI_TEST_TMPDIR}/irq-metadata.json" > /dev/null \
	|| fatal "IRQ metadata or loss counts differ between storage and raw results"
