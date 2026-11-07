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

# Shared public metric assertions for memory collectors.
set -euo pipefail

# Compare the entire selected family, retaining duplicates and checking gauge types.
# Container assertions select one known Pod; other Pods may expose different fields.
assert_memory_metric_samples() {
	local prefix=$1 expected_samples=$2 target_pod=${3:-} active=${4:-1}
	local collector=${prefix#huatuo_bamai_}
	collector=${collector%_}
	collector=${collector%_container}
	local actual="${HUATUO_BAMAI_TEST_TMPDIR}/memory-actual.txt"
	local expected="${HUATUO_BAMAI_TEST_TMPDIR}/memory-expected.txt"
	huatuo_bamai_collect_metrics
	awk -v collector="${collector}" -v active="${active}" '
  index($1, "huatuo_bamai_scrape_collector_success{") == 1 && index($0, "collector=\"" collector "\"") > 0 {
   count++; if ($NF != 1) bad=1
  }
  END { exit bad || count != active }
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" \
		|| fatal "${collector}: expected ${active} successful collector sample; actual scrape: $(grep 'scrape_collector_success{' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt")"
	awk -v prefix="${prefix}" -v pod="${target_pod}" '
  $1 == "#" && $2 == "TYPE" { types[$3] = $4; next }
  index($1, prefix) == 1 && (pod == "" || index($0, "container_host=\"" pod "\"") > 0) {
   print
   name=$1; sub(/\{.*/, "", name); names[name]=1
  }
  END { for (name in names) print "# TYPE " name " " types[name] }
 ' "${HUATUO_BAMAI_TEST_TMPDIR}/metrics.txt" | LC_ALL=C sort > "${actual}"
	awk '{ print; name=$1; sub(/\{.*/, "", name); names[name]=1 }
  END { for (name in names) print "# TYPE " name " gauge" }
 ' "${expected_samples}" | LC_ALL=C sort > "${expected}"
	diff -u "${expected}" "${actual}" || fatal "${prefix}: expected and actual samples differ (Pod=${target_pod:-host})"
}
