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

assert_profile_windows() {
	local payload=$1 minimum=$2 expected_type=$3 container_id=${4:-}
	jq -e --argjson minimum "$minimum" --arg type "$expected_type" --arg container "$container_id" '
  .items | type == "array" and length >= $minimum and all(.[];
   (.profile | type == "object") and (.profile_type | type == "string") and
   ($type == "" or .profile_type == $type) and
   ($container == "" or .container_id == $container))' "$payload" > /dev/null || return 1
}

assert_grafana_flamegraph() {
	local response=$1
	jq -e '([.frames[]?.data.values[0] | length] | add // 0) > 0' "$response" > /dev/null || return 1
}

assert_profile_flamegraph() {
	local response=$1
	jq -e '.flamegraph | (.total | tonumber) > 0 and (.names | length) > 0 and (.levels | length) > 0' \
		"$response" > /dev/null || return 1
}
