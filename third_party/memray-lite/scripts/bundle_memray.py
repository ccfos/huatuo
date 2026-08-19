#!/usr/bin/env python3
"""Create a relocatable memray-lite bundle for external tooling."""

import argparse
import shutil
import subprocess
import sys
import tempfile
import textwrap
from pathlib import Path
from typing import Iterable, Optional, Sequence


def shlex_join(parts: Iterable[str]) -> str:
    import shlex

    return " ".join(shlex.quote(str(p)) for p in parts)


def run(cmd: Sequence[str], **kwargs) -> None:
    print(f"+ {shlex_join(cmd)}")
    subprocess.run(cmd, check=True, **kwargs)


def build_wheel(python: str, outdir: Path) -> Path:
    outdir.mkdir(parents=True, exist_ok=True)
    run([python, "-m", "build", "--wheel", "--outdir", str(outdir)])
    wheels = sorted(outdir.glob("memray-*.whl"))
    if not wheels:
        raise RuntimeError("build did not produce a memray wheel")
    return wheels[-1]


def install_wheel(python: str, wheel: Path, target: Path) -> None:
    target.mkdir(parents=True, exist_ok=True)
    run([python, "-m", "pip", "install", "--no-deps", "--target", str(target), str(wheel)])


def write_wrapper(bin_dir: Path) -> None:
    bin_dir.mkdir(parents=True, exist_ok=True)
    script = bin_dir / "memray"
    script.write_text(
        textwrap.dedent(
            """
            #!/usr/bin/env bash
            set -euo pipefail
            ROOT="$(cd "$(dirname "$0")/.." && pwd)"
            export PYTHONPATH="$ROOT/python${PYTHONPATH:+:$PYTHONPATH}"
            exec "${MEMRAY_PYTHON:-python3}" -m memray "$@"
            """
        ).strip()
        + "\n"
    )
    script.chmod(0o755)


def write_readme(bundle_dir: Path) -> None:
    readme = bundle_dir / "README.bundle"
    readme.write_text(
        textwrap.dedent(
            f"""
            memray-lite bundle
            ==================

            Layout
            ------
            bin/memray          Wrapper that sets PYTHONPATH and executes `python3 -m memray`.
            python/             Site-packages tree produced via `pip install --target`.

            Usage
            -----
            1. Copy this directory to the host or container.
            2. Optionally set `MEMRAY_PYTHON=/path/to/python3` if python3 is not on PATH.
            3. Run `bin/memray <subcommand>` (e.g. `bin/memray attach ...`).

            Container attach tips
            ---------------------
            - Pass `--injector-path /path/to/bundle/python/memray/_inject*.so` to `memray attach`.
            - Pass `--target-pythonpath /path/to/bundle/python` so the injected payload can import memray.
            """
        ).strip()
        + "\n"
    )


def make_tarball(bundle_dir: Path) -> Path:
    base = bundle_dir.parent / bundle_dir.name
    archive = shutil.make_archive(str(base), "gztar", root_dir=bundle_dir.parent, base_dir=bundle_dir.name)
    return Path(archive)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Create a relocatable memray-lite bundle")
    parser.add_argument(
        "--output",
        default="build/memray-lite",
        type=Path,
        help="Directory where the bundle will be created",
    )
    parser.add_argument(
        "--python",
        default=sys.executable,
        help="Python interpreter to use for build/pip commands (default: current interpreter)",
    )
    parser.add_argument(
        "--wheel",
        type=Path,
        help="Existing memray wheel to re-use; if omitted a fresh wheel is built",
    )
    parser.add_argument(
        "--tarball",
        action="store_true",
        help="Create a .tar.gz archive next to the bundle directory",
    )
    parser.add_argument(
        "--clean",
        action="store_true",
        help="Remove the output directory before building",
    )
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    bundle_dir = args.output.resolve()
    if args.clean and bundle_dir.exists():
        shutil.rmtree(bundle_dir)

    site_dir = bundle_dir / "python"
    bin_dir = bundle_dir / "bin"

    temp_dir = None  # type: Optional[Path]
    try:
        wheel = args.wheel
        if wheel is None:
            temp_dir = Path(tempfile.mkdtemp(prefix="memray-bundle-wheel"))
            wheel = build_wheel(args.python, temp_dir)
        install_wheel(args.python, wheel, site_dir)
        write_wrapper(bin_dir)
        write_readme(bundle_dir)
        if args.tarball:
            archive = make_tarball(bundle_dir)
            print(f"Created {archive}")
    finally:
        if temp_dir and temp_dir.exists():
            shutil.rmtree(temp_dir)

    print(f"Bundle ready at {bundle_dir}")


if __name__ == "__main__":
    main()
