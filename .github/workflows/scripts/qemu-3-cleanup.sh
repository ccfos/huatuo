#!/usr/bin/env bash
set -xeuo pipefail

VM_NAME=${1:-"huatuo-os-distro-test-vm"}
VM_MAC=${2:-"4A:6F:6C:69:6E:2E"}
VM_IP=${3:-"192.168.122.100"}

virsh net-update default delete ip-dhcp-host \
	"<host mac='${VM_MAC}' ip='${VM_IP}'/>" --live --config || true
virsh destroy ${VM_NAME} || true
virsh undefine ${VM_NAME} --nvram || true
virsh undefine ${VM_NAME} || true
