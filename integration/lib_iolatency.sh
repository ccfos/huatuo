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

# Assert the public IOLatency schema and interval contract for host and container
# fixtures. Return 3 only during warmup or an unconfirmed idle/drain interval.

assert_iolatency_metrics() {
	local disk=$1 operation=$2 mode=$3 input=$4 started=$5 ended=$6 state=$7
	local previous=null result="${input}.assertion.json" decoded="${input}.json"
	[[ ! -r ${state} ]] || previous=$(< "${state}")
	awk '
	/^# TYPE huatuo_bamai_iolatency_/ {
		if (++seen[$3] > 1) { print "duplicate TYPE: " $3 > "/dev/stderr"; bad=1 }
	}
	END { exit bad }
	' "${input}" || return 1
	if (($# >= 8)); then
		decoded=$8
	else
		io_test_metrics_json "${input}" > "${decoded}" || return 1
	fi
	jq -e --arg disk "${disk}" --arg operation "${operation}" --arg mode "${mode}" \
		--argjson started "${started}" --argjson ended "${ended}" \
		--argjson previous "${previous}" '
		def require(condition; message): if condition then . else error(message) end;
		def finite:
			(.raw_value | test("^[+-]?([0-9]+([.][0-9]*)?|[.][0-9]+)([eE][+-]?[0-9]+)?$"))
			and (.value | type) == "number" and (.value | isnan | not) and (.value | isinfinite | not);
		def suffix: .name | ltrimstr("huatuo_bamai_iolatency_");
		def base: suffix | ltrimstr("container_");
		def latency: [0.00005,0.0001,0.00025,0.0005,0.001,0.002,0.004,
			0.008,0.016,0.032,0.064,0.128,0.256,0.512,1,2,"+Inf"];
		def bounds:
			if (base | endswith("io_size_bytes_bucket")) then [4096,16384,65536,262144,1048576,"+Inf"]
			else latency end;
		def intervals: ["interval_start_timestamp_seconds","interval_end_timestamp_seconds"];
		def histogram_names: ["q2d_seconds_bucket","d2c_seconds_bucket","q2g_seconds_bucket",
			"queued_io_size_bytes_bucket","issued_io_size_bytes_bucket"];
		def required_distributions: ["d2c_seconds_bucket", "queued_io_size_bytes_bucket", "issued_io_size_bytes_bucket"];
		def common: ["host","region"];
		def histogram_labels:
			common + ["disk","operation","le"]
			+ (if (suffix | startswith("container_"))
				then ["container_host","container_hostnamespace","container_name","container_type","container_level"]
				else [] end) | sort;
		def bound: if .labels.le == "+Inf" then "+Inf" else .labels.le | tonumber end;
		[.[] | select(.name | startswith("huatuo_bamai_iolatency_"))] as $public
		| [.[] | select(.name == "huatuo_bamai_scrape_collector_success")] as $collectors
		| require(all(($public + $collectors)[]; finite and .label_count == (.labels | length));
			"nonfinite metric or duplicate label")
		| require(all($public | group_by([.name, (.labels | to_entries | sort_by(.key))])[]; length == 1);
			"duplicate public sample")
		| require(all($public[]; .type_count == 1 and .type == "gauge"
			and .labels.host == "huatuo-dev" and .labels.region == "dev");
			"IOLatency Gauge type or public labels differ")
		| require(all($public[];
			if (suffix as $name | intervals | index($name)) != null
			then (.labels | keys) == common and .value > 0
			else (base as $name | histogram_names | index($name)) != null
				and (.labels | keys) == histogram_labels
				and (.labels.disk | length) > 0 and (.labels.operation == "read" or .labels.operation == "write")
				and .value >= 0 and .value == (.value | floor)
				and (bound as $upper | bounds | index($upper)) != null
			end); "invalid IOLatency name, labels, count or bucket bound")
		| [$public[] | select((suffix as $name | intervals | index($name)) == null)
			| . + {bound: bound}] as $buckets
		| [$buckets | group_by([.name, (.labels | del(.le))])[]
			| sort_by(.bound) | {suffix: (.[0] | suffix), labels: (.[0].labels | del(.le)),
				bounds: map(.bound), expected: (.[0] | bounds), counts: map(.value)}] as $groups
		| require(all($groups[]; .bounds == .expected and .counts == (.counts | sort));
			"missing, duplicate or nonmonotonic fixed buckets")
		| require(all($groups | group_by(.labels)[];
			(map(.suffix | ltrimstr("container_")) | sort) == (histogram_names | sort));
			"missing histogram family for disk, operation or owner")
		| require($mode != "idle" or all($groups[]; .labels.disk != $disk or all(.counts[]; . == 0));
			"idle disk repeated old samples")
		| [$public[] | select((suffix as $name | intervals | index($name)) != null)
			| {key: suffix, value: .value}] | from_entries as $interval
		| require(($groups | length) == 0 or ($interval | keys) == (intervals | sort);
			"histograms lack collector interval timestamps")
		| require(($interval | length) == 0 or ($interval | keys) == (intervals | sort);
			"incomplete interval timestamps")
		| require(($interval | length) == 0 or
			($interval.interval_start_timestamp_seconds <= $interval.interval_end_timestamp_seconds
			and $started - 1 <= $interval.interval_end_timestamp_seconds
			and $interval.interval_end_timestamp_seconds <= $ended + 1
			and ($previous == null or ($interval.interval_start_timestamp_seconds >= $previous - 0.000001
				and $interval.interval_end_timestamp_seconds >= $previous)));
			"collector interval overlaps, moves backwards or exceeds the observed scrape")
		| [$groups[] | select(.labels.disk == $disk and .labels.operation == $operation
			and (.suffix | startswith("container_") | not) and .counts[-1] > 0) | .suffix] | unique | sort as $positive
		| [$collectors[] | select(.labels.collector == "iolatency") | .value] as $health
		| ($health == [1] and all(required_distributions[]; . as $name | $positive | index($name) != null)) as $ready
		| require($health == [0] or $health == [1]; "missing or invalid IOLatency collector status")
		| require($mode != "active" or $ready;
			"completed IO lacks healthy positive D2C, queued-size or issued-size distributions")
		| {disk: $disk, operation: $operation, ready: $ready, health: $health,
			interval_start: $interval.interval_start_timestamp_seconds, interval_end: $interval.interval_end_timestamp_seconds,
			positive_distributions: $positive, missing_positive_distributions: (required_distributions - $positive),
			host_sample_counts: ([$groups[] | select(.labels.disk == $disk
				and (.suffix | startswith("container_") | not))] | group_by(.labels.operation)
				| map({key: .[0].labels.operation, value: (map({key: .suffix, value: .counts[-1]}) | from_entries)})
				| from_entries),
			distributions: ($groups | length), zero_buckets: ([$groups[].counts[] | select(. == 0)] | length)}
	' "${decoded}" > "${result}" || return 1
	if jq -e '.interval_end != null' "${result}" > /dev/null; then
		jq -r '.interval_end' "${result}" > "${state}"
	fi
	if [[ ${mode} == warmup ]] && ! jq -e '.ready' "${result}" > /dev/null; then
		return 3
	fi
	if [[ ${mode} == idle || ${mode} == drain ]] && jq -e '.health == [0]' "${result}" > /dev/null; then
		log_info "unconfirmed interval: success=0 may mean no data or a collection error"
		return 3
	fi
	log_info "${mode}: $(jq -c . "${result}")"
}

assert_iolatency_delay_comparison() {
	local disk=$1 baseline=$2 delayed=$3 restored=$4 result=$5
	jq -ne --arg disk "${disk}" \
		--slurpfile before "${baseline}.metrics.json" --slurpfile during "${delayed}.metrics.json" \
		--slurpfile after "${restored}.metrics.json" --slurpfile before_time "${baseline}.time.json" \
		--slurpfile during_time "${delayed}.time.json" --slurpfile after_time "${restored}.time.json" '
		def require(condition; message): if condition then . else error(message) end;
		def latency: [0.00005,0.0001,0.00025,0.0005,0.001,0.002,0.004,
			0.008,0.016,0.032,0.064,0.128,0.256,0.512,1,2,"+Inf"];
		def distribution:
			[.[] | select(.name == "huatuo_bamai_iolatency_d2c_seconds_bucket"
				and .labels.disk == $disk and .labels.operation == "read")
				| {upper: (if .labels.le == "+Inf" then "+Inf" else .labels.le | tonumber end), count: .value}]
			| sort_by(.upper)
			| require(length == 0 or (map(.upper) == latency
				and all(.[]; (.count | type) == "number" and (.count | isnan | not)
					and (.count | isinfinite | not) and .count >= 0 and .count == (.count | floor))
				and map(.count) == (map(.count) | sort)); "malformed D2C distribution")
			| if length == 0 or .[-1].count == 0 then null
				else .[-1].count as $total | map(. + {cdf: (.count / $total)}) end;
		[$before_time[0].seconds,$during_time[0].seconds,$after_time[0].seconds] as $seconds
		| require(all($seconds[]; type == "number" and (isnan | not) and (isinfinite | not) and . > 0);
			"invalid external workload duration")
		| ($before[0] | distribution) as $b
		| ($during[0] | distribution) as $d
		| ($after[0] | distribution) as $a
		| ([$seconds[0],$seconds[2]] | max) as $normal
		| require($b != null and $d != null and $a != null;
			"completed delay profile lacks a positive D2C distribution")
		| if $seconds[1] < $normal * 4 or $seconds[1] - $normal < 0.5
		then {result: "unconfirmed external delay", seconds: $seconds, confirmed: false}
		else .
			| [range(0; 16) | . as $index | {boundary: $b[$index].upper,
				separation: (([$b[$index].cdf,$a[$index].cdf] | min) - $d[$index].cdf)}]
			| max_by(.separation) as $shift
			| require($shift.separation >= 0.5;
				"externally confirmed delay did not shift and restore the D2C distribution")
			| {result: "shift and recovery confirmed", seconds: $seconds, confirmed: true,
				bucket_boundary_seconds: $shift.boundary, cdf_separation: $shift.separation}
		end
	' > "${result}" || return 1
	jq -e '.confirmed' "${result}" > /dev/null || return 3
}
