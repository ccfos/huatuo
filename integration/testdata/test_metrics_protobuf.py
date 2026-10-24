#!/usr/bin/env python3

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

# Decode delimited MetricFamily responses into JSON facts for Shell assertions.
import json
from pathlib import Path
import struct
import sys


def varint(data, offset):
    value = 0
    for shift in range(0, 70, 7):
        byte = data[offset]
        offset += 1
        value |= (byte & 127) << shift
        if byte < 128:
            return value, offset
    raise ValueError("protobuf varint exceeds 64 bits")


def fields(data):
    result = {}
    offset = 0
    while offset < len(data):
        tag, offset = varint(data, offset)
        number, wire = tag >> 3, tag & 7
        if number == 0:
            raise ValueError("protobuf field number is zero")
        if wire == 0:
            value, offset = varint(data, offset)
        elif wire in (1, 5):
            width = 8 if wire == 1 else 4
            value = data[offset:offset + width]
            if len(value) != width:
                raise ValueError("truncated fixed-width protobuf field")
            offset += width
        elif wire == 2:
            length, offset = varint(data, offset)
            value = data[offset:offset + length]
            if len(value) != length:
                raise ValueError("truncated protobuf message")
            offset += length
        else:
            raise ValueError("unsupported protobuf wire type: {}".format(wire))
        result.setdefault(number, []).append(value)
    return result


def text(message, number):
    return message.get(number, [b""])[-1].decode("utf-8")


def decode(data):
    offset = 0
    families = []
    while offset < len(data):
        length, offset = varint(data, offset)
        payload = data[offset:offset + length]
        if len(payload) != length:
            raise ValueError("truncated delimited MetricFamily")
        offset += length
        family = fields(payload)
        metrics = []
        for payload in family.get(4, []):
            metric = fields(payload)
            labels = []
            for payload in metric.get(1, []):
                label = fields(payload)
                labels.append({"name": text(label, 1), "value": text(label, 2)})
            gauges = []
            for payload in metric.get(2, []):
                gauge = fields(payload)
                gauges.extend(struct.unpack("<d", value)[0] for value in gauge.get(1, []))
            metrics.append({"labels": labels, "gauges": gauges})
        families.append({"name": text(family, 1), "type": family.get(3, [None])[-1], "metrics": metrics})
    return families


if __name__ == "__main__":
    print(json.dumps(decode(Path(sys.argv[1]).read_bytes())))
