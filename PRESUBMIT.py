# Copyright 2017 The Chromium Authors. All rights reserved.
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

"""Presubmit script for changes affecting web_page_replay_go/.

See http://dev.chromium.org/developers/how-tos/depottools/presubmit-scripts
for more details about the presubmit API built into depot_tools.
"""

import os

PRESUBMIT_VERSION = '2.0.0'
USE_PYTHON3 = True


def CheckGoTests(input_api, output_api):
  cmd_name = 'WebPageReplay go tests'
  if input_api.verbose:
    print(f'Running {cmd_name}')
  test_cmd = input_api.Command(
      name=cmd_name,
      cmd=['go', 'test', './webpagereplay'],
      kwargs={'cwd': os.path.join(input_api.PresubmitLocalPath(), 'src')},
      message=output_api.PresubmitError)
  return input_api.RunTests([test_cmd])
