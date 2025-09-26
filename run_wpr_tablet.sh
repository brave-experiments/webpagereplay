set -e

go build src/wpr.go

# Bind to 0.0.0.0 to expose in all interfaces, including local network.
# Remove the ecdsa_{cert,key}.pem to avoid problems.
#
# Nuke --inject_scripts= to avoid Google Docs issue.
sudo ./wpr replay \
  --inject_scripts= \
  --host=0.0.0.0 \
  --http_port=80 \
  --https_port=443 \
  --https_cert_file=wpr_cert.pem \
  --https_key_file=wpr_key.pem \
  --rules_file=../crossbench/config/benchmark/loadline2/webpagereplay_script_injections_tablet.json \
  --serve_response_in_chronological_sequence \
  archive_tablet.wprgo
