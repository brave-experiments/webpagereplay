if [[ "$(uname -s)" == 'Darwin' ]]; then
  ipconfig getifaddr en0
else
  hostname -I | awk '{print $1}'
fi
