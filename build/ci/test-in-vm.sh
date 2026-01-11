#!/bin/sh
set -eux

MATRIX_ARCH=$1
MOUNT_DIR=/host

echo "======== Workspace ========"
pwd
ls
ls -lah ${MOUNT_DIR}/
ls -lah ${MOUNT_DIR}/_output/${MATRIX_ARCH}

echo "========  Test...  ========"
# 1️⃣ Prepare
# disable es and kubelet fetching pods in huatuo-bamai.conf
sed -i -e 's/# Address.*/Address=""/g' \
  -e '$a\    KubeletReadOnlyPort=0' \
  -e '$a\    KubeletAuthorizedPort=0' \
${MOUNT_DIR}/_output/${MATRIX_ARCH}/conf/huatuo-bamai.conf
# add [netdev_rdma_link] to Blacklist (not supported in kind)
sed -i 's/^BlackList = \[\(.*\)\]/BlackList = ["netdev_rdma_link", \1]/' \
${MOUNT_DIR}/_output/${MATRIX_ARCH}/conf/huatuo-bamai.conf

# 2️⃣ Test
# just run huatuo-bamai for 60s
log_file=/tmp/huatuo-bamai.log
chmod +x ${MOUNT_DIR}/_output/${MATRIX_ARCH}/bin/huatuo-bamai
timeout -s SIGKILL 60s \
    ${MOUNT_DIR}/_output/${MATRIX_ARCH}/bin/huatuo-bamai \
    --region example \
    --config huatuo-bamai.conf \
    > $log_file 2>&1 || true
# colorize log
match_keywords="error|panic"
sed -E "s/($match_keywords)/\x1b[31m\1\x1b[0m/gI" $log_file
# check log for focus keywords
! grep -qE "$match_keywords" $log_file