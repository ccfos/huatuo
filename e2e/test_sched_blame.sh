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
source "${ROOT_DIR}/e2e/lib.sh"

competitor_name="sched-blame-e2e"
competitor_pod="${competitor_name}-1"
competitor_label="app=${competitor_name}"
target_pid=""
competitor_pid=""

cleanup() {
	if [[ -n "${target_pid}" ]]; then
		kubectl exec -n "${BUSINESS_POD_NS}" "${BUSINESS_DEFAULT_POD_NAME}" \
			-- kill "${target_pid}" > /dev/null 2>&1 || true
	fi
	if [[ -n "${competitor_pid}" ]]; then
		kubectl exec -n "${BUSINESS_POD_NS}" "${competitor_pod}" \
			-- kill "${competitor_pid}" > /dev/null 2>&1 || true
	fi
	k8s_delete_pod "${BUSINESS_POD_NS}" "${competitor_label}" \
		> /dev/null 2>&1 || true
}
trap cleanup EXIT

allowed_cpus() {
	local pod=$1
	kubectl exec -n "${BUSINESS_POD_NS}" "${pod}" \
		-- awk '/Cpus_allowed_list/ { print $2 }' /proc/self/status
}

shared_cpu() {
	python3 - "$1" "$2" << 'PY'
import sys


def expand(spec):
    cpus = set()
    for field in spec.strip().split(","):
        bounds = [int(value) for value in field.split("-", 1)]
        if len(bounds) == 1:
            cpus.add(bounds[0])
        else:
            cpus.update(range(bounds[0], bounds[1] + 1))
    return cpus


common = sorted(expand(sys.argv[1]) & expand(sys.argv[2]))
if not common:
    raise SystemExit("pods have no shared allowed CPU")
print(common[0])
PY
}

start_busy_loop() {
	local pod=$1 cpu=$2
	kubectl exec -n "${BUSINESS_POD_NS}" "${pod}" -- sh -c \
		"taskset -c ${cpu} sh -c 'while :; do :; done' >/dev/null 2>&1 & echo \$!"
}

sched_blame_observed_contention() {
	[[ -s "${SCHED_BLAME_RATIO_FILE}" ]] || return 1
	python3 - "${SCHED_BLAME_RATIO_FILE}" "${BUSINESS_DEFAULT_POD_NAME}" << 'PY'
import csv
import sys


with open(sys.argv[1], newline="", encoding="utf-8") as source:
    rows = csv.DictReader(source)
    for row in rows:
        if row["target_container_name"] != sys.argv[2]:
            continue
        if row["sample_valid"].lower() != "true":
            continue
        if (
            float(row["target_runtime_ns"]) > 0
            and float(row["external_contention_ns"]) > 0
            and float(row["current_external_contention_percent"]) > 0
        ):
            raise SystemExit(0)
raise SystemExit(1)
PY
}

log_info "creating SchedBlame competitor pod"
k8s_delete_pod "${BUSINESS_POD_NS}" "${competitor_label}" \
	> /dev/null 2>&1 || true
k8s_create_pod \
	"${BUSINESS_POD_NS}" \
	"${competitor_name}" \
	"${BUSINESS_POD_IMAGE}" \
	"${competitor_label}" \
	1
assert_kubelet_pod_count \
	"${BUSINESS_POD_NS}" \
	"^${competitor_pod}$" \
	1 \
	"SchedBlame competitor pod created"
assert_huatuo_bamai_pod_count \
	"^${competitor_pod}$" \
	1 \
	"SchedBlame competitor discovered"

cpu=$(shared_cpu \
	"$(allowed_cpus "${BUSINESS_DEFAULT_POD_NAME}")" \
	"$(allowed_cpus "${competitor_pod}")")
log_info "running SchedBlame workloads on CPU ${cpu}"
target_pid=$(start_busy_loop "${BUSINESS_DEFAULT_POD_NAME}" "${cpu}")
competitor_pid=$(start_busy_loop "${competitor_pod}" "${cpu}")

wait_until \
	"${WAIT_HUATUO_BAMAI_TIMEOUT}" \
	"${WAIT_HUATUO_BAMAI_INTERVAL}" \
	sched_blame_observed_contention \
	|| fatal "SchedBlame did not record external contention"

log_info "SchedBlame recorded external contention"
