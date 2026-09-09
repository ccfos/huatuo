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

WORK_DIR=$(mktemp -d "${HUATUO_BAMAI_TEST_TMPDIR}/iolatency-accounting.XXXXXX")
FIXTURE="${ROOT_DIR}/integration/testdata/iolatency_accounting.c"

# Native helper stubs exercise the production completion path and a forced
# insertion race. Compiling the real BPF target separately is not a load test.
command -v clang > /dev/null || fatal "clang not found in PATH"
clang -O2 -pthread -Wall -Wextra -Werror -Wno-unknown-attributes \
	-I "${ROOT_DIR}/bpf/include" "${FIXTURE}" -o "${WORK_DIR}/iolatency_accounting" \
	2> "${WORK_DIR}/native.compile.log" \
	|| fatal "clang failed compiling ${FIXTURE}:"$'\n'"$(< "${WORK_DIR}/native.compile.log")"
"${WORK_DIR}/iolatency_accounting"
"${WORK_DIR}/iolatency_accounting" --bounds
if (($# > 0)); then
	"${WORK_DIR}/iolatency_accounting" "$@"
fi
compile_bpf_fixture "${ROOT_DIR}/bpf/iolatency_tracing.c" "${WORK_DIR}/iolatency_tracing.o"
log_info "I/O latency completion-path regression and BPF target compilation passed"
