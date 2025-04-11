if [[ ! -f wpr ]]; then
  go build src/wpr.go
fi

# Bind to 0.0.0.0 to expose in all interfaces, including local network.
sudo ./wpr replay \
  --host=0.0.0.0 \
  --http_port=80 \
  --https_port=443 \
  --inject_scripts=deterministic.js \
  --serve_response_in_chronological_sequence \
  archive_phone.wprgo


