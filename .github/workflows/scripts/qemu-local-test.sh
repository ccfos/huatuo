#!/usr/bin/env bash
#
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
#

# Purpose: Run the QEMU VM test lifecycle locally from one command.
# Caller: Local developers only; GitHub Actions does not invoke this script.
# Environment:
# - VM_IMAGE_REF: Image reference override passed to qemu-1.
# - VM_IMAGE_PULL: Image pull policy passed to qemu-1.
# - VM_PROXY_HOST: Optional host proxy address passed to qemu-2.
# - VM_PROXY_PORT: Optional host proxy port passed to qemu-2.
# - RUNNER_TEMP: Preferred parent directory for local context and run storage.
# - TMPDIR: Storage fallback when RUNNER_TEMP is unset; defaults to /tmp.
# Parameters:
# - ARCH: Optional architecture; defaults to amd64.
# - DISTRO: Optional distribution; defaults to ubuntu24.04.
# Examples:
# - .github/workflows/scripts/qemu-local-test.sh  # run the default local VM test
# - .github/workflows/scripts/qemu-local-test.sh amd64 ubuntu24.04

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)
source "$ROOT_DIR/.github/workflows/scripts/vm-test/logging.sh"
ARCH=${1:-amd64}
DISTRO=${2:-ubuntu24.04}
shift $((2 > $# ? $# : 2))
(($# == 0)) || {
	vm_log_error 'usage: qemu-local-test.sh [ARCH] [DISTRO]'
	exit 2
}

context_file=$(mktemp "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/huatuo-vm-context.XXXXXXXX")
cleanup() {
	local code=$?
	if [[ -s "$context_file" ]]; then
		# The wrapper scripts write simple NAME=VALUE records to this private file.
		set -a
		source "$context_file"
		set +a
		.github/workflows/scripts/qemu-3-cleanup.sh || true
	fi
	rm -f "$context_file"
	exit "$code"
}
trap cleanup EXIT

cd "$ROOT_DIR"
vm_log "starting local VM test for ${DISTRO}/${ARCH}"
.github/workflows/scripts/qemu-0-host-init.sh "$ARCH"
VM_CONTEXT_OUTPUT="$context_file" .github/workflows/scripts/qemu-1-start-vm.sh "$ARCH" "$DISTRO"
set -a
source "$context_file"
set +a
.github/workflows/scripts/qemu-2-run-in-vm.sh
