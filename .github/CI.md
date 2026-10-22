# Pull request checks

`os-distro-qemu-ci.yml` (`OS Distro QEMU CI`) handles event triggers, filtering
and concurrency. It calls `os-distro-qemu-test-reusable.yml`
(`OS Distro QEMU Tests (Reusable)`) to build the matrix and run guest tests.

The build/lint/vendor and OS distro QEMU workflows run expensive jobs only for
non-draft PRs. Marking a draft ready for review triggers both workflows. Changes
limited to `docs/**` and `**/*.md` skip expensive jobs; mixed changes run normally.
Manual QEMU runs always execute the selected distro tests. Main pushes use the
same documentation filter, without the PR draft restriction.

PR QEMU runs select five representative guests:

| Guest | Kernel | Cgroup |
| --- | --- | --- |
| Rocky Linux 8.10 | 4.18 | v1 |
| openEuler 22.03 LTS-SP4 | 5.10 | v1 |
| openEuler 24.03 LTS-SP4 | 6.6 | v1 |
| Ubuntu 26.04 LTS | 7.0 | v2 |
| Ubuntu 20.04.6 LTS | 5.4 | hybrid |

These guests cover older and newer kernels, cgroup v1/v2/hybrid, and RPM/DEB
environments. Main pushes run all 13 guests. Compatibility failures on guests
outside the PR subset can therefore surface after merge. Manual runs still
support individual guests, comma-separated selections, and `all` for pre-merge
full validation. Kernel versions and cgroup modes above follow the documented
guest matrix and should be reviewed when guest images change.

Each workflow cancels previous runs for the same PR when a new commit arrives
or the PR becomes a draft. A separate job-level concurrency group per author
keeps one running task and at most one pending task across different PRs. A newer
pending task replaces the older pending task, but does not cancel another PR's
running task. Re-run checks if a pending task for a PR was replaced. This is not
a FIFO queue, and a replacement run has no guaranteed priority over other PRs.
Build and QEMU use separate author groups. QEMU holds its author slot across the
entire reusable workflow; distro jobs within that workflow still run in parallel.
Manual QEMU debug runs are not automatically cancelled by later manual runs.

The reusable QEMU workflow adds a `QEMU tests` prefix to its job check names.
Update branch protection rules if they require the previous individual VM check
names.

The change-classification job uses the entire PR diff, or the entire push range.
Unavailable Git history falls back to running tests. Renames are treated as
deletions and additions so moving code into documentation cannot skip tests.
Skipped build and QEMU jobs do not validate code; branch protection must not
require successful execution on documentation-only or draft PRs.

Run CI selection tests with:

```sh
python3 -B -m unittest discover -s .github/workflows/scripts -p 'test_*.py'
```
