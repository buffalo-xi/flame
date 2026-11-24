package xregistry

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// ResolveAdvertise works out the address other services should dial.
//
// serviceAddr, when set, is "host" or "host:port" and wins over everything derived. Otherwise the
// address follows listen: a concrete bind address is used as-is, while a wildcard bind (empty,
// 0.0.0.0, ::) falls back to iface and then to the hostname's first non-loopback IPv4 address.
// The port always follows listen unless serviceAddr carries one.
//
// Why listen alone is not enough: a wildcard bind says nothing about which address is reachable
// from another machine. An address that is wrong here registers "successfully" and then probes as
// critical forever — the failure surfaces in the catalog, not in the config.
func ResolveAdvertise(serviceAddr, listen, iface string) (string, int, error) {
	listenHost, listenPort, err := net.SplitHostPort(listen)
	if err != nil {
		return "", 0, fmt.Errorf("xregistry: listen %q: %w", listen, err)
	}
	port, err := strconv.Atoi(listenPort)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("xregistry: listen %q has no usable port", listen)
	}

	if serviceAddr != "" {
		host, explicitPort, err := splitAdvertise(serviceAddr)
		if err != nil {
			return "", 0, fmt.Errorf("xregistry: service_addr %q: %w", serviceAddr, err)
		}
		if explicitPort > 0 {
			port = explicitPort
		}
		if host != "" && !isWildcardHost(host) {
			return host, port, nil
		}
	}

	// The listener already names a concrete address: that is the address, no guessing needed.
	if listenHost != "" && !isWildcardHost(listenHost) {
		return listenHost, port, nil
	}

	host, err := deriveHost(iface)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

// splitAdvertise accepts "host", "host:port" and the bracketed IPv6 form. A bare IPv6 literal has
// no port and is returned as the host.
func splitAdvertise(v string) (string, int, error) {
	if h, p, err := net.SplitHostPort(v); err == nil {
		port, perr := strconv.Atoi(p)
		if perr != nil || port <= 0 || port > 65535 {
			return "", 0, fmt.Errorf("port %q is not a usable port number", p)
		}
		return h, port, nil
	}
	// No port part: either a hostname/IPv4 or a bare IPv6 literal, which SplitHostPort rejects
	// without brackets.
	host := strings.Trim(v, "[]")
	if host == "" {
		return "", 0, fmt.Errorf("empty host")
	}
	return host, 0, nil
}

// IsLoopbackHost reports whether host addresses only this machine.
func IsLoopbackHost(host string) bool {
	h := strings.Trim(strings.TrimSpace(host), "[]")
	if h == "" || strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// IsLocalRegistry reports whether the registry address points at this machine. An empty address
// means the consul client's own default, which also honours CONSUL_HTTP_ADDR.
func IsLocalRegistry(address string) bool {
	if address == "" {
		address = os.Getenv("CONSUL_HTTP_ADDR")
	}
	address = strings.TrimPrefix(address, "http://")
	address = strings.TrimPrefix(address, "https://")
	host := address
	if h, _, err := net.SplitHostPort(address); err == nil {
		host = h
	}
	return IsLoopbackHost(host)
}

// deriveHost picks an address for a wildcard bind: the named interface first, then the hostname,
// then the first physical interface with an IPv4 address.
func deriveHost(iface string) (string, error) {
	if iface != "" {
		return interfaceIPv4(iface)
	}

	if name, err := os.Hostname(); err == nil && name != "" {
		if addrs, err := net.LookupHost(name); err == nil {
			for _, a := range addrs {
				if ip := net.ParseIP(a); ip != nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
					return ip.String(), nil
				}
			}
		}
	}

	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("xregistry: no advertised address: %w", err)
	}
	for _, i := range interfaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Skip the virtual interfaces a container/VM host is full of: their addresses are
		// reachable only from inside this machine, which is exactly the mistake this function
		// exists to avoid.
		if isVirtualInterface(i.Name) {
			continue
		}
		if ip := firstIPv4(i); ip != "" {
			return ip, nil
		}
	}

	return "", fmt.Errorf("xregistry: listen binds a wildcard and no advertised address could be derived " +
		"(advertise_iface, the hostname and every interface were unusable); set registry.service_addr explicitly")
}

func interfaceIPv4(name string) (string, error) {
	i, err := net.InterfaceByName(name)
	if err != nil {
		return "", fmt.Errorf("xregistry: advertise_iface %q: %w", name, err)
	}
	if ip := firstIPv4(*i); ip != "" {
		return ip, nil
	}
	return "", fmt.Errorf("xregistry: advertise_iface %q has no non-loopback IPv4 address", name)
}

func firstIPv4(i net.Interface) string {
	addrs, err := i.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		return ip.String()
	}
	return ""
}

func isWildcardHost(host string) bool {
	switch strings.Trim(host, "[]") {
	case "", "0.0.0.0", "::":
		return true
	}
	return false
}

func isVirtualInterface(name string) bool {
	// Heuristic, not a guarantee; it only has to keep the common container/VM bridges out of the
	// way. registry.service_addr overrides the result when the guess is wrong.
	for _, prefix := range []string{"docker", "br-", "veth", "virbr", "vmnet", "cni", "flannel", "tun", "tap", "utun"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
