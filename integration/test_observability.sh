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

# These live checks are opt-in because perf and bounded D-state workers need
# a disposable VM. Cgroup iterator fixtures have their own automatic runner.
set -euo pipefail

source "${ROOT_DIR}/integration/lib.sh"

command -v go > /dev/null || skip "go command is not installed"
if [[ -z "${HUATUO_CPU_LIVE_DIR:-}" && -z "${HUATUO_TRIGGER_BPF_DIR:-}" ]]; then
	skip "set HUATUO_CPU_LIVE_DIR (perf + perf.o) or HUATUO_TRIGGER_BPF_DIR on a test VM"
fi

cd "${ROOT_DIR}"
go test -mod=vendor -tags=integration \
	./integration/testdata/autotracing_test.go \
	./integration/testdata/memory_reclaim_test.go \
	-run '^Test(CPUHostLiveTrigger|DloadHostLiveTrigger|ReclaimLiveAttach)$' \
	-count=1 -timeout=3m -v
