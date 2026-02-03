#!/usr/bin/env bash
set -xeuo pipefail

ARCH=${1:-amd64}
OS_DISTRO=${2:-ubuntu24.04}
GOLANG_VERSION="1.24.0"

function print_sys_info() {
	# sys info
	uname -a
	if [ -f /etc/os-release ]; then
		cat /etc/os-release
	fi

	echo "$PATH" | tr ':' '\n' | awk '{printf "  %s\n", $0}'
	env | sort

	lscpu || true

	free -h

	ip addr show || true
	ip route show || true

	df -h

	# tool chains
	go version || ture
	go env || true

	docker version || true
	sudo docker info || true
	crictl version || true

	kubectl get pods -A || true
	systemctl status kubelet || true
	ps -ef | grep kubelet | grep -v grep || true

	curl -k --cert /var/lib/kubelet/pki/kubelet-client-current.pem \
		--key /var/lib/kubelet/pki/kubelet-client-current.pem \
		--header "Content-Type: application/json" \
		'https://127.0.0.1:10250/pods/' || true
}

function install_golang() {
	local GOLANG_URL="https://go.dev/dl/go$GOLANG_VERSION.linux-$ARCH.tar.gz"
	local GOLANG_TAR="go$GOLANG_VERSION.linux-$ARCH.tar.gz"

	wget -q -O "$GOLANG_TAR" "$GOLANG_URL"
	rm -rf /usr/local/go
	tar -C /usr/local -xzf "$GOLANG_TAR" && rm "$GOLANG_TAR"
	export PATH="/usr/local/go/bin:${PATH}"    # golang
	export PATH="$(go env GOPATH)/bin:${PATH}" # installed tools
}

function prapre_test_env() {
	case $OS_DISTRO in
	ubuntu*)
		apt update
		apt install make libbpf-dev clang git gcc -y
		;;
	esac

	go install github.com/vektra/mockery/v2@latest && which mockery
	git config --global --add safe.directory /mnt/host
}

print_sys_info
install_golang
prapre_test_env

cd /mnt/host && pwd
ls -alh /mnt/host

echo -e "\n\n⬅️ integration test..."

make integration

echo -e "✅ integration test ok."
