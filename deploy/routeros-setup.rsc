# RouterOS 7 setup for mikrotik-home-netflow-plus.
# Replace <COLLECTOR_IP>, <ROUTER_LAN_IP> and <STRONG_PASSWORD>, then paste into a terminal.
# Explained step by step in SETUP-MIKROTIK-ROUTER.md. The app's Status page
# shows this script with the addresses filled in.

# --- Flow export (IPFIX). 1m is the lowest active timeout RouterOS accepts. ---
/ip traffic-flow set enabled=yes active-flow-timeout=1m
/ip traffic-flow target add dst-address=<COLLECTOR_IP> port=2055 version=ipfix \
    src-address=<ROUTER_LAN_IP> v9-template-refresh=20 v9-template-timeout=1m

# --- Read-only API user, usable only from the collector ---
/user group add name=flowmon policy=read,api
/user add name=flowmon group=flowmon password=<STRONG_PASSWORD> address=<COLLECTOR_IP>/32

# --- Certificate so the API can be reached over TLS (api-ssl, port 8729) ---
/certificate add name=api-cert common-name=router.lan key-size=2048 days-valid=3650 \
    key-usage=key-cert-sign,crl-sign,digital-signature,key-encipherment,tls-server
/certificate sign api-cert
/ip service set api-ssl certificate=api-cert

# --- Recommended: keep the router clock right ---
# /system ntp client set enabled=yes servers=time.cloudflare.com
