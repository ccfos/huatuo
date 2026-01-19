---
title: K8s Daemonset 部署
type: docs
description: 
author: HUATUO Team
date: 2026-01-11
weight: 2
---
通过 K8s daemonset 方式在云原生集群部署。

### 1. 获取配置文件
```bash
curl -L -o huatuo-bamai.conf https://github.com/ccfos/huatuo/raw/main/huatuo-bamai.conf
```

根据实际环境修改配置，如kubelet 和 elasticsrearch 的相关配置。

### 2. 创建 configmap
```bash
kubectl create configmap huatuo-bamai-config --from-file=./huatuo-bamai.conf
```

### 3. 部署采集器
```bash
kubectl apply -f https://github.com/ccfos/huatuo/blob/main/build/huatuo-daemonset.minimal.yaml
```

huatuo-daemonset.minimal.yaml：
```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: huatuo
  namespace: default
  labels:
    app: huatuo
spec:
  selector:
    matchLabels:
      app: huatuo
  template:
    metadata:
      labels:
        app: huatuo
    spec:
      containers:
      - name: huatuo
        image: docker.io/huatuo/huatuo-bamai:latest
        resources:
          limits:
            cpu: '1'
            memory: 2Gi
          requests:
            cpu: 500m
            memory: 512Mi
        securityContext:
          privileged: true
        volumeMounts:
        - name: proc
          mountPath: /proc
        - name: sys
          mountPath: /sys
        - name: run
          mountPath: /run
        - name: var
          mountPath: /var
        - name: etc
          mountPath: /etc
        - name: huatuo-local
          mountPath: /home/huatuo-bamai/huatuo-local
        - name: huatuo-bamai-config-volume
          mountPath: /home/huatuo-bamai/conf/huatuo-bamai.conf
          subPath: huatuo-bamai.conf
      volumes:
      - name: proc
        hostPath:
          path: /proc
      - name: sys
        hostPath:
          path: /sys
      - name: run
        hostPath:
          path: /run
      - name: var
        hostPath:
          path: /var
      - name: etc
        hostPath:
          path: /etc
      - name: huatuo-local
        hostPath:
          path: /var/log/huatuo/huatuo-local
          type: DirectoryOrCreate
      - name: huatuo-bamai-config-volume
        configMap:
          name: huatuo-bamai-config
      hostNetwork: true
      hostPID: true
```
