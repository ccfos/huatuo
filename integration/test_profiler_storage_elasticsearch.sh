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
source "${ROOT_DIR}/integration/lib_storage.sh"

# Bash cannot export arrays from the integration runner to this test process.
CURL_TIMEOUT=(--connect-timeout 2 --max-time 3)

command -v docker > /dev/null || skip "docker command is not installed"
docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"
command -v curl > /dev/null || skip "curl command is not installed"
command -v jq > /dev/null || skip "jq command is not installed"
command -v go > /dev/null || fatal "go command is not installed"

cleanup() {
	local status=$?
	if [[ ${status} -ne 0 ]]; then
		elasticsearch_dump_logs || true
	fi
	elasticsearch_stop || true
}
trap cleanup EXIT

elasticsearch_start

HUATUO_ES_TEST_ADDR="${ELASTICSEARCH_ADDR}" \
	go test ./internal/profiler/service \
	-run '^TestProfileStorageSearchProfilesElasticsearch$' -count=1
