#!/usr/bin/env bash
# Copyright 2026 The HuaTuo Authors.
# SPDX-License-Identifier: Apache-2.0

# Protocol conversion stays in the harness; assertions remain in Shell.
set -euo pipefail
profile_query_encode() {
	python3 - "$1" "$2" << 'PY'
import json
import sys

method, source = sys.argv[1:]
schemas = {
	"LabelNames": {"matchers": (1, "strings"), "start": (2, "integer"), "end": (3, "integer")},
	"LabelValues": {"name": (1, "string"), "matchers": (2, "strings"), "start": (3, "integer"), "end": (4, "integer")},
	"SelectMergeStacktraces": {"profileTypeID": (1, "string"), "labelSelector": (2, "string"), "start": (3, "integer"), "end": (4, "integer")},
}

def varint(value):
	value &= (1 << 64) - 1
	result = bytearray()
	while value > 127:
		result.append((value & 127) | 128)
		value >>= 7
	result.append(value)
	return result

try:
	schema = schemas[method]
	result = bytearray()
	for key, value in json.loads(source).items():
		number, kind = schema[key]
		if kind == "integer":
			number_value = int(value)
			if not -(1 << 63) <= number_value < (1 << 63):
				raise ValueError("integer outside int64 range")
			result += varint(number << 3) + varint(number_value)
		else:
			for item in value if kind == "strings" else [value]:
				encoded = item.encode("utf-8")
				result += varint(number << 3 | 2) + varint(len(encoded)) + encoded
	sys.stdout.buffer.write(result)
except (KeyError, TypeError, ValueError, AttributeError) as error:
	sys.exit(f"encode {method} request: {error}")
PY
}

profile_query_decode() {
	python3 - "$1" "$2" << 'PY'
import json
import sys
from pathlib import Path

def varint(data, offset):
	result = 0
	for shift in range(0, 70, 7):
		if offset >= len(data):
			raise ValueError("truncated varint")
		byte = data[offset]
		offset += 1
		if shift == 63 and byte > 1:
			raise ValueError("varint exceeds uint64")
		result |= (byte & 127) << shift
		if byte < 128:
			return result, offset
	raise ValueError("unterminated varint")

def fields(data):
	offset = 0
	while offset < len(data):
		tag, offset = varint(data, offset)
		number, wire = tag >> 3, tag & 7
		if number == 0:
			raise ValueError("field number zero")
		if wire == 0:
			value, offset = varint(data, offset)
		elif wire in (1, 2, 5):
			if wire == 2:
				size, offset = varint(data, offset)
			else:
				size = 8 if wire == 1 else 4
			end = offset + size
			if end > len(data):
				raise ValueError("truncated field")
			value, offset = data[offset:end], end
		else:
			raise ValueError(f"unsupported wire type {wire}")
		yield number, wire, value

def signed(value):
	return value if value < 1 << 63 else value - (1 << 64)

method, path = sys.argv[1:]
try:
	data = Path(path).read_bytes()
	if method in ("LabelNames", "LabelValues"):
		result = {"names": []}
		for number, wire, value in fields(data):
			if number == 1:
				if wire != 2:
					raise ValueError("label name must be a string")
				result["names"].append(value.decode("utf-8"))
	elif method == "SelectMergeStacktraces":
		result = {}
		for number, wire, value in fields(data):
			if number == 1:
				if wire != 2:
					raise ValueError("flamegraph must be a message")
				graph = result.setdefault("flamegraph", {"names": [], "levels": [], "total": "0"})
				for field, kind, content in fields(value):
					if field == 1:
						if kind != 2:
							raise ValueError("flamegraph name must be a string")
						graph["names"].append(content.decode("utf-8"))
					elif field == 2:
						if kind != 2:
							raise ValueError("flamegraph level must be a message")
						values = []
						for entry, encoding, coordinates in fields(content):
							if entry != 1:
								continue
							if encoding == 0:
								values.append(signed(coordinates))
							elif encoding == 2:
								offset = 0
								while offset < len(coordinates):
									coordinate, offset = varint(coordinates, offset)
									values.append(signed(coordinate))
							else:
								raise ValueError("flamegraph coordinate must be an integer")
						graph["levels"].append({"values": values})
					elif field == 3:
						if kind != 0:
							raise ValueError("flamegraph total must be an integer")
						graph["total"] = str(signed(content))
	else:
		raise ValueError(f"unsupported method {method}")
	print(json.dumps(result))
except (OSError, TypeError, ValueError) as error:
	sys.exit(f"decode {method} response: {error}")
PY
}

assert_profile_query() {
	local base=$1 token=$2 method=$3 body=$4 result=$5 expected=${6:-200}
	local status content_type curl_status=0 codec_status=0
	local request_file="${result%.json}.request.proto" response_file="${result%.json}.response.proto"
	local error_file="${result%.json}.error"
	printf '%s\n' "$body" > "${result%.json}.request.json"
	profile_query_encode "$method" "$body" > "$request_file" 2> "$error_file" || codec_status=$?
	report_http_response "$method request encoding" "${result%.json}.request.json" "$error_file"
	assert_eq "$codec_status" 0 "$method request encoding" || fatal "$method request encoding failed"
	status=$(curl -sS "${CURL_TIMEOUT[@]}" -H "Authorization: Bearer ${token}" \
		-H 'Content-Type: application/proto' --data-binary "@${request_file}" \
		-o "$response_file" -w '%{http_code} %{content_type}' \
		"${base}/v1/profiling/flamegraph/querier.v1.QuerierService/${method}" 2> "$error_file") || curl_status=$?
	read -r status content_type <<< "$status"
	log_info "$method HTTP status=${status}, content-type=${content_type}, curl=${curl_status}"
	if [[ $curl_status -ne 0 || $status != 200 ]]; then
		report_http_response "$method response" "$response_file" "$error_file"
	fi
	assert_eq "$curl_status" 0 "$method transport" || fatal "$method transport failed"
	assert_eq "$status" "$expected" "$method HTTP status" || fatal "$method returned HTTP $status"
	if [[ $status != 200 ]]; then
		cp "$response_file" "$result"
		return
	fi
	assert_eq "${content_type%%;*}" application/proto "$method response content type" \
		|| fatal "$method returned an unexpected content type: $content_type"

	profile_query_decode "$method" "$response_file" > "$result" 2> "$error_file" || codec_status=$?
	report_http_response "$method response" "$result" "$error_file"
	assert_eq "$codec_status" 0 "$method response decoding" || fatal "$method response decoding failed"
	if [[ $method == SelectMergeStacktraces ]]; then
		jq -e '.flamegraph | type == "object" and (.names | length > 0) and (.levels | length > 0)' "$result" > /dev/null \
			|| fatal "$method returned no flamegraph names or levels; inspect $result"
	fi
}
