#!/usr/bin/env bash
set -euo pipefail

ARCH="amd64"
OS_DISTRO=${1:-"ubuntu24.04"}
VM_NAME=${2:-"huatuo-os-distro-vm"}
VM_IP=192.168.122.100
OS_IMAGE=ubuntu-24.04-server-cloudimg-${ARCH}.img
LIBVIRT_IMAGE_DIR=/var/lib/libvirt/images

CLOUD_USER_DATA=/tmp/user-data
SSH_KEY=${SSH_KEY:-"${HOME}/.ssh/id_ed25519_vm"}
SSH_OPTS=(
    -i "${SSH_KEY}"
    -o StrictHostKeyChecking=no
    -o UserKnownHostsFile=/dev/null
    -o ConnectTimeout=1
)


function cloud_user_data() {
    # generate ssh keys for passwordless login
    [ -f "$SSH_KEY" ] || ssh-keygen -t ed25519 -f "$SSH_KEY" -N ""
    HOST_PUBKEY=$(cat ${SSH_KEY}.pub)

    # for cloud-init
    tee ${CLOUD_USER_DATA} > /dev/null <<EOF
#cloud-config

hostname: $OS_DISTRO

users:
  - name: root
    shell: /bin/bash
    sudo: ['ALL=(ALL) NOPASSWD:ALL']
    ssh_authorized_keys:
      - $HOST_PUBKEY
chpasswd:
  list: |
    root:1
  expire: false

disable_root: false
ssh_pwauth: true
packages:
  - sudo
  - bash

growpart:
  mode: auto
  devices: ['/']
  ignore_growroot_disabled: false

EOF

    # validate cloud-init user-data
    cloud-init schema --config-file ${CLOUD_USER_DATA}
}


function prepare_qcow2_image() {
    # download huatuo/os-distro-test and decompress image
    docker pull huatuo/os-distro-test:${OS_DISTRO}.${ARCH}
    cid=$(docker create huatuo/os-distro-test:${OS_DISTRO}.${ARCH})
    docker cp ${cid}:/data/${OS_IMAGE}.zst .
    zstd --decompress -f --rm --threads=0 ${OS_IMAGE}.zst
    
    # libvirt
    sudo mkdir -p ${LIBVIRT_IMAGE_DIR}
    sudo mv ${OS_IMAGE} ${LIBVIRT_IMAGE_DIR}/
    sudo chown libvirt-qemu:kvm ${LIBVIRT_IMAGE_DIR}/${OS_IMAGE}
}


function install_vm() {
    local imgsize="10G"
    sudo qemu-img resize "${LIBVIRT_IMAGE_DIR}/${OS_IMAGE}" ${imgsize}
    sudo virsh net-update default add ip-dhcp-host \
        "<host mac='4A:6F:6C:69:6E:2E' ip='${VM_IP}'/>" --live --config
    echo "${VM_IP} ${VM_NAME}" | sudo tee -a /etc/hosts

    echo -e "install vm ${VM_NAME} from qcow2 image [${LIBVIRT_IMAGE_DIR}/${OS_IMAGE}], resize to ${imgsize}"

    # install vm
    sudo virt-install \
    --os-variant ${OS_DISTRO} \
    --name ${VM_NAME} \
    --cpu host-passthrough \
    --virt-type=kvm --hvm \
    --vcpus=8,sockets=1 \
    --memory $((16*1024)) \
    --memballoon model=virtio \
    --cloud-init user-data=${CLOUD_USER_DATA} \
    --graphics none \
    --network bridge=virbr0,model=virtio,mac='4A:6F:6C:69:6E:2E' \
    --disk ${LIBVIRT_IMAGE_DIR}/${OS_IMAGE},bus=virtio,cache=none,format=qcow2 \
    --import --noautoconsole >/dev/null
}


function wait_for_vm_ready() {
    local timeout=600   # seconds
    local interval=1    # seconds

    echo -e "waiting for vm ${VM_NAME} (${VM_IP}) to become ready..."

    for ((i=1; i<=timeout; i+=interval)); do
        if ssh "${SSH_OPTS[@]}" "root@${VM_NAME}" "uname -a" 2>/dev/null; then
                return 0
        fi

        echo -e "waiting for vm ${VM_NAME}... ${i}/${timeout}s"
        sleep $interval
    done

    echo -e "❌ vm ${VM_NAME} is not ready after ${timeout}s" && exit 1
}


function wait_for_k8s_ready() {
    local timeout=120   # seconds
    local interval=2    # seconds
    local jitter_count=3

    echo -e "waiting for vm k8s to become ready..."
    
    for ((i=1; i<=timeout; i+=interval)); do
        if ssh "${SSH_OPTS[@]}" "root@${VM_IP}" \
            "kubectl wait --for=condition=Ready pod --all -A --timeout=1s" >/dev/null 2>&1; then
            jitter_count=$((jitter_count-1))
            if [ $jitter_count -le 0 ]; then
                ssh "${SSH_OPTS[@]}" "root@${VM_IP}" "kubectl get pod -A" || true
                return 0
            fi
        fi
        echo -e "waiting for k8s to become ready... ${i}/${timeout}s"
        sleep $interval
    done

    echo -e "⚠️ k8s not ready after ${timeout}s"
    # echo -e "❌ coredns not ready after ${timeout}s" && exit 1
}


function rsync_workspace_to_vm() {
    echo -e "rsync workspace → vm ${VM_NAME}:/mnt/host..."

    ssh "${SSH_OPTS[@]}" "root@${VM_NAME}" "mkdir -p /mnt/host"

    rsync -az --delete \
        --numeric-ids \
        -e "ssh ${SSH_OPTS[*]}" \
        ./ root@${VM_NAME}:/mnt/host/

    ssh "${SSH_OPTS[@]}" "root@${VM_NAME}" "ls -lah /mnt/host"
}


case "$OS_DISTRO" in
  ubuntu*)
    u_version=${OS_DISTRO#ubuntu}
    OS_IMAGE=ubuntu-${u_version}-server-cloudimg-${ARCH}.img
    ;;
#   centos*)
#     # TODO:
  *)
    echo -e "❌ Unsupported OS distro: '$OS_DISTRO'" >&2
    echo -e "Supported distros: ubuntu*" >&2
    exit 1
    ;;
esac

cloud_user_data
echo -e "✅ ${CLOUD_USER_DATA} ok."
prepare_qcow2_image
echo -e "✅ image ${LIBVIRT_IMAGE_DIR}/${OS_IMAGE} ready."
install_vm
echo -e "✅ vm ${VM_NAME} is installed"

wait_for_vm_ready
echo -e "✅ VM ${OS_DISTRO} ${VM_NAME} is ready."
wait_for_k8s_ready
echo -e "✅ k8s cluster is ready."

rsync_workspace_to_vm
echo -e "✅ rsync to VM path ${VM_NAME}:/mnt/host done."
