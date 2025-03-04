DNSMASQ_CONF="$(brew --prefix)/etc/dnsmasq.conf"
echo > "$DNSMASQ_CONF"
for domain in `cat domains.txt`; do
  echo "address=/$domain/127.0.0.1" >> "$DNSMASQ_CONF"
done
