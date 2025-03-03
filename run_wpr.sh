# Normally you would need --host=0.0.0.0 to bind on all interfaces and thus
# expose this server in the local network. But I need a separate frontend server
# anyways that redirects to 8000 or 8001 appropriately. That frontend server
# uses 0.0.0.0.
go run src/wpr.go replay \
  --http_port=8000 \
  --https_port=8001 \
  --inject_scripts=deterministic.js \
  --serve_response_in_chronological_sequence \
  archive_phone.wprgo


