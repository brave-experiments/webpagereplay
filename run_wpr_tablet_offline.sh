set -e

go build src/wpr.go

# Bind to 0.0.0.0 to expose in all interfaces, including local network.
# Remove the ecdsa_{cert,key}.pem to avoid problems.
#
# WARNING: Nuking --inject_scripts is required otherwise the Google Docs story
# doesn't work.
sudo ./wpr replay \
  --host=0.0.0.0 \
  --http_port=80 \
  --https_port=443 \
  --inject_scripts= \
  --https_cert_file=wpr_cert.pem \
  --https_key_file=wpr_key.pem \
  --serve_response_in_chronological_sequence \
  archive_tablet_deterministic_injected.wprgo
