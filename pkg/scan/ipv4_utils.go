package scan

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
)

// IPRange represents a range of IP addresses.
type IPRange struct {
	Start net.IP
	End   net.IP
}

// GenerateIPsFromSpec generates IPs from either a CIDR or range specification
func GenerateIPsFromSpec(spec string) ([]net.IP, error) {
	if strings.Contains(spec, "-") {
		// Handle IP range format
		ranges, err := ParseIPRange(spec)
		if err != nil {
			return nil, fmt.Errorf("failed to parse IP range %s: %w", spec, err)
		}
		return GenerateIPsFromRange(ranges[0]), nil
	}

	// Handle CIDR format
	return GenerateIPsFromCIDR(spec)
}

// GenerateIPsFromCIDR generates all IP addresses in a CIDR range.
func GenerateIPsFromCIDR(network string) ([]net.IP, error) {
	ip, ipnet, err := net.ParseCIDR(network)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR address: %s (%w)", network, err)
	}

	var ips []net.IP
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); Inc(ip) {
		// Skip network and broadcast addresses for IPv4 (except for /32)
		if ip.To4() != nil && !strings.HasSuffix(network, "/32") {
			if IsFirstOrLastAddress(ip, ipnet) {
				continue
			}
		}
		newIP := make(net.IP, len(ip))
		copy(newIP, ip)
		ips = append(ips, newIP)
	}

	return ips, nil
}

// ParseIPRange parses an IP range string into IPRange(s)
func ParseIPRange(ipRange string) ([]IPRange, error) {
	parts := strings.Split(strings.TrimSpace(ipRange), "-")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid IP range format (expected x.x.x.x-y): %s", ipRange)
	}

	startIP := net.ParseIP(strings.TrimSpace(parts[0]))
	if startIP == nil || startIP.To4() == nil {
		return nil, fmt.Errorf("invalid start IP: %s", parts[0])
	}
	startIP = startIP.To4() // Convert to 4-byte form

	endIP := net.IP(nil)
	endStr := strings.TrimSpace(parts[1])

	if strings.Contains(endStr, ".") {
		// Full IP address
		endIP = net.ParseIP(endStr)
		if endIP == nil || endIP.To4() == nil {
			return nil, fmt.Errorf("invalid end IP: %s", endStr)
		}
		endIP = endIP.To4()
	} else {
		// Just the last octet
		lastOctet, err := strconv.Atoi(endStr)
		if err != nil || lastOctet < 0 || lastOctet > 255 {
			return nil, fmt.Errorf("invalid end octet (must be 0-255): %s", endStr)
		}

		endIP = make(net.IP, 4)
		copy(endIP, startIP)
		endIP[3] = byte(lastOctet)
	}

	// Verify they're in the same /24 subnet
	for i := 0; i < 3; i++ {
		if startIP[i] != endIP[i] {
			return nil, fmt.Errorf("IP range must stay within same /24 subnet: %s", ipRange)
		}
	}

	// Ensure start <= end
	if startIP[3] > endIP[3] {
		startIP, endIP = endIP, startIP
	}

	log.Printf("Parsed IP range: %s - %s", startIP, endIP)

	return []IPRange{{
		Start: startIP,
		End:   endIP,
	}}, nil
}

func GenerateIPsFromRange(r IPRange) []net.IP {
	ips := make([]net.IP, 0)
	start := int(r.Start[3])
	end := int(r.End[3])

	baseIP := make(net.IP, len(r.Start))
	copy(baseIP, r.Start)

	for octet := start; octet <= end; octet++ {
		ip := make(net.IP, len(baseIP))
		copy(ip, baseIP)
		ip[3] = byte(octet)
		ips = append(ips, ip)
	}

	log.Printf("Generated %d IPs in range %s - %s", len(ips), r.Start, r.End)
	return ips
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
