---
title: Storage Services
type: docs
description: ""
author: HUATUO Team
date: 2026-07-27
weight: 1
---

## Overview

This guide shows how to write kernel events and AutoTracing results to Elasticsearch or OpenSearch and verify remote records. Examples use specific Docker image versions; protocol, authentication, and index settings must match the target service.

Events are written to enabled backends. `[Storage.LocalFile]` controls local files; `[Storage.Elasticsearch]` controls remote storage. They are configured independently. The local directory is not an automatic fallback queue for failed remote writes.

## Choose a Backend

Choose one backend for your environment. Before starting the collector, confirm the storage service is accessible and set its address, authentication, and index using [Node Configuration](/docs/configuration/huatuo-bamai-configuration_en.md). For Grafana integration, see [Data Sources](/docs/best-practice/datasource_en.md).

## OpenSearch 2.x

### 1. Deploy OpenSearch

```bash
docker pull opensearchproject/opensearch:2.6.0
docker run -d --name opensearch --network host \
    -e "discovery.type=single-node" \
    opensearchproject/opensearch:2.6.0
```

### 2. Verify Service Status

```bash
curl -k -u admin:admin https://localhost:9200
```

Example response:

```json
{
  "name" : "22ca72df78c0",
  "cluster_name" : "docker-cluster",
  "cluster_uuid" : "yxb3foceQVKzXXO6bHpPHQ",
  "version" : {
    "distribution" : "opensearch",
    "number" : "2.6.0",
    "build_type" : "tar",
    "build_hash" : "7203a5af21a8a009aece1474446b437a3c674db6",
    "build_date" : "2023-02-24T18:57:04.388618985Z",
    "build_snapshot" : false,
    "lucene_version" : "9.5.0",
    "minimum_wire_compatibility_version" : "7.10.0",
    "minimum_index_compatibility_version" : "7.0.0"
  },
  "tagline" : "The OpenSearch Project: https://opensearch.org/"
}
```

If verification fails, inspect the container logs:

```bash
docker logs opensearch
```

### 3. Configure huatuo-bamai

Add the following settings to `huatuo-bamai.conf`. This example uses the OpenSearch 2.6.0 image with `admin` as both username and password. See the [configuration guide](/docs/configuration/huatuo-bamai-configuration_en.md) for storage settings.

```toml
[Storage.Elasticsearch]
    Address = "https://127.0.0.1:9200"
    Index = "huatuo_bamai"
    Username = "admin"
    Password = "admin"
```

### 4. Start huatuo-bamai

Use `--config-dir` to specify the directory containing the configuration:

```bash
./_output/bin/huatuo-bamai --region dev --config-dir .
```

Generate the target event in a test environment, then query records for the corresponding node and time. A local file confirms only a local write; verify remote writes by querying OpenSearch:

```bash
curl -k -u admin:admin \
    -X GET "https://localhost:9200/huatuo_bamai/_search?pretty" \
    -H "Content-Type: application/json" \
    -d '{"query": {"match_all": {}}}'
```

Example response:

```json
{
    "_index" : "huatuo_bamai",
    "_id" : "yjPG_50Bu_OF-hukxKR7",
    "_score" : 1.0,
    "_source" : {
      "hostname" : "hostname",
      "region" : "dev",
      "uploaded_timestamp" : "2026-05-07T00:11:49.753166222Z",
      "tracer_name" : "net_rx_latency",
      "observed_timestamp" : "2026-05-07T00:11:49.753166222Z",
      "tracer_type" : "event",
      "tracer_data" : {
        "comm" : "<nil>",
        "pid" : 0,
        "latency_stage" : "RX_STAGE_NETIF",
        "latency_ms" : 1776078133565,
        "tcp_saddr" : "127.0.0.1",
        "tcp_daddr" : "127.0.0.1",
        "tcp_sport" : 37736,
        "tcp_dport" : 9200,
        "tcp_seq" : 1080592402,
        "tcp_ack_seq" : 2465063876,
        "packet_len_bytes" : 781
      }
    }
}
```

The following command counts records only:

```bash
curl -k -u admin:admin -X GET "https://localhost:9200/huatuo_bamai/_count?pretty"
```

In the response, `count` is the number of matching records:
```json
{
  "count" : 2680,
  "_shards" : {
    "total" : 1,
    "successful" : 1,
    "skipped" : 0,
    "failed" : 0
  }
}
```

## Elasticsearch 8.x

### 1. Deploy Elasticsearch

```bash
docker pull docker.elastic.co/elasticsearch/elasticsearch:8.15.5
docker run -d --name elasticsearch --network host \
    -e "discovery.type=single-node" \
    -e "ES_JAVA_OPTS=-Xms1g -Xmx1g" \
    -e "ELASTIC_PASSWORD=123456" \
    docker.elastic.co/elasticsearch/elasticsearch:8.15.5
```

### 2. Verify Service Status

```bash
curl -k -u elastic:123456 https://localhost:9200
```

Example response:

```json
{
  "name" : "ab0b562f8dbd",
  "cluster_name" : "docker-cluster",
  "cluster_uuid" : "aVfOVgJTQXuhZ3HGotK3ww",
  "version" : {
    "number" : "8.15.5",
    "build_flavor" : "default",
    "build_type" : "docker",
    "build_hash" : "b10896bcfe167cce44a84ba2771d101fb596d40d",
    "build_date" : "2024-11-21T22:06:13.985834967Z",
    "build_snapshot" : false,
    "lucene_version" : "9.11.1",
    "minimum_wire_compatibility_version" : "7.17.0",
    "minimum_index_compatibility_version" : "7.0.0"
  },
  "tagline" : "You Know, for Search"
}
```

