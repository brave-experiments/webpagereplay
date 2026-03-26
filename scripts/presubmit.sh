#!/usr/bin/env bash

echo "Running presubmit Go tests..." &&
  go test ./src/webpagereplay &&
  go test src/wpr.go src/wpr_test.go &&
  echo "All tests passed!"
