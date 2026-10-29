#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Exercise an already running development stack; Compose owns service lifecycles.
set -euo pipefail

if (($#)); then
	echo "Run run_test_profiling.sh without arguments after make compose-dev-up." >&2
	exit 2
fi
for dependency in curl jq; do
	command -v "$dependency" > /dev/null || {
		echo "Install $dependency before running run_test_profiling.sh." >&2
		exit 1
	}
done

readonly API_BASE='http://127.0.0.1:12740'
readonly GRAFANA_BASE='http://127.0.0.1:3000'
readonly TARGET_HOST='huatuo-dev-node'
readonly API_TOKEN='huatuo-dev-api-token'
readonly GRAFANA_AUTH='admin:admin'
readonly DURATION_SECONDS=60
readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 60)
ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
readonly ROOT_DIR
readonly OUTPUT_ROOT="${ROOT_DIR}/_output/profiling"
# mktemp creates the run directory, but requires its parent to exist.
mkdir -p -- "$OUTPUT_ROOT"
output_dir=$(mktemp -d "${OUTPUT_ROOT}/run.XXXXXX")
printf 'Results: %s\n' "$output_dir"

request() {
	local label=$1 url=$2
	shift 2
	local curl_status=0 http_status
	: > "${output_dir}/${label}.response.json"
	http_status=$(curl -sS "${CURL_TIMEOUT[@]}" \
		-o "${output_dir}/${label}.response.json" -w '%{http_code}' \
		"$@" "$url" 2> "${output_dir}/${label}.log") || curl_status=$?
	printf '%s: curl exit=%s, HTTP=%s\n' "$label" "$curl_status" "$http_status" >> "${output_dir}/${label}.log"
	if ((curl_status != 0)) || [[ ! "$http_status" =~ ^2[0-9][0-9]$ ]]; then
		echo "Request failed; responses: ${output_dir}" >&2
		cat "${output_dir}/${label}.log" "${output_dir}/${label}.response.json" >&2
		return 1
	fi
}

printf 'Creating %ss host CPU profiling task on %s...\n' "$DURATION_SECONDS" "$TARGET_HOST"
jq -n --arg host "$TARGET_HOST" --argjson duration "$DURATION_SECONDS" '{
	type:"cpu", language:"c", mode:"oncpu", scope:"host",
	duration_seconds:$duration, hostname:$host
}' > "${output_dir}/create.request.json"
request create "${API_BASE}/v1/profiling" -X POST \
	-H "Authorization: Bearer ${API_TOKEN}" -H 'Content-Type: application/json' \
	--data-binary "@${output_dir}/create.request.json"
job_id=$(jq -er '.data.request_id | select(type == "string" and test("^[A-Za-z0-9_-]+$"))' \
	"${output_dir}/create.response.json")
printf 'Job ID: %s\n' "$job_id"

deadline=$((SECONDS + DURATION_SECONDS + 120))
printf 'Waiting for Job completion...\n'
while true; do
	request status "${API_BASE}/v1/profiling/${job_id}" -H "Authorization: Bearer ${API_TOKEN}"
	status=$(jq -er .data.status "${output_dir}/status.response.json")
	[[ "$status" != terminal ]] || break
	if ((SECONDS >= deadline)); then
		echo "Timed out waiting for Job ${job_id}; latest response: ${output_dir}/status.response.json" >&2
		exit 1
	fi
	sleep 5
done
jq -c '.data | {status, terminal}' "${output_dir}/status.response.json"
if [[ $(jq -r .data.terminal.outcome "${output_dir}/status.response.json") != completed ]]; then
	echo "Profiling failed; inspect ${output_dir}/status.response.json and Agent logs." >&2
	exit 1
fi

request raw "${API_BASE}/v1/profiling/${job_id}/raw?limit=1" -H "Authorization: Bearer ${API_TOKEN}"
jq -er '.data.items | arrays | "Raw windows retrieved: \(length) (limit: 1)"' "${output_dir}/raw.response.json"

# The client may have a different clock from the development node.
start_ms=$(jq -er '.data.created_at | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601 * 1000' "${output_dir}/status.response.json")
# Round up the server's end time to include subsecond publication timestamps.
end_ms=$(jq -er '.data.ended_at | sub("\\.[0-9]+Z$"; "Z") | (fromdateiso8601 + 1) * 1000' "${output_dir}/status.response.json")
jq -n --arg id "$job_id" --arg host "$TARGET_HOST" \
	--arg from "$start_ms" --arg to "$end_ms" '{
	from:$from, to:$to, queries:[{
		refId:"A", queryType:"profile",
		datasource:{type:"grafana-pyroscope-datasource",uid:"huatuo-apiserver-pyroscope"},
		profileTypeId:"process_cpu:cpu:nanoseconds:cpu:nanoseconds",
		labelSelector:("{hostname=" + ($host | tojson) + ",id=" + ($id | tojson) + "}")
	}]
}' > "${output_dir}/flamegraph.request.json"
request flamegraph "${GRAFANA_BASE}/api/ds/query" \
	-u "$GRAFANA_AUTH" \
	-H 'Content-Type: application/json' --data-binary "@${output_dir}/flamegraph.request.json"
jq -e 'if .results.A.status == 200 and (.results.A.error | not)
	then .results.A else error("Grafana query failed: " + (.results.A | tostring)) end' \
	"${output_dir}/flamegraph.response.json" > "${output_dir}/flamegraph.json"
node_count=$(jq '[.frames[]?.data.values[0] | length] | add // 0' "${output_dir}/flamegraph.json")
if ((node_count == 0)); then
	echo "Query succeeded but returned no flame graph nodes; inspect ${output_dir}/raw.response.json and the target workload." >&2
else
	printf 'Flame graph nodes: %s\n' "$node_count"
fi

host_query=$(jq -rn --arg host "$TARGET_HOST" '$host | @uri')
dashboard_url="${GRAFANA_BASE}/d/continuous-profiling-host?var-hostname=${host_query}&var-task_id=${job_id}&var-type=process_cpu%3Acpu%3Ananoseconds%3Acpu%3Ananoseconds&from=${start_ms}&to=${end_ms}"
printf '%s\n' "$dashboard_url" > "${output_dir}/grafana-url.txt"
printf 'Flame graph saved: %s/flamegraph.json\n' "$output_dir"
printf 'Grafana: %s\n' "$dashboard_url"
