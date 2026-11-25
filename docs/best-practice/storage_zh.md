---
title: 存储服务
type: docs
description: ""
author: HUATUO Team
date: 2026-07-27
weight: 1
---

## 概述

本文说明如何将内核事件和 AutoTracing 结果写入 Elasticsearch 或 OpenSearch，并验证远端记录。示例使用指定版本的 Docker 镜像；协议、认证和索引配置应与目标服务一致。

事件按已启用的后端写入。本地文件由 `[Storage.LocalFile]` 控制，远端存储使用 `[Storage.Elasticsearch]`。两者独立配置，本地目录不是远端写入失败时的自动补偿队列。

## 选择后端

按现有环境选择下列一种后端。启动采集器前，确认存储服务可访问，并根据[节点配置](/docs/configuration/huatuo-bamai-configuration_zh.md)设置地址、认证和索引。需要接入 Grafana 时，见[数据源配置](/docs/best-practice/datasource_zh.md)。

## OpenSearch 2.x

### 1. 部署 OpenSearch

```bash
docker pull opensearchproject/opensearch:2.6.0
docker run -d --name opensearch --network host \
    -e "discovery.type=single-node" \
    opensearchproject/opensearch:2.6.0
```

### 2. 验证服务状态

```bash
curl -k -u admin:admin https://localhost:9200
```

返回示例：

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

若验证失败，可通过以下命令查看容器日志：

```bash
docker logs opensearch
```

### 3. 配置 huatuo-bamai

在 `huatuo-bamai.conf` 中添加以下配置。本例使用 OpenSearch 2.6.0 镜像，示例用户名和密码均为 `admin`。存储配置的详细说明请参见[配置指南](/docs/configuration/huatuo-bamai-configuration_zh.md)。

```toml
[Storage.Elasticsearch]
    Address = "https://127.0.0.1:9200"
    Index = "huatuo_bamai"
    Username = "admin"
    Password = "admin"
```

### 4. 启动 huatuo-bamai

通过 `--config-dir` 指定配置文件所在目录：

```bash
./_output/bin/huatuo-bamai --region dev --config-dir .
```

在测试环境产生目标事件后，查询对应时间和节点的记录。本地文件存在只能确认本地写入，远端写入需通过以下查询验证。查询 OpenSearch 查询数据：

```bash
curl -k -u admin:admin \
    -X GET "https://localhost:9200/huatuo_bamai/_search?pretty" \
    -H "Content-Type: application/json" \
    -d '{"query": {"match_all": {}}}'
```

返回示例：

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

以下命令仅统计记录总数：

```bash
curl -k -u admin:admin -X GET "https://localhost:9200/huatuo_bamai/_count?pretty"
```

返回示例中，`count` 为匹配记录数：
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

### 1. 部署 Elasticsearch

```bash
docker pull docker.elastic.co/elasticsearch/elasticsearch:8.15.5
docker run -d --name elasticsearch --network host \
    -e "discovery.type=single-node" \
    -e "ES_JAVA_OPTS=-Xms1g -Xmx1g" \
    -e "ELASTIC_PASSWORD=123456" \
    docker.elastic.co/elasticsearch/elasticsearch:8.15.5
```

### 2. 验证服务状态

```bash
curl -k -u elastic:123456 https://localhost:9200
```

返回示例：

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

### 3. 配置 huatuo-bamai

在 `huatuo-bamai.conf` 中添加以下配置。Elasticsearch 容器镜像默认用户名为 `elastic`，密码通过环境变量 `ELASTIC_PASSWORD` 设置。存储配置的详细说明请参见[配置指南](/docs/configuration/huatuo-bamai-configuration_zh.md)。

```toml
[Storage.Elasticsearch]
    Address = "https://127.0.0.1:9200"
    Index = "huatuo_bamai"
    Username = "elastic"
    Password = "123456"
```

### 4. 启动 huatuo-bamai

通过 `--config-dir` 指定配置文件所在目录：

```bash
./_output/bin/huatuo-bamai --region dev --config-dir .
```

在测试环境产生目标事件后，查询对应时间和节点的记录。本地文件存在只能确认本地写入，远端写入需通过以下查询验证。查询 Elasticsearch 查询数据：

```bash
curl -k -u elastic:123456 \
    -X GET "https://localhost:9200/huatuo_bamai/_search?pretty" \
    -H "Content-Type: application/json" \
    -d '{"query": {"match_all": {}}}'
```

返回示例：

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

以下命令仅统计记录总数：

```bash
curl -k -u elastic:123456 -X GET "https://localhost:9200/huatuo_bamai/_count?pretty"
```

返回示例中，`count` 为匹配记录数：
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

本节示例使用 HTTP；地址、协议和认证信息需与实际服务配置一致。

### 1. 部署 Elasticsearch

```bash
docker pull docker.elastic.co/elasticsearch/elasticsearch:7.10.1
docker run -d --name elasticsearch --network host \
    -e "discovery.type=single-node" \
    -e "ES_JAVA_OPTS=-Xms1g -Xmx1g" \
    -e "ELASTIC_PASSWORD=123456" \
    docker.elastic.co/elasticsearch/elasticsearch:7.10.1
```

### 2. 验证服务状态

```bash
curl -k -u elastic:123456 http://localhost:9200
```

返回示例：

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

### 3. 配置 huatuo-bamai

```toml
[Storage.Elasticsearch]
    Address = "http://127.0.0.1:9200"
    Index = "huatuo_bamai"
    Username = "elastic"
    Password = "123456"
```

### 4. 启动 huatuo-bamai

通过 `--config-dir` 指定配置文件所在目录：

```bash
./_output/bin/huatuo-bamai --region dev --config-dir .
```

在测试环境产生目标事件后，查询对应时间和节点的记录。本地文件存在只能确认本地写入，远端写入需通过以下查询验证。查询 Elasticsearch 查询数据：

```bash
curl -k -u elastic:123456 \
    -X GET "http://localhost:9200/huatuo_bamai/_search?pretty" \
    -H "Content-Type: application/json" \
    -d '{"query": {"match_all": {}}}'

或者：
curl -k -u elastic:123456 \
    -X GET "http://localhost:9200/huatuo_bamai/_count?pretty"
```

## 验证与排查

- 先检查服务状态，再查询目标节点和时间范围的事件记录。
- 索引总数增加不一定来自本次采集；应核对 `hostname`、`tracer_name` 和时间字段。
- 未触发目标事件时可能没有记录。先确认采集条件，再检查存储日志。
- 本地文件和远端写入均可能失败；本地记录还受轮转与保留数量限制，不能保证永久保留。

批量写入、重试、查询与失败语义见[存储接口](/docs/development/storage_zh.md)。
