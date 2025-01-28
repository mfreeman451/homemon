// Package agent pkg/agent/sweep_service.go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mfreeman451/serviceradar/pkg/models"
	"github.com/mfreeman451/serviceradar/pkg/scan"
	"github.com/mfreeman451/serviceradar/pkg/sweeper"
	"github.com/mfreeman451/serviceradar/proto"
)

// SweepService implements sweeper.SweepService and provides network scanning capabilities.
type SweepService struct {
	scanner   scan.Scanner
	store     sweeper.Store
	processor sweeper.ResultProcessor
	mu        sync.RWMutex
	closed    chan struct{}
	config    *models.Config
}

func applyDefaultConfig(config *models.Config) *models.Config {
	// Ensure we have default sweep modes
	if len(config.SweepModes) == 0 {
		config.SweepModes = []models.SweepMode{
			models.ModeTCP,
			models.ModeICMP,
		}
	}

	// Set reasonable defaults
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Second
	}

	if config.Concurrency == 0 {
		config.Concurrency = 100
	}

	if config.ICMPCount == 0 {
		config.ICMPCount = 3
	}

	return config
}

func (s *SweepService) Start(ctx context.Context) error {
	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	// Do initial sweep
	if err := s.performSweep(ctx); err != nil {
		log.Printf("Initial sweep failed: %v", err)
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return nil
		case <-ticker.C:
			if err := s.performSweep(ctx); err != nil {
				log.Printf("Periodic sweep failed: %v", err)
			}
		}
	}
}

func (s *SweepService) generateTargets() ([]models.Target, error) {
	var allTargets []models.Target

	for _, network := range s.config.Networks {
		// First generate all IP addresses
		ips, err := scan.GenerateIPsFromCIDR(network)
		if err != nil {
			return nil, fmt.Errorf("failed to generate IPs for %s: %w", network, err)
		}

		// For each IP, create ICMP target if enabled
		if containsMode(s.config.SweepModes, models.ModeICMP) {
			for _, ip := range ips {
				allTargets = append(allTargets, models.Target{
					Host: ip.String(),
					Mode: models.ModeICMP,
				})
			}
		}

		// For each IP, create TCP targets for each port if enabled
		if containsMode(s.config.SweepModes, models.ModeTCP) {
			for _, ip := range ips {
				for _, port := range s.config.Ports {
					allTargets = append(allTargets, models.Target{
						Host: ip.String(),
						Port: port,
						Mode: models.ModeTCP,
					})
				}
			}
		}
	}

	log.Printf("Generated %d targets from %d networks (%d ports, modes: %v)",
		len(allTargets),
		len(s.config.Networks),
		len(s.config.Ports),
		s.config.SweepModes)

	return allTargets, nil
}

// Helper function to check if a mode is in the list of modes.
func containsMode(modes []models.SweepMode, mode models.SweepMode) bool {
	for _, m := range modes {
		if m == mode {
			return true
		}
	}

	return false
}

func (*SweepService) Name() string {
	return "network_sweep"
}

func (s *SweepService) performSweep(ctx context.Context) error {
	// Generate targets
	targets, err := s.generateTargets()
	if err != nil {
		return fmt.Errorf("failed to generate targets: %w", err)
	}

	log.Printf("Starting sweep with %d targets", len(targets))

	// Start the scan
	results, err := s.scanner.Scan(ctx, targets)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	// Process results as they come in
	for result := range results {
		// Process the result
		if err := s.processor.Process(&result); err != nil {
			log.Printf("Failed to process result: %v", err)
		}

		// Store the result
		if err := s.store.SaveResult(ctx, &result); err != nil {
			log.Printf("Failed to save result: %v", err)
		}
	}

	return nil
}

// NewSweepService now creates a persistent service with a single processor instance.
func NewSweepService(config *models.Config) (*SweepService, error) {
	// Apply default configuration
	if config.ICMPCount <= 0 {
		config.ICMPCount = 3 // Set reasonable default for ICMP ping count
	}

	// Create scanner with config settings
	scanner := scan.NewCombinedScanner(
		config.Timeout,
		config.Concurrency,
		config.ICMPCount,
	)

	// Create processor instance (which now handles both processing and storage)
	processor := sweeper.NewBaseProcessor()

	// Create an in-memory store that references the same processor if needed:
	store := sweeper.NewInMemoryStore(processor)

	service := &SweepService{
		scanner:   scanner,
		store:     store,
		processor: processor,
		config:    config,
		closed:    make(chan struct{}),
	}

	return service, nil
}

func (s *SweepService) Stop() error {
	close(s.closed)

	return s.scanner.Stop()
}

// identifyService maps common port numbers to service names.
/*
func identifyService(port int) string {
	commonPorts := map[int]string{
		21:   "FTP",
		22:   "SSH",
		23:   "Telnet",
		25:   "SMTP",
		53:   "DNS",
		80:   "HTTP",
		110:  "POP3",
		143:  "IMAP",
		443:  "HTTPS",
		3306: "MySQL",
		5432: "PostgreSQL",
		6379: "Redis",
		8080: "HTTP-Alt",
		8443: "HTTPS-Alt",
		9000: "Kadcast", // Dusk network port
	}

	if service, ok := commonPorts[port]; ok {
		return service
	}

	return fmt.Sprintf("Port-%d", port)
}
*/

func (s *SweepService) GetStatus(ctx context.Context) (*proto.StatusResponse, error) {
	summary, err := s.processor.GetSummary(ctx)
	if err != nil {
		log.Printf("Error getting sweep summary: %v", err)
		return nil, fmt.Errorf("failed to get sweep summary: %w", err)
	}

	// For each host in the summary, properly format ICMP status
	for i := range summary.Hosts {
		host := &summary.Hosts[i]
		if host.ICMPStatus != nil {
			// Ensure RoundTrip is properly set in nanoseconds
			if host.ICMPStatus.Available {
				host.ResponseTime = host.ICMPStatus.RoundTrip
			}
		}
	}

	// Convert to JSON for the message field
	data := struct {
		Network        string              `json:"network"`
		TotalHosts     int                 `json:"total_hosts"`
		AvailableHosts int                 `json:"available_hosts"`
		LastSweep      int64               `json:"last_sweep"`
		Ports          []models.PortCount  `json:"ports"`
		Hosts          []models.HostResult `json:"hosts"`
	}{
		Network:        strings.Join(s.config.Networks, ","),
		TotalHosts:     summary.TotalHosts,
		AvailableHosts: summary.AvailableHosts,
		LastSweep:      summary.LastSweep,
		Ports:          summary.Ports,
		Hosts:          summary.Hosts,
	}

	statusJSON, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal sweep status: %w", err)
	}

	return &proto.StatusResponse{
		Available:    true,
		Message:      string(statusJSON),
		ServiceName:  "network_sweep",
		ServiceType:  "sweep",
		ResponseTime: time.Since(time.Unix(summary.LastSweep, 0)).Nanoseconds(),
	}, nil
}

// UpdateConfig updates the sweep configuration.
func (s *SweepService) UpdateConfig(config *models.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Apply default configuration
	config = applyDefaultConfig(config)
	s.config = config

	return nil
}

// Close implements io.Closer.
func (s *SweepService) Close() error {
	return s.Stop()
}
