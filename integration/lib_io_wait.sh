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

# Keep public wait-counter assertions shared by host, container and recovery
# cases. Workload durations qualify the input; they are not per-wait references.

assert_io_wait_slowdown() {
	local evidence
	evidence=$(jq -s -e '
		def valid_receipt:
			(.requests | type == "number") and (.requests > 0) and
			(.requests == (.requests | floor)) and
			(.operation == "read" or .operation == "write") and
			(.seconds | type == "number") and (.seconds >= 0) and
			(.seconds | isinfinite | not) and (.seconds | isnan | not);
		if length != 2 or (all(.[]; valid_receipt) | not) then
			error("invalid IO workload receipt")
		elif .[0].requests != .[1].requests or .[0].operation != .[1].operation then
			error("baseline and controlled workloads differ")
		else {baseline_seconds: .[0].seconds, limited_seconds: .[1].seconds} end
	' "$1" "$2") || return 1
	printf '%s\n' "${evidence}"
	if ! jq -e '.limited_seconds >= ([0.5, .baseline_seconds * 2] | max)' \
		<<< "${evidence}" > /dev/null; then
		log_info "external timing did not confirm a controlled slowdown" >&2
		return 3
	fi
}

assert_io_wait_metrics() {
	local collector="" disk="" operation="" namespace="" positive_windows=0 container_groups=null
	local is_idle=false is_warmup=false has_no_samples=false requires_other=false document documents summary path
	while (($#)); do
		case $1 in
		--collector)
			collector=$2
			shift 2
			;;
		--disk)
			disk=$2
			shift 2
			;;
		--operation)
			operation=$2
			shift 2
			;;
		--namespace)
			namespace=$2
			shift 2
			;;
		--positive-windows)
			positive_windows=$2
			shift 2
			;;
		--container-groups)
			container_groups=$2
			shift 2
			;;
		--warmup)
			is_warmup=true
			shift
			;;
		--idle)
			is_idle=true
			shift
			;;
		--no-samples)
			has_no_samples=true
			shift
			;;
		--require-other)
			requires_other=true
			shift
			;;
		--*)
			log_error "unknown wait assertion option: $1"
			return 1
			;;
		*) break ;;
		esac
	done
	(($#)) || {
		log_error "wait assertion requires metric files"
		return 1
	}
	documents=$(for path in "$@"; do
		document=$(io_test_metrics_json "${path}") || return 1
		jq -c --arg source "${path}" '{source: $source, metrics: .}' <<< "${document}" || return 1
	done) || return 1
	summary=$(jq -s -e --arg collector "${collector}" --arg disk "${disk}" \
		--arg operation "${operation}" --arg namespace "${namespace}" \
		--argjson positive_windows "${positive_windows}" --argjson container_groups "${container_groups}" \
		--argjson is_idle "${is_idle}" --argjson is_warmup "${is_warmup}" --argjson has_no_samples "${has_no_samples}" \
		--argjson requires_other "${requires_other}" '
		def abs: if . < 0 then -. else . end;
		def assert_value($condition; $message):
			if $condition then . else error($message) end;
		("huatuo_bamai_" + $collector + "_") as $prefix |
		(if $collector == "blk_throtl" then "delayed_io_count" else "waitq_io_count" end) as $count |
		["host", "region", "disk", "operation"] as $common |
		["container_host", "container_hostnamespace", "container_name", "container_type", "container_level"] as $container |
		map(. as $document |
			[.metrics[] | select(.name | startswith($prefix)) |
				. + {suffix: (.name | ltrimstr($prefix))} |
				. + {is_container: (.suffix | startswith("container_")),
					field: (if .suffix == $count or .suffix == ("container_" + $count)
						then "count" else "average" end)} |
				assert_value(
					(.suffix == $count or .suffix == "average_wait_ms" or
					 .suffix == ("container_" + $count) or .suffix == "container_average_wait_ms") and
					.type == "gauge" and .type_count == 1; "unexpected wait metric or type: " + .name) |
				assert_value(
					(.labels | keys) == (($common + (if .is_container then $container else ["scope"] end)) | sort) and
					.label_count == (.labels | length);
					"unexpected wait labels: " + (.labels | tostring)) |
				assert_value(.labels.host == "huatuo-dev" and .labels.region == "dev";
					"incorrect wait host/region labels") |
				assert_value(.labels.operation == "read" or .labels.operation == "write";
					"invalid wait direction") |
				assert_value(.is_container or .labels.scope == "host" or .labels.scope == "other";
					"invalid wait scope") |
				assert_value(
					(.value | type == "number") and .value >= 0 and
					(.value | isinfinite | not) and (.value | isnan | not);
					"invalid wait value: " + (.raw_value | tostring)) |
				assert_value(.field != "count" or .value == (.value | floor);
					"noninteger wait count") |
				if .is_container and $namespace != "" and .labels.container_hostnamespace == $namespace then
					assert_value(
						(.labels.container_name == "reader-0" or
						 ($container_groups == 2 and .labels.container_name == "reader-1")) and
						.labels.container_host == ("huatuo-io-" + (.labels.container_name | ltrimstr("reader-"))) and
						.labels.container_type == "normal" and .labels.container_level == "besteffort";
						"incorrect known-container labels")
				else . end
			] as $rows |
			[.metrics[] | select(.name == "huatuo_bamai_scrape_collector_success" and
				.labels.collector == $collector) |
				assert_value(.label_count == (.labels | length); "duplicate collector status labels") | .value] as $health |
			[$rows[] | select(.labels.disk == $disk and .labels.operation == $operation)] as $selected |
			assert_value(all(($selected | group_by(.labels))[];
				(map(.field) | length) == (map(.field) | unique | length));
				"duplicate public wait series: " + $document.source) |
			assert_value($health == [0] or $health == [1];
				"missing or invalid collector status: " + $document.source) |
			if $has_no_samples then
				assert_value(all($selected[]; .value == 0);
					"discarded or idle waits reappeared: " + $document.source)
			else . end |
			# A zero public health value cannot confirm either activity or quietness.
			if $health == [0] then
				{healthy: 0, unconfirmed: 1, count: 0, other: 0, positive: 0, containers: []}
			else
				($selected | group_by(.labels) | map(
					assert_value(length == 2 and (map(.field) | sort) == ["average", "count"];
						"duplicate or incomplete wait pair: " + $document.source) |
					{labels: .[0].labels, count: (map(select(.field == "count"))[0].value),
					 average: (map(select(.field == "average"))[0].value)} |
					assert_value(.count != 0 or .average == 0;
						"zero wait count has a nonzero average: " + $document.source)
				)) as $groups |
				[$groups[] | select(.labels.scope == "host")] as $hosts |
				[$groups[] | select(.labels.scope == "other")] as $others |
				[$groups[] | select(.labels.scope == null)] as $containers |
				assert_value(($groups | length) == 0 or ($hosts | length) == 1;
					"wait output has missing or multiple Host series: " + $document.source) |
				($hosts[0] // {count: 0, average: 0}) as $host |
				($others[0] // {count: 0, average: 0}) as $other |
				($containers + [$other]) as $parts |
				assert_value(($others | length) <= 1 and $host.count == ([$parts[].count] | add // 0);
					"Host != Other + containers: " + $document.source) |
				(if $host.count > 0 then ([$parts[] | .average * .count] | add) / $host.count else 0 end) as $weighted |
				assert_value($host.count == 0 or (($host.average - $weighted | abs) <=
					([0.000000001, ($weighted | abs) * 0.000001] | max));
					"wait averages do not aggregate by count: " + $document.source) |
				{healthy: 1, unconfirmed: 0, count: $host.count, other: $other.count,
				 positive: (if $host.count > 0 and $host.average > 0 then 1 else 0 end),
				 containers: [$containers[] | select(.count > 0 and
					($namespace == "" or .labels.container_hostnamespace == $namespace)) | .labels]}
			end
		) |
		{host_count: ([.[].count] | add), other_count: ([.[].other] | add),
		 positive_windows: ([.[].positive] | add), container_groups: ([.[].containers[]] | unique | length),
		 unconfirmed_windows: ([.[].unconfirmed] | add), healthy_windows: ([.[].healthy] | add)} |
		assert_value(($is_idle | not) or .host_count == 0; "quiet intervals repeated old waits") |
		assert_value($is_warmup or .positive_windows >= $positive_windows; "confirmed sustained waits lack positive output windows") |
		assert_value($container_groups == null or .container_groups == $container_groups
			or ($is_warmup and .container_groups < $container_groups);
			"unexpected positive container groups") |
		assert_value($is_warmup or ($requires_other | not) or .other_count > 0; "confirmed unattributed waits lack Other output")
	' <<< "${documents}") || return 1
	jq 'del(.healthy_windows)' <<< "${summary}"
	if [[ ${is_warmup} == true ]] && ! jq -e --argjson windows "${positive_windows}" \
		--argjson groups "${container_groups}" --argjson other "${requires_other}" '
		.positive_windows >= $windows and ($groups == null or .container_groups == $groups)
		and (($other | not) or .other_count > 0)
	' <<< "${summary}" > /dev/null; then
		return 3
	fi
	if [[ ${is_idle} == true ]] && jq -e '.healthy_windows == 0' <<< "${summary}" > /dev/null; then
		return 3
	fi
}

assert_io_wait_host_positive() {
	jq -e '(.positive_windows > 0) and (.other_count > 0)' "$1" > /dev/null
}
