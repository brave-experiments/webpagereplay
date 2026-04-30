#!/usr/bin/env vpython3
# Copyright 2025 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

"""Script to build and upload WebPageReplay Go binaries to Cloud Storage.

It builds binaries for all supported platforms, computes their hashes,
and uploads them to Cloud Storage if they differ from the hashes recorded
in binary_dependencies.json.
"""

import argparse
import hashlib
import json
import logging
import os
import pathlib
import subprocess
import sys
import tempfile

_REPO_DIR = pathlib.Path(__file__).resolve().parents[1]
_SRC_DIR = _REPO_DIR / 'src'
_GO_COMPILER_PATH = _REPO_DIR / 'third_party' / 'golang' / 'bin' / 'go'
_SUPPORTED_PLATFORMS = (('win', 'x86'), ('mac', 'arm64'), ('mac', 'x86_64'),
                        ('linux', 'x86_64'), ('win', 'AMD64'),
                        ('linux', 'armv7l'), ('linux', 'aarch64'))
_CHECK_ONLY_FLAG = '--check-only'


# GOARCH in the build command expects values that differ from the keys in
# binary_dependencies.json. Changing the keys to match GOARCH would require
# touching all consumers of the JSON.
def _compute_go_arch(os_arch):
    # go build command recognizes 'amd64' but not 'x86_64', so we switch x86_64
    # to amd64 string here.
    # The two names can be used interchangbly, see:
    # https://wiki.debian.org/DebianAMD64Faq?action=recall&rev=65
    if os_arch == 'x86_64' or os_arch == 'AMD64':
        return 'amd64'

    if os_arch == 'x86':
        return '386'

    if os_arch == 'armv7l':
        return 'arm'

    if os_arch == 'aarch64':
        return 'arm64'

    if os_arch == 'mips':
        return 'mipsle'

    return os_arch


# GOOS in the build command expects values that differ from the keys in
# binary_dependencies.json. Changing the keys to match GOOS would require
# touching all consumers of the JSON.
def _compute_go_os(os_name):
    # go build command recognizes 'darwin' but not 'mac'.
    if os_name == 'mac':
        return 'darwin'

    if os_name == 'win':
        return 'windows'

    return os_name


def _run(cmd, env=None, stdout=None, stderr=None):
    if env is None:
        env = {}
    env_str = " ".join([f"{k}={v}" for k, v in env.items()])
    cmd_str = " ".join(cmd)
    logging.info(f'{env_str} {cmd_str}')
    subprocess.check_call(cmd,
                          env=os.environ.copy() | env,
                          stdout=stdout,
                          stderr=stderr)


def _build_go_binary(binary_name, os_name, os_arch, go_path_dir):
    out_dir = go_path_dir / f'{os_name}_{os_arch}'
    # exist_ok=True because there's more than one binary_name per (OS, arch).
    out_dir.mkdir(exist_ok=True)
    binary_file = out_dir / binary_name
    if os_name == 'win':
        binary_file = binary_file.with_suffix('.exe')
    _run(
        # -trimpath and -buildvcs=false are required for deterministic builds
        # (stable binary hash).
        [
            str(_GO_COMPILER_PATH), 'build', '-C',
            str(_SRC_DIR), '-trimpath', '-buildvcs=false', '-o',
            str(binary_file), f'{binary_name}.go'
        ],
        {
            'GOPATH': str(go_path_dir),
            'GOOS': _compute_go_os(os_name),
            'GOARCH': _compute_go_arch(os_arch),
            'CGO_ENABLED': '0',
        })
    return binary_file


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        _CHECK_ONLY_FLAG,
        action='store_true',
        help='Check if binaries are up to date without uploading')
    parser.add_argument('--verbose',
                        action='store_true',
                        help='Enable verbose logging')
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO if args.verbose else logging.ERROR)
    json_path = _REPO_DIR / 'scripts' / 'binary_dependencies.json'
    with open(json_path) as file:
        deps_data = json.load(file)
    with tempfile.TemporaryDirectory() as go_path_str:
        go_path_dir = pathlib.Path(go_path_str)
        repo_symlink_dir = go_path_dir / 'src' / 'go.chromium.org'
        repo_symlink_dir.mkdir(parents=True)
        (repo_symlink_dir / 'webpagereplay').symlink_to(_REPO_DIR)
        # `go get` prints useless "downloading..." to stderr, so that's why
        # that's the one set to DEVNULL here instead of stdout.
        # TODO(crbug.com/495366518): We still want real errors to be displayed
        # by default. Figure out a replacement.
        _run([str(_GO_COMPILER_PATH), 'get', '-C',
              str(_SRC_DIR), './...'],
             env={'GOPATH': str(go_path_dir)},
             stderr=None if args.verbose else subprocess.DEVNULL)
        for os_name, os_arch in _SUPPORTED_PLATFORMS:
            for binary_name in ('wpr', 'httparchive'):
                binary_file = _build_go_binary(binary_name, os_name, os_arch,
                                               go_path_dir)
                with open(binary_file, 'rb') as file:
                    out_hash = hashlib.sha1(file.read()).hexdigest()
                bin_key = f'{binary_name}_go'
                platform_key = f'{os_name}_{os_arch}'
                hash_key = 'cloud_storage_hash'
                if deps_data[bin_key][platform_key][hash_key] == out_hash:
                    continue

                if args.check_only:
                    logging.error(
                        f'{binary_name} outdated for {os_name} {os_arch}. Run '
                        f'{pathlib.Path(__file__).name} without '
                        f'{_CHECK_ONLY_FLAG} to update.')
                    return 1

                cmd = ['gsutil.py']
                if not args.verbose:
                    cmd.append('-q')
                cmd.extend([
                    'cp',
                    str(binary_file),
                    'gs://chromium-telemetry/binary_dependencies/'
                    f'{bin_key}_{out_hash}'
                ])
                _run(cmd)
                deps_data[bin_key][platform_key][hash_key] = out_hash

    if not args.check_only:
        with open(json_path, 'w') as file:
            json.dump(deps_data, file, indent=2)
            file.write('\n')

    return 0


if __name__ == '__main__':
    sys.exit(main())