### 3. Configure huatuo-bamai

Add the following settings to `huatuo-bamai.conf`. The Elasticsearch image uses the default username `elastic`; set the password through `ELASTIC_PASSWORD`. See the [configuration guide](/docs/configuration/huatuo-bamai-configuration_en.md) for storage settings.

```toml
[Storage.Elasticsearch]
    Address = "https://127.0.0.1:9200"
    Index = "huatuo_bamai"
    Username = "elastic"
    Password = "123456"
```

### 4. Start huatuo-bamai

Use `--config-dir` to specify the directory containing the configuration:

```bash
./_output/bin/huatuo-bamai --region dev --config-dir .
```

Generate the target event in a test environment, then query records for the corresponding node and time. A local file confirms only a local write; verify remote writes by querying Elasticsearch:

```bash
curl -k -u elastic:123456 \
    -X GET "https://localhost:9200/huatuo_bamai/_search?pretty" \
    -H "Content-Type: application/json" \
    -d '{"query": {"match_all": {}}}'
```

Example response:

```json
{
    "_index" : "huatuo_bamai",
    "_id" : "WtNZAJ4BQ8x-thPHEY1i",
    "_score" : 1.0,
    "_source" : {
      "hostname" : "hostname",
      "region" : "dev",
      "uploaded_timestamp" : "2026-05-07T02:51:37.696263325Z",
      "tracer_name" : "net_rx_latency",
      "observed_timestamp" : "2026-05-07T02:51:37.696263325Z",
      "tracer_type" : "event",
      "tracer_data" : {
        "comm" : "<nil>",
        "pid" : 0,
        "latency_stage" : "RX_STAGE_NETIF",
        "latency_ms" : 1776078133565,
        "tcp_saddr" : "127.0.0.1",
        "tcp_daddr" : "127.0.0.1",
        "tcp_sport" : 2379,
        "tcp_dport" : 36706,
        "tcp_seq" : 950542706,
        "tcp_ack_seq" : 1960972383,
        "packet_len_bytes" : 91
      }
    }
}
```

The following command counts records only:

```bash
curl -k -u elastic:123456 -X GET "https://localhost:9200/huatuo_bamai/_count?pretty"
```

In the response, `count` is the number of matching records:
```json
{
  "count" : 2680,
  "_shards" : {
    "total" : 1,
    "successful" : 1,
    "skipped" : 0,
    "failed" : 0
  }
}
```

## Elasticsearch 7.x

This section uses HTTP. Match the address, protocol, and credentials to the actual service configuration.

### 1. Deploy Elasticsearch

```bash
docker pull docker.elastic.co/elasticsearch/elasticsearch:7.10.1
docker run -d --name elasticsearch --network host \
    -e "discovery.type=single-node" \
    -e "ES_JAVA_OPTS=-Xms1g -Xmx1g" \
    -e "ELASTIC_PASSWORD=123456" \
    docker.elastic.co/elasticsearch/elasticsearch:7.10.1
```

### 2. Verify Service Status

```bash
curl -k -u elastic:123456 http://localhost:9200
```

Example response:

```json
{
  "name" : "d88c9e8df48b",
  "cluster_name" : "docker-cluster",
  "cluster_uuid" : "_ZZefWx4SniAc255t_lIVg",
  "version" : {
    "number" : "7.10.1",
    "build_flavor" : "default",
    "build_type" : "docker",
    "build_hash" : "1c34507e66d7db1211f66f3513706fdf548736aa",
    "build_date" : "2020-12-05T01:00:33.671820Z",
    "build_snapshot" : false,
    "lucene_version" : "8.7.0",
    "minimum_wire_compatibility_version" : "6.8.0",
    "minimum_index_compatibility_version" : "6.0.0-beta1"
  },
  "tagline" : "You Know, for Search"
}
```

### 3. Configure huatuo-bamai

```toml
[Storage.Elasticsearch]
    Address = "http://127.0.0.1:9200"
    Index = "huatuo_bamai"
    Username = "elastic"
    Password = "123456"
```

### 4. Start huatuo-bamai

Use `--config-dir` to specify the directory containing the configuration:

```bash
./_output/bin/huatuo-bamai --region dev --config-dir .
```

Generate the target event in a test environment, then query records for the corresponding node and time. A local file confirms only a local write; verify remote writes by querying Elasticsearch:

```bash
curl -k -u elastic:123456 \
    -X GET "http://localhost:9200/huatuo_bamai/_search?pretty" \
    -H "Content-Type: application/json" \
    -d '{"query": {"match_all": {}}}'

Or:
curl -k -u elastic:123456 \
    -X GET "http://localhost:9200/huatuo_bamai/_count?pretty"
```

## Verification and Troubleshooting

- Check service health first, then query event records for the target node and time range.
- A higher index count does not necessarily come from the current collection. Check `hostname`, `tracer_name`, and time fields.
- No records may exist until the target event occurs. Check collection conditions before storage logs.
- Both local and remote writes can fail. Local records are also subject to rotation and retention limits and are not guaranteed to persist indefinitely.

See [Storage Interfaces](/docs/development/storage_en.md) for bulk writes, retries, queries, and failure semantics.
