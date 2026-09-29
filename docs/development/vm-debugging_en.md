---
title: Debug a VM in GitHub Actions
type: docs
description:
author: HUATUO Team
date: 2026-09-29
weight: 6
---

Use the `OS Distro QEMU Test` workflow to test a branch in a supported Linux
VM. No local VM setup is required.

## Prepare Your Fork

1. Fork the repository and enable workflows on the fork's **Actions** page.
2. Add an SSH public key to your
   [GitHub account](https://github.com/settings/keys). Only the user who starts
   the workflow can connect.
3. Push the branch that you want to debug to your fork.

## Start a VM

1. Open **Actions > OS Distro QEMU Test > Run workflow** in your fork.
2. Select your branch.
3. Enter one distribution in **os**, such as `ubuntu24.04`. An empty value
   starts a VM for every supported distribution.
4. Select when to retain the VM and the retention time under **Debug VM**.
5. Run the workflow and open its `Test in VM` job.

If **Run workflow** is missing, sync the fork's default branch first. GitHub
only lists manual workflows that exist on the default branch.

Use **Before test** to inspect or change the VM before tests run. Use
**After test on failure** to investigate a failed test, or **After test
always** to keep the VM regardless of the result.

## Connect

Open the job's `Debug VM: Retain ...` step and follow its commands:

1. Run the displayed SSH command in your local terminal. This connects
   to the GitHub runner.
2. On the runner, run the displayed `ssh -i ... root@<VM_IP>` command to enter
   the VM.

The commands already contain the correct key path and VM address.

## Finish

Exit the VM, then release the workflow from the runner:

```bash
cd "$GITHUB_WORKSPACE"
touch continue
```

The workflow continues to the tests or cleanup. Disconnecting SSH alone does
not release it; otherwise it waits until the selected retention time expires.
