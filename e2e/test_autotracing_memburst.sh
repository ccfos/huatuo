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

set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"
require_commands docker
# Use a locally available image; never pull during an acceptance test.
export MEMBURST_CONTAINER_IMAGE=${MEMBURST_CONTAINER_IMAGE:-${BUSINESS_POD_IMAGE}}
docker image inspect "${MEMBURST_CONTAINER_IMAGE}" > /dev/null || fatal "container image is not available locally"
docker info > /dev/null || fatal "Docker daemon is unavailable"
export MEMBURST_CONTAINER_ONLY=1
export MEMBURST_ACCEPTANCE_TEST_PATTERN=^TestMemburstAcceptanceContainer$
source "${ROOT_DIR}/integration/test_basic_autotracing_memburst.sh"
