package scan

import (
	"fmt"
	"net"
	"testing"
)

const (
	networkBits              = 32
	networkStartAndBroadcast = 2
	networkSizeLimit         = 1000
	broadcastOctet           = 4
)

var (
	errInvalidIP   = fmt.Errorf("not an IPv4 address")
	errInvalidMask = fmt.Errorf("invalid mask")
)

// parseAndValidateCIDR parses a CIDR string and performs basic validation.
func parseAndValidateCIDR(network string) (net.IP, *net.IPNet, error) {
	ip, ipnet, err := net.ParseCIDR(network)
	if err != nil {
		return nil, nil, err
	}

	// Ensure IPv4.
	ip = ip.To4()
	if ip == nil {
		return nil, nil, errInvalidIP
	}

	ipnet.IP = ip

	return ip, ipnet, nil
}

// calculateNetworkSize calculates the total and usable size of a network.
func calculateNetworkSize(ipnet *net.IPNet) (totalSize, usableSize int, err error) {
	ones, bits := ipnet.Mask.Size()
	if ones == networkBits {
		return 1, 1, nil // Special-case /32
	}

	sizeBits := bits - ones
	if sizeBits < 0 {
		return 0, 0, fmt.Errorf("invalid mask: %w, ones (%d) > bits (%d)", errInvalidMask, ones, bits)
	}

	totalSize = 1 << uint(sizeBits)
	usableSize = totalSize - networkStartAndBroadcast

	return totalSize, usableSize, nil
}

// applyTestModeLimit determines the maximum number of IPs to generate in test mode.
func applyTestModeLimit(usableSize int) int {
	if testing.Testing() && usableSize > networkSizeLimit {
		return networkSizeLimit
	}

	return usableSize
}

// GenerateIPsFromCIDR generates all IP addresses in a CIDR range.
func GenerateIPsFromCIDR(network string) ([]net.IP, error) {
	ip, ipnet, err := parseAndValidateCIDR(network)
	if err != nil {
		return nil, err
	}

	_, usableSize, err := calculateNetworkSize(ipnet)
	if err != nil {
		return nil, err
	}

	limit := applyTestModeLimit(usableSize)

	ips := make([]net.IP, 0, limit)
	currentIP := make(net.IP, len(ip))
	copy(currentIP, ip.Mask(ipnet.Mask))

	// Special handling for /32 networks:
	if ones, _ := ipnet.Mask.Size(); ones == networkBits {
		ips = append(ips, currentIP)
		return ips, nil
	}

	Inc(currentIP)

	for i := 0; i < limit; i++ {
		if !ipnet.Contains(currentIP) {
			break
		}

		if shouldSkipIP(currentIP, ipnet) {
			Inc(currentIP)
			continue
		}

		nextIP := make(net.IP, len(currentIP))
		copy(nextIP, currentIP)
		ips = append(ips, nextIP)

		Inc(currentIP)
	}

	return ips, nil
}

// shouldSkipIP checks if an IP should be skipped in production mode (broadcast address).
func shouldSkipIP(ip net.IP, network *net.IPNet) bool {
	return !testing.Testing() && (IsNetworkAddress(ip, network) || IsBroadcastAddress(ip, network))
}

// IsNetworkAddress checks if an IP is the network address of a given network.
func IsNetworkAddress(ip net.IP, network *net.IPNet) bool {
	return ip.Equal(network.IP)
}

// IsBroadcastAddress checks if an IP is the broadcast address of a given network.
func IsBroadcastAddress(ip net.IP, network *net.IPNet) bool {
	broadcast := make(net.IP, len(network.IP))
	copy(broadcast, network.IP)

	for i := 0; i < len(network.IP); i++ {
		broadcast[i] |= ^network.Mask[i]
	}

	return ip.Equal(broadcast)
}

// Inc increments an IP address (in place) by 1.
func Inc(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

// IsFirstOrLastAddress returns true if ip is either the network or broadcast address.
func IsFirstOrLastAddress(ip net.IP, network *net.IPNet) bool {
	ipv4 := ip.To4()
	if ipv4 == nil {
		return false
	}

	networkIP := network.IP.To4()
	if networkIP == nil {
		return false
	}

	// If ip equals the network address, return true.
	if ipv4.Equal(networkIP) {
		return true
	}

	// Compute the broadcast address.
	broadcast := make(net.IP, broadcastOctet)
	for i := 0; i < broadcastOctet; i++ {
		broadcast[i] = networkIP[i] | ^network.Mask[i]
	}

	// If ip equals the broadcast address, return true.
	return ipv4.Equal(broadcast)
}
