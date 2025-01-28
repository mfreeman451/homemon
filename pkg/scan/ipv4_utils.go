package scan

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
)

// IPRange represents a range of IP addresses.
type IPRange struct {
	Start net.IP
	End   net.IP
}

// inc increments an IP address by one.
func inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// parseIPCIDR parses a CIDR range and returns a slice of IP addresses
func parseIPCIDR(cidr string) ([]net.IP, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}

	var ips []net.IP
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); inc(ip) {
		nextIP := make(net.IP, len(ip))
		copy(nextIP, ip)
		ips = append(ips, nextIP)
	}

	// Remove network and broadcast addresses for non-/32 networks
	if len(ips) > 2 && ip.To4() != nil && !strings.HasSuffix(cidr, "/32") {
		ips = ips[1 : len(ips)-1]
	}

	return ips, nil
}

// GenerateIPsFromCIDR generates all IP addresses in a CIDR range.
func GenerateIPsFromCIDR(network string) ([]net.IP, error) {
	ip, ipnet, err := net.ParseCIDR(network)
	if err != nil {
		return nil, err
	}

	var ips []net.IP

	for i := ip.Mask(ipnet.Mask); ipnet.Contains(i); Inc(i) {
		// Skip network and broadcast addresses for IPv4
		if i.To4() != nil && IsFirstOrLastAddress(i, ipnet) {
			continue
		}

		newIP := make(net.IP, len(i))
		copy(newIP, i)

		ips = append(ips, newIP)
	}

	return ips, nil
}

// Inc increments an IP address.
func Inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++

		if ip[j] > 0 {
			break
		}
	}
}

// IsFirstOrLastAddress checks if the IP is the network or broadcast address.
func IsFirstOrLastAddress(ip net.IP, network *net.IPNet) bool {
	// Get the IP address as 4-byte slice for IPv4
	ipv4 := ip.To4()
	if ipv4 == nil {
		return false
	}

	// Check if it's the network address (first address)
	if ipv4.Equal(ip.Mask(network.Mask)) {
		return true
	}

	// Create broadcast address
	broadcast := make(net.IP, len(ipv4))
	for i := range ipv4 {
		broadcast[i] = ipv4[i] | ^network.Mask[i]
	}

	// Check if it's the broadcast address (last address)
	return ipv4.Equal(broadcast)
}

// GenerateIPs generates a slice of IPs from IPRange slice
func GenerateIPs(ranges []IPRange) []net.IP {
	var ips []net.IP

	for _, r := range ranges {
		start := binary.BigEndian.Uint32(r.Start.To4())
		end := binary.BigEndian.Uint32(r.End.To4())

		for i := start; i <= end; i++ {
			ip := make(net.IP, 4)
			binary.BigEndian.PutUint32(ip, i)
			ips = append(ips, ip)
		}
	}

	return ips
}

// ParseIPSpecFromStrings parses multiple IP specifications from a slice of strings
func ParseIPSpecFromStrings(specs []string) ([]IPRange, error) {
	var ranges []IPRange

	for _, spec := range specs {
		// Split by comma for multiple specifications
		for _, s := range strings.Split(spec, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}

			r, err := parseIPSpec(s)
			if err != nil {
				return nil, fmt.Errorf("failed to parse IP spec %s: %w", s, err)
			}
			ranges = append(ranges, r...)
		}
	}

	return ranges, nil
}

// parseIPSpec parses either CIDR or IP range format.
// For single IPs, converts them to /32 CIDR.
func parseIPSpec(spec string) ([]IPRange, error) {
	// Handle IP range format (e.g., 192.168.1.1-192.168.1.10 or 192.168.1.1-10)
	if strings.Contains(spec, "-") {
		return parseIPRange(spec)
	}

	// If it's a CIDR or single IP, convert to range
	cidrSpec := spec
	if !strings.Contains(spec, "/") {
		// Convert single IP to /32 CIDR
		cidrSpec = spec + "/32"
	}

	ipRange, err := parseCIDR(cidrSpec)
	if err != nil {
		return nil, err
	}

	return []IPRange{ipRange}, nil
}

// parseCIDR converts a CIDR notation to an IPRange
func parseCIDR(cidr string) (IPRange, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return IPRange{}, err
	}

	// Calculate the last IP in the network
	mask := binary.BigEndian.Uint32(ipnet.Mask)
	start := binary.BigEndian.Uint32(ip.To4())
	end := (start & mask) | (^mask)

	startIP := make(net.IP, 4)
	endIP := make(net.IP, 4)
	binary.BigEndian.PutUint32(startIP, start)
	binary.BigEndian.PutUint32(endIP, end)

	return IPRange{Start: startIP, End: endIP}, nil
}

// parseIPRange parses an IP range string into IPRange(s)
// Supports formats:
// - Full range: 192.168.1.1-192.168.1.10
// - Short range: 192.168.1.1-10
func parseIPRange(ipRange string) ([]IPRange, error) {
	parts := strings.Split(ipRange, "-")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid range format: %s", ipRange)
	}

	startIP := net.ParseIP(strings.TrimSpace(parts[0]))
	if startIP == nil || startIP.To4() == nil {
		return nil, fmt.Errorf("invalid start IP: %s", parts[0])
	}

	var endIP net.IP
	endStr := strings.TrimSpace(parts[1])

	if !strings.Contains(endStr, ".") {
		// Short format: use last octet only
		octets := strings.Split(startIP.String(), ".")
		lastOctet := endStr
		endIP = net.ParseIP(fmt.Sprintf("%s.%s.%s.%s",
			octets[0], octets[1], octets[2], lastOctet))
		if endIP == nil {
			return nil, fmt.Errorf("invalid end IP octet: %s", endStr)
		}
	} else {
		// Full IP format
		endIP = net.ParseIP(endStr)
		if endIP == nil || endIP.To4() == nil {
			return nil, fmt.Errorf("invalid end IP: %s", endStr)
		}
	}

	// Ensure correct order
	startInt := binary.BigEndian.Uint32(startIP.To4())
	endInt := binary.BigEndian.Uint32(endIP.To4())
	if startInt > endInt {
		startIP, endIP = endIP, startIP
	}

	return []IPRange{{Start: startIP, End: endIP}}, nil
}
