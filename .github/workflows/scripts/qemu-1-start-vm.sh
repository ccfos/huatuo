#!/bin/bash
set -e

OS_DISTRO=${1:-ubuntu24.04}
VM_NAME=${2:-huatuo-os-distro-vm}
QCOW2_IMAGE=ubuntu-24.04-server-cloudimg-amd64.img
LIBVIRT_IMAGE_DIR=/var/lib/libvirt/images
CLOUD_USER_DATA=/tmp/user-data
VM_IP=192.168.122.100

# Handle different os distro
case "$OS_DISTRO" in
  ubuntu*)
    u_version=${OS_DISTRO#ubuntu}
    QCOW2_IMAGE=ubuntu-${u_version}-server-cloudimg-amd64.img
    ;;
#   centos*)
#     # TODO:
  *)
    echo "[ERROR] Unsupported OS distro: '$OS_DISTRO'" >&2
    echo "[ERROR] Supported distros: ubuntu*" >&2
    exit 1
    ;;
esac

# Create ssh key pair for passwordless login
rm -f ~/.ssh/id_ed25519
ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519 -q -N ""
HOST_PUBKEY=$(cat ~/.ssh/id_ed25519.pub)

# Create cloud-init user-data
cat <<EOF > ${CLOUD_USER_DATA}
#cloud-config

hostname: $OS_DISTRO

users:
  - name: root
    shell: /bin/bash
    sudo: ['ALL=(ALL) NOPASSWD:ALL']
    ssh_authorized_keys:
      - $HOST_PUBKEY

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

# Download huatuo/os-distro-test and decompress image
docker pull huatuo/os-distro-test:${OS_DISTRO}.amd64
cid=$(docker create huatuo/os-distro-test:${OS_DISTRO}.amd64)
docker cp ${cid}:/data/${QCOW2_IMAGE}.zst .
zstd --decompress -f --rm --threads=0 ${QCOW2_IMAGE}.zst
qemu-img resize ${QCOW2_IMAGE} 10G
sudo mkdir -p ${LIBVIRT_IMAGE_DIR}
sudo mv ${QCOW2_IMAGE} ${LIBVIRT_IMAGE_DIR}/
sudo chown libvirt-qemu:kvm ${LIBVIRT_IMAGE_DIR}/${QCOW2_IMAGE}

# Bind mac address to vm ip
sudo virsh net-update default add ip-dhcp-host \
    "<host mac='4A:6F:6C:69:6E:2E' ip='${VM_IP}'/>" --live --config
echo " ${VM_IP} ${VM_NAME}" | sudo tee -a /etc/hosts

# Install VM
sudo virt-install \
  --os-variant $OS_DISTRO \
  --name ${VM_NAME} \
  --cpu host-passthrough \
  --virt-type=kvm --hvm \
  --vcpus=4,sockets=1 \
  --memory $((4*1024)) \
  --memballoon model=virtio \
  --cloud-init user-data=${CLOUD_USER_DATA} \
  --graphics none \
  --network network=default,model=virtio,mac='4A:6F:6C:69:6E:2E' \
  --disk ${LIBVIRT_IMAGE_DIR}/${QCOW2_IMAGE},bus=virtio,cache=none,format=qcow2 \
  --import --noautoconsole >/dev/null

# Wait for vm to be ready
set +e
WAIT_VM_OK_TIMEOUT=600 # seconds
VM_IS_READY=0
SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=1"
for i in $(seq 1 ${WAIT_VM_OK_TIMEOUT}); do
  ssh 2>/dev/null ${SSH_OPTS} root@${VM_IP} "uname --all" && VM_IS_READY=1 && break
  echo "waiting for vm to be ready $i/${WAIT_VM_OK_TIMEOUT}s"
  sleep 1
done
if [ "$VM_IS_READY" -ne 1 ]; then
  echo -e "\033[1;31m vm ${VM_NAME} is not ready after $WAIT_VM_OK_TIMEOUT seconds \033[0m"
  exit 1
fi

# VM is ready
echo "========= VM ${OS_DISTRO} ${VM_NAME} is ready =========="
