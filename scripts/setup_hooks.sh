#!/usr/bin/env bash

# Sets up the shared Git hooks for this repository.

git config core.hooksPath .githooks &&
  echo "Git hooks successfully configured to use .githooks/"
