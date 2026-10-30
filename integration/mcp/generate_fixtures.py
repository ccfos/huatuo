# Copyright 2026 The HuaTuo Authors.
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#     http://www.apache.org/licenses/LICENSE-2.0
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Generate MCP event fixtures and compatibility cases; Shell owns storage writes."""

import json
from pathlib import Path
import sys


PREFIX = "huatuo_apiserver_mcp_compat_"


def escomparable():
    fixture = json.loads(Path(__file__).with_name("escomparable_fixtures.json").read_text())
    indices, lines = fixture["indices"], []
    for document in fixture["documents"]:
        if document["index"] not in indices:
            raise ValueError(f"fixture targets an undeclared index: {document}")
        source = {**fixture["source_defaults"], **document["source"]}
        if "profile" in document:
            template = fixture["profiles"][document["profile"]]
            source["tracer_data"] = {"flamedata": {
                "profile_type": document.get("profile_type", template["profile_type"]),
                "profile": {**template["profile"], **document.get("profile_overrides", {})},
            }}
        lines.extend((json.dumps({"index": {"_index": document["index"], "_id": document["id"]}}),
                      json.dumps(source)))
    return indices, lines


def compatibility():
    indices, lines = {}, []
    cases = {"alias-test": ("keyword", "multifield"), "aggregate-test": ("keyword",),
             "plain-test": ("keyword", "text"),
             "source-test": ("unindexed",), "overflow-test": ("unindexed",),
             "millis-test": ("millis",), "payload-test": ("payload",),
             "mixed-payload-test": ("keyword", "payload"),
             "scoped-payload-test": ("keyword", "payload"),
             "payload-overflow-test": ("payload",)}
    for region, modes in cases.items():
        for mode in modes:
            index = PREFIX + region + "_" + mode
            alias = PREFIX + region
            mapping = {"dynamic": False} if mode == "unindexed" else {"properties": {}}
            if mode != "unindexed":
                for field in ("region", "hostname", "container_hostname",
                              "container_host_namespace", "tracer_name"):
                    definition = {"type": "text" if mode in ("multifield", "text") else "keyword"}
                    if mode == "multifield":
                        definition["fields"] = {"keyword": {"type": "keyword"}}
                    mapping["properties"][field] = definition
                mapping["properties"]["uploaded_time"] = {"type": "date"}
                mapping["properties"]["tenant"] = {"type": "keyword"}
                if mode == "millis":
                    mapping["properties"]["uploaded_timestamp"] = {"type": "date"}
                if mode == "payload":
                    mapping["properties"]["tracer_data"] = {"type": "object", "enabled": False}
                if region == "scoped-payload-test" and mode == "keyword":
                    mapping["properties"]["tracer_data"] = {"properties": {
                        "flamedata": {"properties": {"label": {"type": "keyword"}}},
                    }}
            alias_definition = {"filter": {"term": {"tenant": "a"}}} if region in ("alias-test", "aggregate-test") else {}
            indices[index] = {
                "settings": {"number_of_shards": 1, "number_of_replicas": 0},
                "mappings": mapping, "aliases": {alias: alias_definition},
            }
            overflow = region in ("overflow-test", "payload-overflow-test")
            ordinary_bulk = region == "scoped-payload-test" and mode == "keyword"
            count = 501 if overflow or ordinary_bulk else 2
            if region == "aggregate-test":
                count = 4
            for number in range(count):
                source = {
                    "region": region, "hostname": "node-a", "container_hostname": "pod-a",
                    "container_host_namespace": "ns-a", "tracer_name": "cpu-idle",
                    "tenant": "a", "uploaded_time": "2026-01-15T10:10:00Z",
                }
                if region in ("alias-test", "aggregate-test") and number == 1:
                    source["tenant"], source["tracer_name"] = "b", "hidden-tracer"
                if region == "aggregate-test" and number == 2:
                    source["hostname"] = "node-b"
                if region == "aggregate-test" and number == 3:
                    source["tracer_name"] = "waitrate"
                if region == "plain-test" and number == 1:
                    source["hostname"] = "node a"
                if region == "source-test" or mode in ("millis", "payload") or region == "mixed-payload-test":
                    source["tracer_data"] = {"flamedata": [
                        {"level": 0, "label": "root", "value": 100, "self": 0},
                        {"level": 1, "label": "leaf", "value": 100, "self": 100},
                    ]}
                if region == "source-test":
                    source["uploaded_time"] = "2026-01-15 10:10:00 +0000"
                if mode == "millis":
                    del source["uploaded_time"]
                    source["uploaded_timestamp"] = 1768471800123 if number == 0 else "1768471800123"
                lines.extend((json.dumps({"index": {"_index": index, "_id": str(number)}}),
                              json.dumps(source)))
    return indices, lines


def prepare(workspace):
    for name, generate in (("escomparable", escomparable), ("compatibility", compatibility)):
        indices, lines = generate()
        Path(workspace, f"{name}-indices.json").write_text(json.dumps(indices))
        Path(workspace, f"{name}-bulk.ndjson").write_text("\n".join(lines) + "\n")


if __name__ == "__main__":
    prepare(*sys.argv[1:])
