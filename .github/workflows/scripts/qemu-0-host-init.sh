#!/usr/bin/env bash
set -euo pipefail

ARCH=${1:-amd64}
OS_DISTRO=${2:-ubuntu24.04}

case "$ARCH" in
amd64)
	;;
arm64)
	;;
*)
	echo -e "❌ Unsupported ARCH: '$ARCH'" >&2
	echo -e " Supported ARCHs: amd64, arm64" >&2
	exit 1
	;;
esac


case "$OS_DISTRO" in
ubuntu*)
	# Install dependencies
	sudo apt-get update -y
	sudo apt-get install -y cloud-image-utils virt-manager qemu-utils
	;;
*)
	echo -e "❌ Unsupported OS distro: '$OS_DISTRO'" >&2
	echo -e " Supported distros: ubuntu*" >&2
	exit 1
	;;
esac

