// Package enrich adds human meaning to addresses and ports: service names,
// MAC vendors, remembered DNS names and the organisation that owns an address.
package enrich

import (
	"fmt"
	"sync"
)

// Service describes what a connection is, as far as ports can tell.
type Service struct {
	Label  string
	Tunnel bool // VPN or overlay: the contents are opaque
}

type portKey struct {
	proto uint8
	port  uint16
}

const (
	tcp = 6
	udp = 17
)

var (
	svcMu    sync.RWMutex
	services = map[portKey]Service{
		{tcp, 20}: {"FTP", false}, {tcp, 21}: {"FTP", false}, {tcp, 22}: {"SSH", false}, {tcp, 23}: {"Telnet", false},
		{tcp, 25}: {"SMTP", false}, {tcp, 53}: {"DNS", false}, {tcp, 80}: {"HTTP", false}, {tcp, 110}: {"POP3", false},
		{tcp, 143}: {"IMAP", false}, {tcp, 179}: {"BGP", false}, {tcp, 443}: {"HTTPS", false}, {tcp, 445}: {"SMB", false},
		{tcp, 465}: {"SMTP", false}, {tcp, 548}: {"AFP", false}, {tcp, 554}: {"RTSP", false}, {tcp, 587}: {"SMTP", false},
		{tcp, 631}: {"IPP", false}, {tcp, 853}: {"DNS over TLS", false}, {tcp, 873}: {"rsync", false}, {tcp, 993}: {"IMAP", false},
		{tcp, 995}: {"POP3", false}, {tcp, 1194}: {"OpenVPN", true}, {tcp, 1723}: {"PPTP", true}, {tcp, 1883}: {"MQTT", false},
		{tcp, 1935}: {"RTMP", false}, {tcp, 2049}: {"NFS", false}, {tcp, 3306}: {"MySQL", false}, {tcp, 3389}: {"RDP", false},
		{tcp, 3478}: {"STUN", false}, {tcp, 5060}: {"SIP", false}, {tcp, 5222}: {"XMPP", false}, {tcp, 5223}: {"Apple Push", false},
		{tcp, 5228}: {"Google Push", false}, {tcp, 5349}: {"TURN", false}, {tcp, 5432}: {"PostgreSQL", false},
		{tcp, 5900}: {"VNC", false}, {tcp, 6379}: {"Redis", false}, {tcp, 8080}: {"HTTP", false}, {tcp, 8096}: {"Jellyfin", false},
		{tcp, 8291}: {"WinBox", false}, {tcp, 8443}: {"HTTPS", false}, {tcp, 8728}: {"RouterOS API", false},
		{tcp, 8729}: {"RouterOS API", false}, {tcp, 8883}: {"MQTT", false}, {tcp, 9100}: {"Printing", false},
		{tcp, 25565}: {"Minecraft", false}, {tcp, 32400}: {"Plex", false},

		{udp, 53}: {"DNS", false}, {udp, 67}: {"DHCP", false}, {udp, 68}: {"DHCP", false}, {udp, 123}: {"NTP", false},
		{udp, 137}: {"NetBIOS", false}, {udp, 138}: {"NetBIOS", false}, {udp, 161}: {"SNMP", false}, {udp, 443}: {"QUIC", false},
		{udp, 500}: {"IPsec VPN", true}, {udp, 514}: {"Syslog", false}, {udp, 546}: {"DHCPv6", false}, {udp, 547}: {"DHCPv6", false},
		{udp, 1194}: {"OpenVPN", true}, {udp, 1701}: {"L2TP", true}, {udp, 1900}: {"SSDP", false}, {udp, 2055}: {"NetFlow", false},
		{udp, 3478}: {"STUN", false}, {udp, 3479}: {"STUN", false}, {udp, 3480}: {"STUN", false}, {udp, 3481}: {"STUN", false},
		{udp, 3702}: {"WS-Discovery", false}, {udp, 4500}: {"IPsec VPN", true}, {udp, 5060}: {"SIP", false},
		{udp, 5353}: {"mDNS", false}, {udp, 5355}: {"LLMNR", false}, {udp, 9993}: {"ZeroTier", true},
		{udp, 13231}: {"WireGuard", true}, {udp, 19302}: {"STUN", false}, {udp, 41641}: {"Tailscale", true},
		{udp, 51820}: {"WireGuard", true},
	}
	protocols = map[uint8]Service{
		1: {"ICMP", false}, 2: {"IGMP", false}, 4: {"IP-in-IP", true}, 41: {"6in4", true}, 47: {"GRE", true},
		50: {"IPsec VPN", true}, 51: {"IPsec VPN", true}, 58: {"ICMPv6", false}, 89: {"OSPF", false},
	}
)

// RegisterPort adds or replaces a port mapping, for example the router's own
// WireGuard listen port.
func RegisterPort(proto uint8, port uint16, label string, tunnel bool) {
	svcMu.Lock()
	defer svcMu.Unlock()
	services[portKey{proto, port}] = Service{label, tunnel}
}

// LookupService names a connection. The remote port is checked first because
// local devices usually initiate towards a well-known port.
func LookupService(proto uint8, localPort, remotePort uint16) Service {
	if s, ok := protocols[proto]; ok {
		return s
	}
	svcMu.RLock()
	defer svcMu.RUnlock()
	if s, ok := services[portKey{proto, remotePort}]; ok {
		return s
	}
	if s, ok := services[portKey{proto, localPort}]; ok {
		return s
	}
	switch proto {
	case tcp:
		return Service{"TCP other", false}
	case udp:
		return Service{"UDP other", false}
	}
	return Service{fmt.Sprintf("IP proto %d", proto), false}
}

// ProtoName returns the short name of an IP protocol number.
func ProtoName(p uint8) string {
	switch p {
	case tcp:
		return "tcp"
	case udp:
		return "udp"
	case 1:
		return "icmp"
	case 58:
		return "icmpv6"
	case 47:
		return "gre"
	case 50:
		return "esp"
	}
	return fmt.Sprintf("%d", p)
}
