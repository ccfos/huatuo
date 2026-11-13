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

# Emit deterministic async-profiler output; assertions belong to the test script.
set -euo pipefail

pprof_action=$1
shift
pprof_output=""
while (($#)); do
	case $1 in
	-f)
		pprof_output=$2
		shift 2
		;;
	*)
		shift
		;;
	esac
done

if [[ ${pprof_action} == stop && -n ${pprof_output} ]]; then
	cat > "${pprof_output}" << 'SAMPLES'
shared 2
shared_[k] 3
shared;shared_[k] 5
SAMPLES
fi
