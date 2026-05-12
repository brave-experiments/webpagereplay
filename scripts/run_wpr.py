#!/usr/bin/env vpython3
# Copyright 2026 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.
"""
Script to run WebPageReplay wpr.go.
Why not a single run.py with a --binary flag? To avoid handling run.py flags
on top of the underlying binary flags.
"""

from __future__ import annotations

import pathlib
import subprocess
import sys

import go_utils

if __name__ == "__main__":
    go_file = pathlib.Path(__file__).resolve().parents[1] / "src" / "wpr.go"
    subprocess.check_call(
        [str(go_utils.get_go_compiler_path()), "run",
         str(go_file)] + sys.argv[1:])
