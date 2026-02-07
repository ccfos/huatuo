#!/usr/bin/env bash
set -xeuo pipefail

ARCH=${1:-amd64}
OS_DISTRO=${2:-ubuntu24.04}
GOLANG_VERSION="1.24.0"

COMMAMND_DEPS=(
	"go"
	"make"
	"clang"
	"gcc"
	"git"
	"jq"
	"kubectl"
	"curl"
	"wget"
)

function check_command_deps() {
	local ok=1
	for cmd in "${COMMAMND_DEPS[@]}"; do
		if ! command -v "$cmd" &>/dev/null; then
			echo "⚠️ $cmd not found"
			ok=0
		fi
	done

	[ $ok -eq 1 ] || exit 1
}

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
	crictl images || true
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
		apt update >/dev/null
		apt install make libbpf-dev clang git gcc jq -y >/dev/null
		;;
	esac

	go install github.com/vektra/mockery/v2@latest && which mockery
	git config --global --add safe.directory /mnt/host
}

print_sys_info
install_golang
prapre_test_env
check_command_deps

cd /mnt/host && pwd
ls -alh /mnt/host

echo -e "\n\n⬅️ test..."

make test

echo -e "✅ test ok."
