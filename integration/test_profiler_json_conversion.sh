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

# Verify a JSON conversion failure preserves existing profiling output.
set -euo pipefail
source "${ROOT_DIR}/integration/lib.sh"

require_commands go
cd "${ROOT_DIR}"
go test -mod=vendor -tags=integration -count=1 -v \
	-run '^TestJSONConversionFailurePreservesOutput$' \
	./integration/testdata/test_profiler_json_conversion_test.go
