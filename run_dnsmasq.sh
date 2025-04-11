brew -v &>/dev/null
if [[ $? -ne 0 ]]; then
  echo 'Install homebrew first'
  exit 1
fi

if [[ "$(uname -s)" == 'Darwin' ]]; then
  export PATH="$(brew --prefix)/bin:$(brew --prefix)/sbin:$PATH"
fi

dnsmasq -v &>/dev/null
if [[ $? -ne 0 ]]; then
  echo "Dnsmasq not installed, installing"
  if [[ "$(uname -s)" == 'Darwin' ]]; then
    brew install dnsmasq
  fi
fi
DNSMASQ_PATH="$(which dnsmasq)"

echo "MAKE THIS THE ONLY DNS SERVER ON YOUR DEVICE $(./get_ip.sh)"
sudo pkill beyondcorp-dns
echo "address=/#/$(./get_ip.sh)" | sudo "$DNSMASQ_PATH" -d -C -
