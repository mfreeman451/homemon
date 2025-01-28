// Package sweeper pkg/sweeper/sweeper.go
package sweeper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mfreeman451/serviceradar/pkg/models"
	"github.com/mfreeman451/serviceradar/pkg/scan"
)

var (
	errInvalidSweepMode = errors.New("invalid sweep mode")
)

// NetworkSweeper implements the Sweeper interface.
type NetworkSweeper struct {
	config    *models.Config
	scanner   *scan.CombinedScanner
	store     Store
	processor ResultProcessor
	mu        sync.RWMutex
	done      chan struct{}
}

func (s *NetworkSweeper) Start(ctx context.Context) error {
	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	// Do initial sweep
	if err := s.runSweep(ctx); err != nil {
		log.Printf("Initial sweep failed: %v", err)
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return nil
		case <-ticker.C:
			if err := s.runSweep(ctx); err != nil {
				log.Printf("Periodic sweep failed: %v", err)
			}
		}
	}
}

func (s *NetworkSweeper) Stop() error {
	close(s.done)
	return s.scanner.Stop()
}

func (s *NetworkSweeper) GetResults(ctx context.Context, filter *models.ResultFilter) ([]models.Result, error) {
	return s.store.GetResults(ctx, filter)
}

func (s *NetworkSweeper) GetConfig() *models.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.config
}

func (s *NetworkSweeper) UpdateConfig(config *models.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = config

	return nil
}

type SweepMode models.SweepMode

// UnmarshalJSON implements json.Unmarshaler for SweepMode.
func (m *SweepMode) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	switch strings.ToLower(s) {
	case "tcp":
		*m = SweepMode(models.ModeTCP)
	case "icmp":
		*m = SweepMode(models.ModeICMP)
	default:
		return fmt.Errorf("%w: %s", errInvalidSweepMode, s)
	}

	return nil
}

// MarshalJSON implements json.Marshaler for SweepMode.
func (m *SweepMode) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(*m))
}

func (s *NetworkSweeper) generateTargets() ([]models.Target, error) {
	// Parse all IP specifications
	ranges, err := scan.ParseIPSpecFromStrings(s.config.Networks)
	if err != nil {
		return nil, fmt.Errorf("failed to parse network specifications: %w", err)
	}

	// Generate IPs from ranges
	ips := scan.GenerateIPs(ranges)

	log.Printf("Generated %d unique IP addresses from network specifications", len(ips))

	var allTargets []models.Target

	// Create ICMP targets if enabled
	if containsMode(s.config.SweepModes, models.ModeICMP) {
		for _, ip := range ips {
			// Skip network and broadcast addresses for IPv4
			if ip.To4() != nil {
				if ip[15] == 0 || ip[15] == 255 {
					continue
				}
			}
			allTargets = append(allTargets, models.Target{
				Host: ip.String(),
				Mode: models.ModeICMP,
			})
		}
	}

	// Create TCP targets for each port if enabled
	if containsMode(s.config.SweepModes, models.ModeTCP) {
		for _, ip := range ips {
			// Skip network and broadcast addresses for IPv4
			if ip.To4() != nil {
				if ip[15] == 0 || ip[15] == 255 {
					continue
				}
			}
			for _, port := range s.config.Ports {
				allTargets = append(allTargets, models.Target{
					Host: ip.String(),
					Port: port,
					Mode: models.ModeTCP,
				})
			}
		}
	}

	log.Printf("Generated %d targets for scanning (%d IPs, %d ports, modes: %v)",
		len(allTargets),
		len(ips),
		len(s.config.Ports),
		s.config.SweepModes)

	return allTargets, nil
}

func (s *NetworkSweeper) runSweep(ctx context.Context) error {
	// Generate targets
	targets, err := s.generateTargets()
	if err != nil {
		return fmt.Errorf("failed to generate targets: %w", err)
	}

	// Start the scan
	results, err := s.scanner.Scan(ctx, targets)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	// Process results as they come in
	for result := range results {
		// Process the result first
		if err := s.processor.Process(&result); err != nil {
			log.Printf("Failed to process result: %v", err)
		}

		// Store the result
		if err := s.store.SaveResult(ctx, &result); err != nil {
			log.Printf("Failed to save result: %v", err)
		}

		// Log based on scan type
		switch result.Target.Mode {
		case models.ModeICMP:
			if result.Available {
				log.Printf("Host %s responded to ICMP ping (%.2fms)",
					result.Target.Host, float64(result.RespTime)/float64(time.Millisecond))
			}
		case models.ModeTCP:
			if result.Available {
				log.Printf("Host %s has port %d open (%.2fms)",
					result.Target.Host, result.Target.Port,
					float64(result.RespTime)/float64(time.Millisecond))
			}
		}
	}

	return nil
}

func containsMode(modes []models.SweepMode, mode models.SweepMode) bool {
	for _, m := range modes {
		if m == mode {
			return true
		}
	}

	return false
}
