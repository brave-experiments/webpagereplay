DNSMASQ_CONF="$(brew --prefix)/etc/dnsmasq.conf"
echo > "$DNSMASQ_CONF"
for domain in `cat domains.txt`; do
  # DO NOT SUBMIT: this doesn't really work; you need to answer with the
  # real IP of this machine.
  echo "address=/$domain/127.0.0.1" >> "$DNSMASQ_CONF"
done
