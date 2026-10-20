# Pull request checks

The build/lint/vendor and OS distro QEMU workflows run expensive jobs only for
non-draft PRs. Marking a draft ready for review triggers both workflows. Changes
limited to `docs/**` and `**/*.md` skip expensive jobs; mixed changes run normally.
Manual QEMU runs always execute the selected distro tests. Main pushes use the
same documentation filter, without the PR draft restriction.

The change-classification job uses the entire PR diff, or the entire push range.
Unavailable Git history falls back to running tests. Renames are treated as
deletions and additions so moving code into documentation cannot skip tests.
Skipped build and QEMU jobs do not validate code; branch protection must not
require successful execution on documentation-only or draft PRs.

Run classification tests with:

```sh
python3 -B -m unittest discover -s .github/workflows/scripts -p 'test_ci_changes.py'
```
