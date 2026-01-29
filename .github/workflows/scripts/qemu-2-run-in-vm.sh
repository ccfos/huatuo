#!/usr/bin/env bash
set -xeuo pipefail

ARCH=${1:-amd64}
OS_DISTRO=${2:-ubuntu24.04}

function print_sys_info() {
	# sys info
	uname -a
	if [ -f /etc/os-release ]; then
		cat /etc/os-release
	fi

	echo "$PATH" | tr ':' '\n' | awk '{printf "  %s\n", $0}'

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

function prapre_test_env() {
	case $OS_DISTRO in
	ubuntu*)
		apt update
		apt install make libbpf-dev clang git -y
		;;
	esac

	go install github.com/vektra/mockery/v2@latest
	git config --global --add safe.directory /mnt/host
}

print_sys_info
prapre_test_env

cd /mnt/host && pwd
ls -alh /mnt/host

echo -e "\n\n⬅️ integration test..."

make integration

echo -e "✅ integration test ok."
