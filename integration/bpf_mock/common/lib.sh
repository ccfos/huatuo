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

# Public entry point. Sourcing defines helpers without allocating test resources.
if [[ -n ${__HUATUO_BPF_MOCK_LIB_SH_LOADED:-} ]]; then
	return 0
fi
readonly __HUATUO_BPF_MOCK_LIB_SH_LOADED=1

source "${ROOT_DIR}/integration/lib.sh"
source "${ROOT_DIR}/integration/config.sh"
source "${ROOT_DIR}/integration/bpf_mock/common/core.sh"
source "${ROOT_DIR}/integration/bpf_mock/common/elasticsearch.sh"
source "${ROOT_DIR}/integration/bpf_mock/common/container.sh"
