#!/usr/bin/env lucicfg
# Copyright 2026 The Chromium Authors
# Use of this source code is governed by a BSD-style license that can be
# found in the LICENSE file.

# See https://chromium.googlesource.com/infra/luci/luci-go/+/HEAD/lucicfg/doc/README.md
# for information on starlark/lucicfg

lucicfg.check_version("1.30.0", "Please update depot_tools")

lucicfg.config(
    config_dir = "generated",
    fail_on_warnings = True,
)

luci.project(
    name = "webpagereplay",
    buildbucket = "cr-buildbucket.appspot.com",
    logdog = "luci-logdog.appspot.com",
    milo = "luci-milo.appspot.com",
    notify = "luci-notify.appspot.com",
    swarming = "chromium-swarm.appspot.com",
    acls = [
        acl.entry(
            roles = [
                acl.PROJECT_CONFIGS_READER,
                acl.LOGDOG_READER,
                acl.BUILDBUCKET_READER,
                acl.SCHEDULER_READER,
            ],
            groups = "all",
        ),
        acl.entry(
            roles = acl.CQ_COMMITTER,
            # TODO(crbug.com/495366518): Create this or reuse another group.
            groups = "project-webpagereplay-committers",
        ),
        acl.entry(
            roles = acl.CQ_DRY_RUNNER,
            # TODO(crbug.com/495366518): Create this or reuse another group.
            groups = "project-webpagereplay-tryjob-access",
        ),
    ],
)

luci.cq_group(
    name = "cq",
    watch = [cq.refset(repo = "https://chromium.googlesource.com/webpagereplay")],
    retry_config = cq.RETRY_ALL_FAILURES,
)

luci.bucket(
    name = "try",
    acls = [
        acl.entry(
            roles = acl.BUILDBUCKET_TRIGGERER,
            groups = "project-webpagereplay-tryjob-access",
        ),
    ],
)

luci.recipe(
    name = "presubmit",
    cipd_package = "infra/recipe_bundles/chromium.googlesource.com/chromium/tools/build",
    cipd_version = "refs/heads/main",
)

luci.builder(
    name = "linux-presubmit",
    bucket = "try",
    executable = "presubmit",
    dimensions = {
        "os": "Ubuntu-22.04",
        "cpu": "x86-64",
        "pool": "luci.flex.try",
    },
    properties = {
        "repo_name": "webpagereplay",
        "runhooks": True,
    },
    # TODO(crbug.com/495366518): Create a dedicated service account.
    service_account = "catapult-try-builder@chops-service-accounts.iam.gserviceaccount.com",
)

luci.cq_tryjob_verifier(
    builder = "linux-presubmit",
    cq_group = "cq",
)
