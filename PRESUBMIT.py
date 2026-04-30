# Copyright 2017 The Chromium Authors. All rights reserved.
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.
"""Presubmit script for changes affecting webpagereplay.

See http://dev.chromium.org/developers/how-tos/depottools/presubmit-scripts
for more details about the presubmit API built into depot_tools.
"""

import pathlib

PRESUBMIT_VERSION = '2.0.0'
USE_PYTHON3 = True


def CheckGoTests(input_api, output_api):
    # All commands below are run from the src/ directory, this path is relative
    # to that.
    go_path = '../third_party/golang/bin/go'
    return input_api.RunTests([
        input_api.Command(
            name='webpagereplay package tests',
            cmd=[go_path, 'test', './webpagereplay'],
            kwargs={
                'cwd':
                str(pathlib.Path(input_api.PresubmitLocalPath()) / 'src')
            },
            message=output_api.PresubmitError),
        input_api.Command(
            name='wpr.go tests',
            cmd=[go_path, 'test', 'wpr.go', 'wpr_test.go'],
            kwargs={
                'cwd':
                str(pathlib.Path(input_api.PresubmitLocalPath()) / 'src')
            },
            message=output_api.PresubmitError),
        input_api.Command(
            name='httparchive tests',
            cmd=[go_path, 'test', 'httparchive.go', 'httparchive_test.go'],
            kwargs={
                'cwd':
                str(pathlib.Path(input_api.PresubmitLocalPath()) / 'src')
            },
            message=output_api.PresubmitError)
    ])


# The binary update check is divided in 2:
# 1. At submission time
#      Verifies the binaries in the JSON file match the submitted go code. This
#      ensures correctness, while avoiding the user having to regenerate the
#      binaries on every patchset upload.
# 2. At upload time
#      Verifies that if production go files were touched, *some* change to the
#      JSON file happened. This ensures the JSON is among the list of modified
#      files by the time the CL is approved, and so can go through one final
#      update without requiring a restamp on the CL. Ideally the only effect of
#      this check is one run of upload_new_binaries.py upon initial upload.
def CheckPrebuiltBinaryUpdatedOnCommit(input_api, output_api):
    # Restricting to "OnCommit" is not enough, dry-run runs `git cl presubmit`,
    # which exercises on "OnCommit" checks. Thus the additional check.
    if input_api.dry_run:
        return []

    cmd = ["scripts/upload_new_binaries.py", "--check-only"]
    if input_api.verbose:
        cmd.append("--verbose")
    return input_api.RunTests([
        input_api.Command(name="check prebuilt binaries updated",
                          cmd=cmd,
                          kwargs={'cwd': input_api.PresubmitLocalPath()},
                          message=output_api.PresubmitError)
    ])


# See comment in CheckPrebuiltBinaryUpdatedOnCommit().
def CheckPrebuiltBinaryUpdatedOnUpload(input_api, output_api):
    files = input_api.UnixLocalPaths()
    if (not any(f.endswith('binary_dependencies.json') for f in files) and any(
            f.endswith('.go') and not f.endswith('_test.go') for f in files)):
        return [
            output_api.PresubmitError(
                'You changed go files, but didn\'t run scripts/'
                'upload_new_binaries.py')
        ]

    return []

def CheckPanProjectChecks(input_api, output_api):
    # The code-owners plugin is not enabled on the webpagereplay gerrit host, so
    # owners_check is set to false to avoid a failure. Note that owners-approval
    # is still enforced in other manners.
    return input_api.canned_checks.PanProjectChecks(input_api,
                                                    output_api,
                                                    owners_check=False)


def CheckPythonAndJavascriptFormat(input_api, output_api):
    return input_api.canned_checks.CheckPatchFormatted(
        input_api,
        output_api,
        check_clang_format=True,
        check_js=True,
        check_python=True,
        result_factory=output_api.PresubmitError)


def CheckGoFormat(input_api, output_api):
    return input_api.RunTests([
        input_api.Command(name='Checking go format',
                          cmd=['scripts/check_go_format.py'],
                          kwargs={'cwd': input_api.PresubmitLocalPath()},
                          message=output_api.PresubmitError)
    ])


def CheckRuff(input_api, output_api):
    return input_api.RunTests([
        input_api.Command(name='Checking ruff format',
                          cmd=[
                              'vpython3', '-m', 'ruff', 'check', '--select',
                              'E,F,W,B,I,UP'
                          ],
                          kwargs={'cwd': input_api.PresubmitLocalPath()},
                          message=output_api.PresubmitError)
    ])
