#!/usr/bin/env bash
set -euo pipefail

ARCH=${1:-amd64}
OS_DISTRO=${2:-ubuntu24.04}

case "$ARCH" in
amd64) ;;
arm64) ;;
*)
	echo -e "❌ Unsupported ARCH: '$ARCH', Supported ARCHs: amd64, arm64" >&2
	exit 1
	;;
esac

case "$OS_DISTRO" in
ubuntu*)
	PACKAGES=("cloud-image-utils" "virt-manager" "qemu-utils" "cloud-init" "rsync")
	MISSING_PACKAGES=()

	for pkg in "${PACKAGES[@]}"; do
		if dpkg --status "$pkg" &>/dev/null; then
			echo "$pkg is already installed."
		else
			echo "$pkg is missing."
			MISSING_PACKAGES+=("$pkg")
		fi
	done

	if [ "${#MISSING_PACKAGES[@]}" -gt 0 ]; then
		echo "installing missing packages: ${MISSING_PACKAGES[*]}"
		sudo apt-get update
		sudo apt-get install -y "${MISSING_PACKAGES[@]}"
	fi
	;;
*)
	echo -e "❌ Unsupported OS distro: '$OS_DISTRO', only ubuntu* is supported." >&2
	exit 1
	;;
esac
