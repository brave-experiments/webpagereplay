set -e

if [[ ! -f wpr ]]; then
  go build src/wpr.go
fi

# Bind to 0.0.0.0 to expose in all interfaces, including local network.
# Remove the ecdsa_{cert,key}.pem to avoid problems.
sudo ./wpr replay \
  --host=0.0.0.0 \
  --http_port=80 \
  --https_port=443 \
  --https_cert_file=wpr_cert.pem \
  --https_key_file=wpr_key.pem \
  --inject_scripts=deterministic.js \
  --serve_response_in_chronological_sequence \
  new_archive_phone.wprgo
