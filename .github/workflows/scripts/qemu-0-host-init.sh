#!/bin/bash

OS_DISTRO=${1:-ubuntu24.04}

# Handle different os distro
case "$OS_DISTRO" in
  ubuntu*)
    # Install dependencies
    sudo apt-get update -y
    sudo apt-get install -y cloud-image-utils virt-manager qemu-utils
    ;;
#   centos*)
#     # TODO:
  *)
    echo "[ERROR] Unsupported OS distro: '$OS_DISTRO'" >&2
    echo "[ERROR] Supported distros: ubuntu*" >&2
    exit 1
    ;;
esac
