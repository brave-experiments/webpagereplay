#!/usr/bin/env vpython3
# Copyright 2026 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.
"""Script to run WebPageReplay Go files."""

from __future__ import annotations

import subprocess
import sys

import go_utils

if __name__ == "__main__":
    subprocess.check_call([str(go_utils.get_go_compiler_path()), "run"] +
                          sys.argv[1:])
