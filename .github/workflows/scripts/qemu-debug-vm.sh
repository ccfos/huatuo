#!/usr/bin/env bash

set -euo pipefail

: "${DEBUG_TIMEOUT_MINUTES:?DEBUG_TIMEOUT_MINUTES is required}"
: "${GITHUB_WORKSPACE:?GITHUB_WORKSPACE is required}"
: "${UPTERM_SSH_COMMAND:?UPTERM_SSH_COMMAND is required}"
: "${VM_IP:?VM_IP is required}"
: "${VM_SSH_KEY:?VM_SSH_KEY is required}"

if ! [[ "$DEBUG_TIMEOUT_MINUTES" =~ ^[0-9]+$ ]] ||
  ((DEBUG_TIMEOUT_MINUTES < 1 || DEBUG_TIMEOUT_MINUTES > 180)); then
  echo "Invalid debug timeout: $DEBUG_TIMEOUT_MINUTES (expected 1-180 minutes)" >&2
  exit 1
fi

printf '\033[1m[STEP 1️⃣/3] Connect to Host(GitHub runner)\033[0m\n\n'
printf '\033[1;33mNOTICE:\033[0m Add your local SSH public key to:\n'
echo "https://github.com/settings/keys"
echo
printf '\033[1mCOPY & RUN \033[1;35mIN LOCAL TERMINAL\033[0m:\n'
printf '\033[1;36m  $ %s\033[0m\n\n' "$UPTERM_SSH_COMMAND"

printf '\033[1m[STEP 2️⃣/3] Connect to VM\033[0m\n\n'
printf '\033[1mCOPY & RUN \033[1;35mIN RUNNER\033[0m:\n'
printf '\033[1;36m  $ ssh -i "%s" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null root@"%s"\033[0m\n\n' "$VM_SSH_KEY" "$VM_IP"

printf '\033[1m[STEP 3️⃣/3] Finish debugging\033[0m\n\n'
printf '\033[1mRUN \033[1;35mIN VM\033[0m:\n'
printf '\033[1;36m  $ exit\033[0m\n\n'
printf '\033[1mCOPY & RUN \033[1;35mIN RUNNER\033[0m:\n'
printf '\033[1;36m  $ cd "%s" && touch continue\033[0m\n' "$GITHUB_WORKSPACE"
echo

deadline=$((SECONDS + DEBUG_TIMEOUT_MINUTES * 60))
while [[ ! -e /continue && ! -e "$GITHUB_WORKSPACE/continue" ]] &&
  ((SECONDS < deadline)); do
  sleep 3
done

# Ensure the detached Upterm action exits before its post step.
touch "$GITHUB_WORKSPACE/continue"
