#!/bin/bash
set -Eeuo pipefail

echo -e "\n==================== SYSTEM INFORMATION ====================\n"

echo -e "------- OS Information:"
uname -a
if [ -f /etc/os-release ]; then
  echo -e "\n/etc/os-release contents:"
  cat /etc/os-release
fi

echo -e "\n------- PATH Environment Variable:"
echo "$PATH" | tr ':' '\n' | awk '{printf "  %s\n", $0}'

echo -e "\n------- CPU Information:"
lscpu 2>/dev/null || echo "lscpu: not installed or failed"

echo -e "\n------- Memory Information:"
free -h

echo -e "\n------- Network Information:"
echo -e "\nNetwork Interfaces:"
ip addr show 2>/dev/null || echo "ip: not installed"
echo -e "\nRouting Table:"
ip route show 2>/dev/null || echo "ip: not installed"

echo -e "\n------- Disk Usage:"
df -h

echo -e "\n==================== TOOLCHAIN INFORMATION ====================\n"

echo -e "------- Go Environment:"
if command -v go &> /dev/null; then
  go version
  echo -e "\nGo environment variables:"
  go env | grep -E "^(GO|GOROOT|GOPATH|GOVERSION)"
else
  echo "go: not installed"
fi

echo -e "\n------- Docker Information:"
if command -v docker &> /dev/null; then
  docker version 2>/dev/null || true
  echo -e "\nDocker system info:"
  sudo docker info 2>/dev/null || true
else
  echo "docker: not installed"
fi

echo -e "\n==================== END OF SYSTEM REPORT ====================\n"