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

# Verify Elasticsearch configuration reaches startup validation through TOML.
# The complete case disables backend connections; storage I/O has separate tests.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"

readonly CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

require_commands curl ss timeout
[[ -x "${HUATUO_BAMAI_BIN}" ]] \
	|| fatal "huatuo-bamai binary missing: ${HUATUO_BAMAI_BIN}"

STORAGE_HTTP_PORT=$(allocate_available_port) \
	|| fatal "failed to allocate a huatuo-bamai API port"
readonly STORAGE_HTTP_PORT
HUATUO_BAMAI_ADDR="http://127.0.0.1:${STORAGE_HTTP_PORT}"
HUATUO_BAMAI_METRICS_API="${HUATUO_BAMAI_ADDR}/metrics"

trap huatuo_bamai_stop EXIT

# Each digit says whether Address, Username, or Password is configured.
for storage_config_case in 000 001 010 011 100 101 110 111; do
	storage_address=""
	storage_username=""
	storage_password=""
	[[ ${storage_config_case:0:1} == 0 ]] || storage_address="http://elasticsearch.invalid"
	[[ ${storage_config_case:1:1} == 0 ]] || storage_username="elastic"
	[[ ${storage_config_case:2:1} == 0 ]] || storage_password="storage-config-test"
	storage_config_log="${HUATUO_BAMAI_TEST_TMPDIR}/storage-config-${storage_config_case}.log"
	log_info "Elasticsearch Address/Username/Password configured: ${storage_config_case}"

	case ${storage_config_case} in
	000 | 111)
		storage_config_args=(--region dev --disable-kubelet --procfs-prefix "${HUATUO_BAMAI_TEST_FIXTURES}")
		[[ ${storage_config_case} == 000 ]] || storage_config_args+=(--disable-storage)
		integration_huatuo_bamai_start write_storage_config "${storage_config_args[@]}"
		huatuo_bamai_stop
		cp "${HUATUO_BAMAI_TEST_TMPDIR}/huatuo.log" "${storage_config_log}"
		assert_log_has_no_failure "${storage_config_log}" "Elasticsearch ${storage_config_case}"
		;;
	*)
		write_storage_config
		storage_config_status=0
		# Invalid configuration must exit before any daemon resources are started.
		timeout --kill-after=2 10 "${HUATUO_BAMAI_BIN}" \
			--config-dir "${HUATUO_BAMAI_TEST_TMPDIR}" --config bamai.conf --region dev \
			> "${storage_config_log}" 2>&1 || storage_config_status=$?
		assert_eq "${storage_config_status}" 1 "Elasticsearch ${storage_config_case} exit status" \
			|| fatal "partial Elasticsearch configuration did not fail: ${storage_config_log}"
		grep -Fq 'validating Elasticsearch config: address, username, and password must be configured together' \
			"${storage_config_log}" \
			|| fatal "partial Elasticsearch configuration did not report the missing fields: ${storage_config_log}"
		;;
	esac
done
