#!/usr/bin/env vpython3
# Copyright 2025 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile

_REPO_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_WPR_GO_DIR = os.path.join(_REPO_DIR, 'src')
_SUPPORTED_PLATFORMS = (('win', 'x86'), ('mac', 'arm64'), ('mac', 'x86_64'),
                        ('linux', 'x86_64'), ('win', 'AMD64'),
                        ('linux', 'armv7l'), ('linux', 'aarch64'))


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


# Returns whether the dependencies JSON is up-to-date once the function is done.
def _build_and_maybe_upload_go_binary(binary_name, os_name, os_arch,
                                      check_only):
    print(f'Build {binary_name} binary for OS {os_name}, ARCH: {os_arch}')
    try:
        # We want to build wpr go binaries from the local source. We do this by
        # making a temporary GOPATH that symlinks to our local directory.
        go_path_dir = tempfile.mkdtemp()
        repo_dir = os.path.join(go_path_dir, 'src/go.chromium.org')
        os.makedirs(repo_dir)
        os.symlink(_REPO_DIR, os.path.join(repo_dir, 'webpagereplay'))

        env = os.environ.copy()
        env['GOPATH'] = go_path_dir
        env['GOOS'] = _compute_go_os(os_name)
        env['GOARCH'] = _compute_go_arch(os_arch)
        env['CGO_ENABLED'] = '0'

        print(f'GOPATH={go_path_dir}')
        print(f'CWD={_WPR_GO_DIR}')

        get_cmd = ['go', 'get', '-d', './...']
        print(f'Running get command: {" ".join(get_cmd)}')
        subprocess.check_call(get_cmd, env=env, cwd=_WPR_GO_DIR)

        # Build in `go_path_dir`, so the binaries are deleted by the end.
        binary_file = (os.path.join(go_path_dir, f'{binary_name}.exe')
                       if os_name == 'win' else os.path.join(
                           go_path_dir, binary_name))
        build_cmd = [
            'go', 'build', '-v', '-trimpath', '-o', binary_file,
            f'{binary_name}.go'
        ]
        print(f'Running build command: {" ".join(build_cmd)}')
        subprocess.check_call(build_cmd, env=env, cwd=_WPR_GO_DIR)
    except:
        if go_path_dir:
            shutil.rmtree(go_path_dir)
        raise

    with open(binary_file, 'rb') as file:
        hash = hashlib.sha1(file.read()).hexdigest()
    json_path = os.path.join(_REPO_DIR, 'scripts', 'binary_dependencies.json')
    with open(json_path) as file:
        deps_data = json.load(file)
    bin_key = f'{binary_name}_go'
    platform_key = f'{os_name}_{os_arch}'
    if check_only:
        existing_hash = deps_data[bin_key][platform_key]['cloud_storage_hash']
        if existing_hash == hash:
            return True
        print(f'Expected hash {hash} for {binary_name}, got {existing_hash}')
        return False

    print(f'Upload {binary_name} dependency for OS {os_name}, ARCH: {os_arch}')
    subprocess.check_call([
        'gsutil.py', 'cp', binary_file,
        f'gs://chromium-telemetry/binary_dependencies/{bin_key}_{hash}'
    ])
    deps_data[bin_key][platform_key]['cloud_storage_hash'] = hash
    with open(json_path, 'w') as file:
        json.dump(deps_data, file, indent=2)
        file.write('\n')
    shutil.rmtree(go_path_dir)




def main():
    parser = argparse.ArgumentParser()
    parser.add_argument(
        '--check-only',
        action='store_true',
        help='Check if binaries are up to date without uploading')
    args = parser.parse_args()
    for os_name, os_arch in _SUPPORTED_PLATFORMS:
        for binary_name in ('wpr', 'httparchive'):
            up_to_date = _build_and_maybe_upload_go_binary(
                binary_name, os_name, os_arch, args.check_only)
            if args.check_only and not up_to_date:
                return 1
    return 0

if __name__ == '__main__':
    sys.exit(main())
