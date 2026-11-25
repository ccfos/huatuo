---
title: Cluster Deployment
type: docs
description:
author: HUATUO Team, hao022
date: 2026-01-11
weight: 2
---

Deploy the collector on Kubernetes nodes as a DaemonSet, using either manifests or Helm. The administration machine needs kubectl and access to the target cluster.

## 1. Deploy with Manifests

### 1.1 Download the Configuration

```bash
curl -L -o huatuo-bamai.conf https://github.com/ccfos/huatuo/raw/main/huatuo-bamai.conf
```

### 1.2 Edit the Configuration

Adjust the configuration for your environment, including the storage backend and Pod information source. See the [configuration guide](/docs/configuration/huatuo-bamai-configuration_en.md).

**Optional: ServiceAccount token authentication (when host kubelet client certificates are not mounted)**

By default, `huatuo-bamai.conf` sets `KubeletClientCertPath` for client-certificate authentication. To opt into Pod ServiceAccount token authentication instead (for example on managed Kubernetes or K3s clusters with `serverTLSBootstrap: true`):

1. Update `[Pod]` in `huatuo-bamai.conf`:
   ```toml
   [Pod]
       KubeletReadOnlyPort = 0
       KubeletClientCertPath = ""
       # Optional: set KubeletCAPath if kubelet serving certificates are signed
       # by a CA different from /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
       # KubeletCAPath = "/etc/kubernetes/pki/ca.crt"
   ```
   `huatuo-bamai` then reads `/var/run/secrets/kubernetes.io/serviceaccount/token` on every request (supporting token rotation) and verifies the kubelet serving certificate against `KubeletCAPath` (defaulting to `/var/run/secrets/kubernetes.io/serviceaccount/ca.crt`). Both `build/huatuo-daemonset.minimal.yaml` and the Helm chart inject `KUBELET_HOST` from Downward API `status.hostIP` so TLS verification matches the node IP SAN. Before deploying, verify that the kubelet serving certificate on `<node-ip>:10250` includes the node IP in its SAN and chains to the configured CA (for example with `openssl s_client -connect <node-ip>:10250 -CAfile <ca-path> </dev/null`).
2. Grant RBAC permissions for `/pods` and `/configz`:
   - **Recommended (Kubernetes 1.33+ with `KubeletFineGrainedAuthz`)**: grant least-privilege `get` on `nodes/pods` and `nodes/configz`.
     - **Helm**: pass `--set serviceAccount.create=true --set rbac.create=true` in section 2.5.
     - **Manifest**: create a `ServiceAccount`, a `ClusterRole` granting `get` on `nodes/pods` and `nodes/configz`, and a `ClusterRoleBinding`, then set `spec.template.spec.serviceAccountName` on the DaemonSet.
   - **Legacy clusters (without `KubeletFineGrainedAuthz`)**: older Kubernetes versions do not authorize `nodes/pods` and `nodes/configz` separately and require `get` on `nodes/proxy` instead. With Helm, pass `--set rbac.allowLegacyNodesProxy=true` alongside `serviceAccount.create=true` and `rbac.create=true`. **Security note**: `nodes/proxy` `get` is not strictly read-only—it also permits kubelet WebSocket `GET` operations (`exec`, `attach`, `portForward`) against containers on nodes; restrict access to the DaemonSet Pod and upgrade to fine-grained kubelet authorization when possible.

### 1.3 Create a ConfigMap

```bash
kubectl create configmap huatuo-bamai-config \
  --namespace default \
  --from-file=./huatuo-bamai.conf \
  --dry-run=client -o yaml |
kubectl apply -f -
```

### 1.4 Deploy the Collector

Download the DaemonSet manifest:

```bash
curl -L -o huatuo-daemonset.yaml \
  https://raw.githubusercontent.com/ccfos/huatuo/main/build/huatuo-daemonset.minimal.yaml
```

Before deploying to production, set `resources` for the `huatuo` container to the following baseline:

```yaml
resources:
  limits:
    cpu: "2"
    memory: 2Gi
  requests:
    cpu: "2"
    memory: 2Gi
```

Apply the updated manifest:

```bash
kubectl apply -f ./huatuo-daemonset.yaml
```

`requests` reserve resources for scheduling. The kubelet writes `limits` to the Pod cgroup to enforce Huatuo's CPU and memory limits. Huatuo does not create its own cgroup by default, so its processes remain in the `kubepods` hierarchy and can be reclaimed when containerd restarts or the Pod is deleted.

These values are a starting point. Equal requests and limits give the Pod Guaranteed QoS. `[Runtime]` takes effect only when `--enable-cgroup` is explicitly passed; do not pass this flag in Kubernetes deployments.

### 1.5 Verify the Deployment

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

After updating `huatuo-bamai.conf`, repeat section 1.3 to update the ConfigMap, then trigger a rolling update manually:

```bash
kubectl rollout restart daemonset/huatuo --namespace default
```

## 2. Deploy with Helm

The Helm chart is in `build/charts/`.

### 2.1 Check the Environment

The administration machine needs Helm, kubectl, and access to the target Kubernetes cluster.

```bash
(command -v helm || curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash) && helm version

TARGET_CONTEXT="$(kubectl config get-contexts -o name | sed -n '1p')"
kubectl --context "${TARGET_CONTEXT}" get nodes
```

Confirm that the target nodes are `Ready`.

### 2.2 Prepare the Configuration

Follow sections 1.1 and 1.2 to download and edit `huatuo-bamai.conf`.

### 2.3 Set Deployment Parameters

Create `values-production.yaml`:

```yaml
image:
  repository: <registry-accessible-to-all-nodes>/huatuo-bamai
  tag: "<pinned-release-version>"
  pullPolicy: IfNotPresent

resources:
  limits:
    cpu: "2"
    memory: 2Gi
  requests:
    cpu: "2"
    memory: 2Gi

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

### 2.5 Deploy the Collector

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

### 2.6 Verify the Deployment

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

### 2.7 Update and Roll Back

After changing the image version or `huatuo-bamai.conf`, rerun the command in section 2.5.

View the release history and roll back to a specific revision:

```bash
helm history huatuo \
  --kube-context "${TARGET_CONTEXT}" \
  --namespace huatuo

helm rollback huatuo <Revision> \
  --kube-context "${TARGET_CONTEXT}" \
  --namespace huatuo \
  --wait \
  --timeout 10m
```
