#!/bin/bash

echo 'Ensuring dnsmasq installed'
which brew &>/dev/null
brew_missing=$?
if [[ "$brew_missing" -eq 0 ]]; then
  export PATH="$(brew --prefix)/bin:$(brew --prefix)/sbin:$PATH"
fi
which dnsmasq &>/dev/null
if [[ $? -ne 0 ]]; then
  echo 'dnsmasq not installed, installing'
  if [[ "$(uname -s)" == 'Linux' ]]; then
    sudo apt install dnsmasq
  else
    if [[ "$brew_missing" -ne 0 ]]; then
      echo 'Error: install homebrew first'
      exit 1
    fi
    brew install dnsmasq
  fi
fi
DNSMASQ_PATH="$(which dnsmasq)"

echo 'Ensuring no beyondcorp-dns'
beyondcorp_pid=$(ps aux | grep -v grep | grep 'sbin/beyondcorp-dns' | awk '{print $2}')
if [ -n "$beyondcorp_pid" ]; then
  echo 'Found beyondcorp-dns, killing'
  sudo kill $beyondcorp_pid
  sleep 2
fi

echo "Running: MAKE THIS THE ONLY DNS SERVER ON YOUR DEVICE $(./get_ip.sh)"
printf "no-resolv\naddress=/#/$(./get_ip.sh)\n" | sudo "$DNSMASQ_PATH" -d -C -
