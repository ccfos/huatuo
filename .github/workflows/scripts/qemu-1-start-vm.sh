#!/usr/bin/env bash
set -euo pipefail

ARCH=${1:-"amd64"}
OS_DISTRO=${2:-"ubuntu24.04"}
VM_NAME=${3:-"huatuo-os-distro-test-vm"}
VM_MAC="4A:6F:6C:69:6E:2E"
VM_IP="192.168.122.100"
VM_VCPUS=4
VM_MEMORY_MB=$((16 * 1024))
VM_DISK_SIZE="20G"
VM_ROOT="/mnt/host"
OS_IMAGE=ubuntu-24.04-server-cloudimg-${ARCH}.img
LIBVIRT_IMAGE_DIR=/var/lib/libvirt/images
CLOUD_USER_DATA=/tmp/user-data
CLOUD_META_DATA=/tmp/meta-data
CLOUD_INIT_ISO=/tmp/cloud-init.iso
SSH_KEY=${SSH_KEY:-"${HOME}/.ssh/id_ed25519_vm"}
SSH_OPTS=(
	-i "${SSH_KEY}"
	-o StrictHostKeyChecking=no
	-o UserKnownHostsFile=/dev/null
	-o ConnectTimeout=3
)

# for local validate, workflow ignore it
# "$value": local image path, "": workflow default
# your qcow2 image should be in ${LOCAL_IMG_PATH}/${OS_IMAGE}
LOCAL_IMG_PATH=${LOCAL_IMG_PATH:-""}

function cloud_user_data() {
	# generate ssh keys for passwordless login
	[ -f "$SSH_KEY" ] || ssh-keygen -t ed25519 -f "$SSH_KEY" -N ""
	HOST_PUBKEY=$(cat ${SSH_KEY}.pub)

	# for cloud-init
	tee ${CLOUD_USER_DATA} >/dev/null <<EOF
#cloud-config

hostname: $OS_DISTRO

users:
  - name: root
    shell: /bin/bash
    sudo: ['ALL=(ALL) NOPASSWD:ALL']
    ssh_authorized_keys:
      - $HOST_PUBKEY
chpasswd:
  expire: false
  list: |
    root:1

ssh_pwauth: true
disable_root: false
package_upgrade: false

growpart:
  mode: auto
  devices: ['/']
  ignore_growroot_disabled: false
EOF

	if [ -n "$LOCAL_IMG_PATH" ]; then
		tee -a ${CLOUD_USER_DATA} >/dev/null <<EOF
runcmd:
  - mkdir -p ${VM_ROOT}
  - echo "hostshare ${VM_ROOT} 9p trans=virtio,version=9p2000.L,access=any,_netdev 0 0" >> /etc/fstab
  - mount -a
EOF
	fi

	touch "$CLOUD_META_DATA"

	# validate cloud-init user-data
	cloud-init schema --config-file ${CLOUD_USER_DATA}
}

function prepare_qcow2_image() {
	sudo mkdir -p ${LIBVIRT_IMAGE_DIR}

	if [ -n "$LOCAL_IMG_PATH" ]; then
		echo -e "LOCAL_IMG_PATH=${LOCAL_IMG_PATH}, cp ${LOCAL_IMG_PATH}/${OS_IMAGE} ${LIBVIRT_IMAGE_DIR}/"
		sudo cp ${LOCAL_IMG_PATH}/${OS_IMAGE} ${LIBVIRT_IMAGE_DIR}/
	else
		docker pull huatuo/os-distro-test:${OS_DISTRO}.${ARCH}
		cid=$(docker create huatuo/os-distro-test:${OS_DISTRO}.${ARCH})
		docker cp ${cid}:/data/${OS_IMAGE}.zst .
		zstd --decompress -f --rm --threads=0 ${OS_IMAGE}.zst

		sudo mv ${OS_IMAGE} ${LIBVIRT_IMAGE_DIR}/
	fi

	sudo chown libvirt-qemu:kvm ${LIBVIRT_IMAGE_DIR}/${OS_IMAGE}

	if [[ "$ARCH" == "arm64" ]]; then
		echo -e "prepare cloud-init iso [${CLOUD_INIT_ISO}] for ${ARCH}"
		genisoimage -output "$CLOUD_INIT_ISO" -volid cidata -joliet -rock \
			"$CLOUD_USER_DATA" "$CLOUD_META_DATA"
		sudo chown libvirt-qemu:kvm "$CLOUD_INIT_ISO"
	fi

	sudo qemu-img resize "${LIBVIRT_IMAGE_DIR}/${OS_IMAGE}" ${VM_DISK_SIZE}
}

