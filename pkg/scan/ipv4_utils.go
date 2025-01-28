package scan

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// IPRange represents a range of IP addresses.
type IPRange struct {
	Start net.IP
	End   net.IP
}

// ParseIPSpec parses various IP specification formats and returns a slice of IPRange.
// Supported formats:
// - CIDR (192.168.1.0/24)
// - Single IP (192.168.1.1)
// - IP Range (192.168.1.1-192.168.1.10 or 192.168.1.1-10)
func ParseIPSpec(spec string) ([]IPRange, error) {
	// Split multiple specs by comma
	specs := strings.Split(spec, ",")
	var ranges []IPRange

	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}

		// Try parsing as CIDR
		if strings.Contains(s, "/") {
			r, err := parseCIDR(s)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %s: %w", s, err)
			}
			ranges = append(ranges, r)
			continue
		}

		// Try parsing as range
		if strings.Contains(s, "-") {
			r, err := parseIPRange(s)
			if err != nil {
				return nil, fmt.Errorf("invalid IP range %s: %w", s, err)
			}
			ranges = append(ranges, r...)
			continue
		}

		// Try parsing as single IP
		ip := net.ParseIP(s)
		if ip == nil {
			return nil, fmt.Errorf("invalid IP address: %s", s)
		}
		ranges = append(ranges, IPRange{Start: ip, End: ip})
	}

	return ranges, nil
}

// parseCIDR parses a CIDR notation string and returns an IPRange.
func parseCIDR(cidr string) (IPRange, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return IPRange{}, err
	}

	// Calculate the last IP in the network
	mask := binary.BigEndian.Uint32(ipnet.Mask)
	start := binary.BigEndian.Uint32(ip.To4())
	end := (start & mask) | (^mask)

	return IPRange{
		Start: net.IPv4(byte(start>>24), byte(start>>16), byte(start>>8), byte(start)),
		End:   net.IPv4(byte(end>>24), byte(end>>16), byte(end>>8), byte(end)),
	}, nil
}

// parseIPRange parses an IP range string and returns a slice of IPRange.
// Supports formats:
// - Full range: 192.168.1.1-192.168.1.10
// - Short range: 192.168.1.1-10
func parseIPRange(rangeStr string) ([]IPRange, error) {
	parts := strings.Split(rangeStr, "-")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid range format: %s", rangeStr)
	}

	start := net.ParseIP(strings.TrimSpace(parts[0]))
	if start == nil {
		return nil, fmt.Errorf("invalid start IP: %s", parts[0])
	}

	// Handle short format (192.168.1.1-10)
	endStr := strings.TrimSpace(parts[1])
	var end net.IP
	if !strings.Contains(endStr, ".") {
		// Parse the last octet
		lastOctet, err := strconv.Atoi(endStr)
		if err != nil || lastOctet < 0 || lastOctet > 255 {
			return nil, fmt.Errorf("invalid end IP octet: %s", endStr)
		}

		// Copy the first three octets from start
		end = make(net.IP, len(start))
		copy(end, start)
		end[len(end)-1] = byte(lastOctet)
	} else {
		end = net.ParseIP(endStr)
		if end == nil {
			return nil, fmt.Errorf("invalid end IP: %s", endStr)
		}
	}

	// Ensure IPs are in correct order
	if bytes.Compare(start, end) > 0 {
		start, end = end, start
	}

	return []IPRange{{Start: start, End: end}}, nil
}

// GenerateIPs generates a slice of IP addresses from a slice of IPRange.
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

// ParseIPSpecFromStrings parses multiple IP specifications from a slice of strings.
func ParseIPSpecFromStrings(specs []string) ([]IPRange, error) {
	var allRanges []IPRange

	for _, spec := range specs {
		ranges, err := ParseIPSpec(spec)
		if err != nil {
			return nil, err
		}
		allRanges = append(allRanges, ranges...)
	}

	return allRanges, nil
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
