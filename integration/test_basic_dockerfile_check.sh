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

# BuildKit subrequests validate Dockerfile contracts without executing build
# steps such as package installation, code generation, or compilation.

set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

readonly DOCKERFILE_CHECK_RELEASE="${ROOT_DIR}/Dockerfile"
readonly DOCKERFILE_CHECK_DEVEL="${ROOT_DIR}/Dockerfile.devel"
readonly DOCKERFILE_CHECK_OUTLINE="${HUATUO_BAMAI_TEST_TMPDIR}/dockerfile-outline.txt"
readonly DOCKERFILE_CHECK_TARGETS="${HUATUO_BAMAI_TEST_TMPDIR}/dockerfile-devel-targets.txt"

command -v docker > /dev/null || skip "docker command is not installed"
docker info > /dev/null 2>&1 || skip "docker daemon is unavailable"
docker build --help | grep -q -- '--call' \
	|| skip "docker build does not support --call; upgrade Docker Buildx"

log_info "checking release Dockerfile default build arguments"
docker build --call=outline \
	--file "${DOCKERFILE_CHECK_RELEASE}" "${ROOT_DIR}" \
	> "${DOCKERFILE_CHECK_OUTLINE}" 2>&1 \
	|| fatal "failed to evaluate release Dockerfile build arguments"
grep -Eq '^BUILD_MODE[[:space:]]+static([[:space:]]|$)' \
	"${DOCKERFILE_CHECK_OUTLINE}" \
	|| fatal "release Dockerfile BUILD_MODE default must be static"

log_info "checking release Dockerfile static target"
docker build --call=check \
	--file "${DOCKERFILE_CHECK_RELEASE}" "${ROOT_DIR}"

log_info "checking release Dockerfile nostatic target"
docker build --call=check \
	--build-arg BUILD_MODE=nostatic \
	--file "${DOCKERFILE_CHECK_RELEASE}" "${ROOT_DIR}"

log_info "checking development runtime target"
docker build --call=check \
	--target dev-runtime \
	--file "${DOCKERFILE_CHECK_DEVEL}" "${ROOT_DIR}"

log_info "checking development Dockerfile targets"
docker build --call=targets \
	--file "${DOCKERFILE_CHECK_DEVEL}" "${ROOT_DIR}" \
	> "${DOCKERFILE_CHECK_TARGETS}" 2>&1 \
	|| fatal "failed to evaluate development Dockerfile targets"
grep -Eq '^dev-runtime([[:space:]]|$)' "${DOCKERFILE_CHECK_TARGETS}" \
	|| fatal "development Dockerfile target missing: dev-runtime"
grep -Eq '^devel[[:space:]]+\(default\)([[:space:]]|$)' \
	"${DOCKERFILE_CHECK_TARGETS}" \
	|| fatal "development Dockerfile default target must be devel"

log_info "Dockerfile contracts passed"