function install_vm() {
	sudo virsh net-update default add ip-dhcp-host \
		"<host mac='${VM_MAC}' ip='${VM_IP}'/>" --live --config
	echo "${VM_IP} ${VM_NAME}" | sudo tee -a /etc/hosts

	echo -e "install vm ${VM_NAME} from qcow2 [${LIBVIRT_IMAGE_DIR}/${OS_IMAGE}], resize to ${VM_DISK_SIZE}"

	# install vm
	VIRT_LOCAL_ARG=()
	if [ -n "$LOCAL_IMG_PATH" ]; then
		VIRT_LOCAL_ARG=(
			--filesystem source="$(pwd)",target=hostshare,type=mount,accessmode=passthrough
		)
	fi

	VIRT_COMMON_ARG=(
		--name "${VM_NAME}"
		--os-variant "${OS_DISTRO}"
		--vcpus "${VM_VCPUS}"
		--memory "${VM_MEMORY_MB}"
		--disk path="${LIBVIRT_IMAGE_DIR}/${OS_IMAGE}",bus=virtio,cache=none,format=qcow2
		--network network=default,model=virtio,mac=${VM_MAC}
		--import
		--graphics none
		--noautoconsole
	)
	VIRT_X86_64_ARG=(
		"${VIRT_COMMON_ARG[@]}"
		--cloud-init user-data=${CLOUD_USER_DATA}
		"${VIRT_LOCAL_ARG[@]}"
	)
	VIRT_ARM64_ARG=(
		"${VIRT_COMMON_ARG[@]}"
		--arch aarch64
		--machine virt
		# --cpu cortex-a57
		--disk path="${CLOUD_INIT_ISO}",device=cdrom
		--boot loader=/usr/share/AAVMF/AAVMF_CODE.fd,loader.readonly=yes,loader.type=pflash,nvram.template=/usr/share/AAVMF/AAVMF_VARS.fd
		"${VIRT_LOCAL_ARG[@]}"
	)

	case "$ARCH" in
	amd64)
		echo -e "🧩 [amd64] sudo virt-install ${VIRT_X86_64_ARG[@]}"
		sudo virt-install "${VIRT_X86_64_ARG[@]}"
		;;
	arm64)
		echo -e "🧩 [arm64] sudo virt-install ${VIRT_ARM64_ARG[@]}"
		sudo virt-install "${VIRT_ARM64_ARG[@]}"
		;;
	esac
}

function wait_for_vm_ready() {
	local timeout=600 # seconds
	local interval=1  # seconds

	echo -e "waiting for vm ${VM_NAME} (${VM_IP}) to become ready..."

	for ((i = 1; i <= timeout; i += interval)); do
		if ssh "${SSH_OPTS[@]}" "root@${VM_IP}" "uname -a"; then
			return 0
		fi

		echo -e "waiting for vm ${VM_NAME}... ${i}/${timeout}s"
		sleep $interval
	done

	echo -e "❌ vm ${VM_NAME} is not ready after ${timeout}s" && exit 1
}

function wait_for_k8s_ready() {
	local timeout=120 # seconds
	local interval=2  # seconds
	local jitter_count=3

	echo -e "waiting for vm k8s to become ready..."

	for ((i = 1; i <= timeout; i += interval)); do
		if ssh "${SSH_OPTS[@]}" "root@${VM_IP}" \
			"kubectl wait --for=condition=Ready pod --all -A --timeout=3s" >/dev/null 2>&1; then
			jitter_count=$((jitter_count - 1))
			if [ $jitter_count -le 0 ]; then
				ssh "${SSH_OPTS[@]}" "root@${VM_IP}" "kubectl get pod -A" || true
				return 0
			fi
		fi
		echo -e "waiting for k8s to become ready... ${i}/${timeout}s"
		sleep $interval
	done

	echo -e "❌ k8s not ready after ${timeout}s, but continue, dont exit."
}

function rsync_workspace_to_vm() {
	echo -e "rsync workspace → vm ${VM_NAME}:${VM_ROOT}..."

	if [ -n "$LOCAL_IMG_PATH" ]; then
		echo -e "LOCAL_IMG_PATH=${LOCAL_IMG_PATH}, skip rsync."
		return 0
	fi

	ssh "${SSH_OPTS[@]}" "root@${VM_IP}" "mkdir -p ${VM_ROOT}"

	rsync -az --delete \
		--numeric-ids \
		-e "ssh ${SSH_OPTS[*]}" \
		./ root@${VM_IP}:${VM_ROOT}/

	ssh "${SSH_OPTS[@]}" "root@${VM_IP}" "ls -lah ${VM_ROOT}"
}

echo -e "\n\nARCH: $ARCH OS_DISTRO: $OS_DISTRO"
case "$OS_DISTRO" in
ubuntu*)
	u_version=${OS_DISTRO#ubuntu}
	OS_IMAGE=ubuntu-${u_version}-server-cloudimg-${ARCH}.img
	;;
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
echo -e "✅ k8s is ready."

rsync_workspace_to_vm
echo -e "✅ rsync to VM path ${VM_NAME}:${VM_ROOT} done."
