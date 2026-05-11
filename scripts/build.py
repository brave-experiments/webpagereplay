#!/usr/bin/env vpython3
# Copyright 2026 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.
"""Script to build WebPageReplay Go binaries."""

import argparse
import logging
import os
import pathlib
import subprocess
import sys

_REPO_DIR = pathlib.Path(__file__).resolve().parents[1]
_SRC_DIR = _REPO_DIR / "src"
_GO_COMPILER_PATH = _REPO_DIR / "third_party" / "golang" / "bin" / "go"


def _compute_go_arch(os_arch):
    # As given by the output of `go tool dist list`
    if os_arch == "x64":
        return "amd64"
    if os_arch == "x86":
        return "386"
    if os_arch == "arm64":
        return "arm64"
    if os_arch == "arm32":
        return "arm"
    raise ValueError(f"Invalid architecture {os_arch}")


def _compute_go_os(os_name):
    # As given by the output of `go tool dist list`
    if os_name == "win":
        return "windows"
    if os_name == "mac":
        return "darwin"
    if os_name in ("linux", "android"):
        return os_name
    raise ValueError(f"Invalid OS {os_name}")


def _run(cmd, env=None, stdout=None, stderr=None):
    if env is None:
        env = {}
    env_str = " ".join([f"{k}={v}" for k, v in env.items()])
    cmd_str = " ".join(cmd)
    logging.info(f"{env_str} {cmd_str}")
    subprocess.check_call(cmd,
                          env=os.environ.copy() | env,
                          stdout=stdout,
                          stderr=stderr)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--os",
        required=True,
        help="Target OS (win, mac, linux or android)",
    )
    parser.add_argument("--arch",
                        required=True,
                        help="Target arch (x64, x86, arm64 or arm32)")
    parser.add_argument("--output",
                        required=True,
                        help="Output path for the binary")
    parser.add_argument("--binary",
                        required=True,
                        help="Binary to build (wpr or httparchive)")
    parser.add_argument("--verbose",
                        action="store_true",
                        help="Enable verbose logging")
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO if args.verbose else logging.ERROR)
    output_file = pathlib.Path(args.output).resolve()
    output_file.parent.mkdir(parents=True, exist_ok=True)
    _run(
        [
            str(_GO_COMPILER_PATH),
            "build",
            "-C",
            str(_SRC_DIR),
            "-trimpath",
            "-buildvcs=false",
            "-o",
            str(output_file),
            f"{args.binary}.go",
        ],
        {
            "GOOS": _compute_go_os(args.os),
            "GOARCH": _compute_go_arch(args.arch),
            "CGO_ENABLED": "0",
        },
    )

    return 0


if __name__ == "__main__":
    sys.exit(main())
