---
title: Kubernetes
type: docs
description:
author: HUATUO Team
date: 2026-01-11
weight: 2
---

This document describes how to deploy the Huatuo collector to a Kubernetes cluster using a DaemonSet.

## 1. Kubernetes Manifest Deployment

### 1.1 Download the configuration file

```bash
curl -L -o huatuo-bamai.conf https://github.com/ccfos/huatuo/raw/main/huatuo-bamai.conf
```

### 1.2 Modify the configuration file

Modify the configuration file for the deployment environment. For example, configure the storage backend and the method used to obtain Pod information. See the [Configuration Guide](/docs/configuration/huatuo-bamai-configuration_en.md) for details.

### 1.3 Create the ConfigMap

```bash
kubectl create configmap huatuo-bamai-config \
  --namespace default \
  --from-file=./huatuo-bamai.conf \
  --dry-run=client -o yaml |
kubectl apply -f -
```

### 1.4 Deploy the collector

Download the DaemonSet manifest:

```bash
curl -L -o huatuo-daemonset.yaml \
  https://raw.githubusercontent.com/ccfos/huatuo/main/build/huatuo-daemonset.minimal.yaml
```

Before deploying to production, set the `huatuo` container resources to this
initial baseline:

```yaml
resources:
  limits:
    cpu: "2"
    memory: 4Gi
  requests:
    cpu: "1"
    memory: 1Gi
```

Apply the modified manifest:

```bash
kubectl apply -f ./huatuo-daemonset.yaml
```

`requests` provide scheduling guarantees, while `limits` isolate abnormal jobs
from the node. The `4 GiB` container limit leaves runtime headroom above the
default `2048 MiB` process limit, reducing OOM risk.

These values are an initial baseline. Adjust them based on node capacity,
collection jobs, and observed resource peaks. Container limits must not be lower
than the `[Runtime]` process limits in `huatuo-bamai.conf`.

### 1.5 Verify the deployment

```bash
kubectl rollout status daemonset/huatuo \
  --namespace default \
  --timeout=10m

kubectl get pods \
  --namespace default \
  --selector app=huatuo \
  --output wide

kubectl get daemonset huatuo \
  --namespace default \
  --output jsonpath='{.spec.template.spec.containers[?(@.name=="huatuo")].resources}'
```

After updating `huatuo-bamai.conf`, rerun section 1.3 to update the ConfigMap, then manually restart the DaemonSet:

```bash
kubectl rollout restart daemonset/huatuo --namespace default
```

## 2. Helm Deployment

The Helm Chart is located at `build/charts/`.

### 2.1 Check the deployment environment

Helm and kubectl must be installed on the management host, which must be able to access the target Kubernetes cluster.

```bash
(command -v helm || curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash) && helm version

TARGET_CONTEXT="$(kubectl config get-contexts -o name | sed -n '1p')"
kubectl --context "${TARGET_CONTEXT}" get nodes
```

Verify that the target Nodes are `Ready`.

### 2.2 Prepare the configuration file

Download and modify `huatuo-bamai.conf` as described in sections 1.1 and 1.2.

### 2.3 Configure deployment values

Create `values-production.yaml`:

```yaml
image:
  repository: <registry-accessible-to-all-nodes>/huatuo-bamai
  tag: "<release-version>"
  pullPolicy: IfNotPresent

resources:
  limits:
    cpu: "2"
    memory: 4Gi
  requests:
    cpu: "1"
    memory: 1Gi

nodeSelector:
  kubernetes.io/os: linux

tolerations:
  - operator: Exists

hostPaths:
  proc: /proc
  sys: /sys
  run: /run
  var: /var
  etc: /etc
  data: /var/log/huatuo/huatuo-local
```

### 2.4 Validate the Helm Chart

```bash
helm lint ./build/charts \
  -f ./values-production.yaml \
  --set-file config.content=./huatuo-bamai.conf

helm template huatuo ./build/charts \
  --namespace huatuo \
  -f ./values-production.yaml \
  --set-file config.content=./huatuo-bamai.conf \
  >/dev/null
```

### 2.5 Deploy the collector

```bash
helm upgrade --install huatuo ./build/charts \
  --kube-context "${TARGET_CONTEXT}" \
  --namespace huatuo \
  --create-namespace \
  -f ./values-production.yaml \
  --set-file config.content=./huatuo-bamai.conf \
  --atomic \
  --timeout 10m
```

### 2.6 Verify the deployment

```bash
helm status huatuo \
  --kube-context "${TARGET_CONTEXT}" \
  --namespace huatuo

kubectl --context "${TARGET_CONTEXT}" \
  --namespace huatuo \
  get daemonset,configmap,pod --output wide

kubectl --context "${TARGET_CONTEXT}" \
  --namespace huatuo \
  rollout status daemonset/huatuo \
  --timeout=10m
```

Inspect the collector logs:

```bash
kubectl --context "${TARGET_CONTEXT}" \
  --namespace huatuo \
  logs \
  --selector app.kubernetes.io/name=huatuo \
  --prefix \
  --tail=100
```

### 2.7 Upgrade and roll back

After changing the image version or `huatuo-bamai.conf`, rerun the command in section 2.5.

List the release history and roll back to a selected revision:

```bash
helm history huatuo \
  --kube-context "${TARGET_CONTEXT}" \
  --namespace huatuo

helm rollback huatuo <revision> \
  --kube-context "${TARGET_CONTEXT}" \
  --namespace huatuo \
  --wait \
  --timeout 10m
```
