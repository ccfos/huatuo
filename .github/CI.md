# Pull request checks

The build/lint/vendor and OS distro QEMU workflows run expensive jobs only for
non-draft PRs. Marking a draft ready for review triggers both workflows. Main
pushes and manual QEMU runs are not restricted by PR draft status.
