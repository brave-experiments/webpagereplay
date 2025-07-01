#!/usr/bin/env bash

set -e

phone_targets=(
    "edition.cnn.com:/2024/04/21/china/china-spy-agency-public-profile-intl-hnk/index.html:cnn"
    "www.amazon.co.uk:/NIVEA-Suncream-Spray-Protect-Moisture/dp/B001B0OJXM:amazon"
    "en.m.wikipedia.org:/wiki/Taylor_Swift:wiki"
    "www.globo.com:/:globo"
    "www.google.com:/search:google_search_phone"
)

cp archive_phone_deterministic.wprgo archive_phone_deterministic_injected.wprgo
for target in "${phone_targets[@]}"; do
    IFS=':' read -r host full_path script <<< "$target"

    echo "Injecting: Host=$host, Path=$full_path, Script=$script"

    script_file="$PWD/../crossbench/config/benchmark/loadline2/${script}_instrumentation.js"
    if [[ ! -f "$script_file" ]]; then
      echo f"script $script not found"
      exit 1
    fi

    echo "Injecting: Host=$host, Path=$full_path, Script=$script"
    go run src/httparchive.go inject \
      --host "$host" \
      --full_path "$full_path" \
      archive_phone_deterministic_injected.wprgo \
      tmp.wprgo \
      "$script_file"
    mv tmp.wprgo archive_phone_deterministic_injected.wprgo
done
echo "Success: Generated archive_phone_deterministic_injected.wprgo"

tablet_targets=(
    "edition.cnn.com:/2024/04/21/china/china-spy-agency-public-profile-intl-hnk/index.html:cnn"
    "www.amazon.co.uk:/NIVEA-Suncream-Spray-Protect-Moisture/dp/B001B0OJXM:amazon"
    "www.youtube.com:/watch:youtube"
    "docs.google.com:/document/d/13AWeOGqtSkfpPK7meqE_X-GQQggwx4JJ1vc0YGvKg34/edit:google_docs"
    "www.google.com:/search:google_search_tablet"
)

cp archive_tablet_deterministic.wprgo archive_tablet_deterministic_injected.wprgo
for target in "${tablet_targets[@]}"; do
    IFS=':' read -r host full_path script <<< "$target"


    script_file="$PWD/../crossbench/config/benchmark/loadline2/${script}_instrumentation.js"
    if [[ ! -f "$script_file" ]]; then
      echo f"script $script not found"
      exit 1
    fi

    echo "Injecting: Host=$host, Path=$full_path, Script=$script"
    go run src/httparchive.go inject \
      --host "$host" \
      --full_path "$full_path" \
      archive_tablet_deterministic_injected.wprgo \
      tmp.wprgo \
      "$script_file"
    mv tmp.wprgo archive_tablet_deterministic_injected.wprgo
done
echo "Success: Generated archive_tablet_deterministic_injected.wprgo"