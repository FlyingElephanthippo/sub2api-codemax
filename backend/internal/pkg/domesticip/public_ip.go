package domesticip

import (
	"net/netip"
	"strings"
)

// These special-use ranges are unsuitable as a persistent public client grant.
// IPv6 is additionally restricted to the currently allocated global-unicast
// space; mapped IPv4 is normalized before applying the IPv4 exclusions.
var excludedPrefixes = func() []netip.Prefix {
	raw := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
	}
	result := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		result = append(result, netip.MustParsePrefix(s))
	}
	return result
}()

func isPublicAddr(addr netip.Addr) bool {
	if !addr.IsValid() || addr.Zone() != "" {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	if addr.Is6() && !netip.MustParsePrefix("2000::/3").Contains(addr) {
		return false
	}
	for _, prefix := range excludedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func NormalizePublicIP(raw string) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil || !isPublicAddr(addr) {
		return "", ErrInvalidPublicIP
	}
	return addr.Unmap().String(), nil
}
