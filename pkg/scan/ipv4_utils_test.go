package scan

import (
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsFirstOrLastAddress_IPv6(t *testing.T) {
	ip := net.ParseIP("2001:db8::1")
	_, network, err := net.ParseCIDR("2001:db8::/32")
	require.NoError(t, err)

	result := IsFirstOrLastAddress(ip, network)
	require.False(t, result, "Expected false for non-first/last IPv6 address")
}

func TestInc_IPv6(t *testing.T) {
	ip := net.ParseIP("2001:db8::1")
	Inc(ip)
	require.Equal(t, "2001:db8::2", ip.String(), "Expected IPv6 address to increment correctly")
}

func TestGenerateIPsFromCIDR_LargeCIDR(t *testing.T) {
	t.Parallel()

	ips, err := GenerateIPsFromCIDR("192.168.0.0/16")
	require.NoError(t, err)
	require.NotNil(t, ips, "Expected IPs to be generated, but got nil error: %v", err)

	// In test mode, should return limited set
	require.Len(t, ips, 1000, "Expected limited number of IPs for large range in test mode")

	// Verify IPs are valid and unique
	seen := make(map[string]bool)

	for _, ip := range ips {
		ipStr := ip.String()
		require.False(t, seen[ipStr], "Found duplicate IP: %s", ipStr)
		seen[ipStr] = true

		// Verify it's a valid IPv4 address
		require.NotNil(t, ip.To4(), "Invalid IPv4 address: %s", ipStr)

		// Verify it starts with correct prefix
		require.True(t, strings.HasPrefix(ipStr, "192.168."),
			"IP %s doesn't have correct prefix", ipStr)
	}
}

func TestGenerateIPsFromCIDR_SmallRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cidr      string
		wantCount int
		wantErr   bool
	}{
		{
			name:      "valid /30 network",
			cidr:      "192.168.1.0/30",
			wantCount: 2, // Excluding network and broadcast addresses
			wantErr:   false,
		},
		{
			name:      "invalid CIDR",
			cidr:      "invalid",
			wantCount: 0,
			wantErr:   true,
		},
		{
			name:      "single IP (/32)",
			cidr:      "192.168.1.1/32",
			wantCount: 1,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ips, err := GenerateIPsFromCIDR(tt.cidr)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, ips, "Expected IPs to be generated, but got nil error: %v", err)
			require.Len(t, ips, tt.wantCount)

			// Verify IPs are valid
			if len(ips) > 0 {
				for _, ip := range ips {
					require.NotNil(t, ip.To4())
				}
			}
		})
	}
}

func TestIsFirstOrLastAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ip      string
		network string
		want    bool
	}{
		{
			name:    "network address",
			ip:      "192.168.1.0",
			network: "192.168.1.0/24",
			want:    true,
		},
		{
			name:    "broadcast address",
			ip:      "192.168.1.255",
			network: "192.168.1.0/24",
			want:    true,
		},
		{
			name:    "regular address",
			ip:      "192.168.1.100",
			network: "192.168.1.0/24",
			want:    false,
		},
		{
			name:    "IPv6 address",
			ip:      "2001:db8::1",
			network: "2001:db8::/32",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ip := net.ParseIP(tt.ip)
			_, network, err := net.ParseCIDR(tt.network)
			require.NoError(t, err)

			got := IsFirstOrLastAddress(ip, network)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInc(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ip   net.IP
		want net.IP
	}{
		{
			name: "normal increment",
			ip:   net.ParseIP("192.168.1.1"),
			want: net.ParseIP("192.168.1.2"),
		},
		{
			name: "increment with rollover",
			ip:   net.ParseIP("192.168.1.255"),
			want: net.ParseIP("192.168.2.0"),
		},
		{
			name: "IPv6 address",
			ip:   net.ParseIP("2001:db8::1"),
			want: net.ParseIP("2001:db8::2"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ipCopy := make(net.IP, len(tt.ip))
			copy(ipCopy, tt.ip)

			Inc(ipCopy)
			assert.Equal(t, tt.want.String(), ipCopy.String())
		})
	}
}
