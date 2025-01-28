package scan

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/mfreeman451/serviceradar/pkg/models"
)

const (
	errorChannelSize = 2
)

type CombinedScanner struct {
	tcpScanner  Scanner
	icmpScanner Scanner
	done        chan struct{}
}

func NewCombinedScanner(timeout time.Duration, concurrency, icmpCount int) *CombinedScanner {
	var icmpScanner Scanner

	if icmpCount > 0 {
		var err error

		icmpConcurrency := concurrency / 4

		if icmpConcurrency < 1 {
			icmpConcurrency = 1
		}

		icmpScanner, err = NewICMPScanner(timeout, icmpConcurrency, icmpCount)
		if err != nil {
			log.Printf("ICMP scanning not available: %v, falling back to TCP only", err)

			icmpScanner = nil // Explicitly set to nil to be clear
		}
	}

	scanner := &CombinedScanner{
		tcpScanner:  NewTCPScanner(timeout, concurrency),
		icmpScanner: icmpScanner,
		done:        make(chan struct{}),
	}

	log.Printf("Created combined scanner with timeout=%v, concurrency=%d, icmpCount=%d",
		timeout, concurrency, icmpCount)

	return scanner
}

func (s *CombinedScanner) Scan(ctx context.Context, targets []models.Target) (<-chan models.Result, error) {
	if len(targets) == 0 {
		empty := make(chan models.Result)
		close(empty)

		return empty, nil
	}

	// Calculate total hosts by counting unique IPs
	uniqueHosts := make(map[string]struct{})
	for _, target := range targets {
		uniqueHosts[target.Host] = struct{}{}
	}

	totalHosts := len(uniqueHosts)

	separated := s.separateTargets(targets)
	log.Printf("Scanning targets - TCP: %d, ICMP: %d, Unique Hosts: %d",
		len(separated.tcp), len(separated.icmp), totalHosts)

	// Pass total hosts count through result metadata
	for i := range targets {
		targets[i].Metadata = map[string]interface{}{
			"total_hosts": totalHosts,
		}
	}

	// Handle single scanner cases
	if result := s.handleSingleScannerCase(ctx, separated); result != nil {
		return result.resultChan, result.err
	}

	// Handle mixed scanner case
	return s.handleMixedScanners(ctx, separated)
}

type scanResult struct {
	resultChan <-chan models.Result
	err        error
}

// handleSingleScannerCase handles cases where only one type of scanner is needed.
func (s *CombinedScanner) handleSingleScannerCase(ctx context.Context, targets scanTargets) *scanResult {
	if len(targets.tcp) > 0 && len(targets.icmp) == 0 {
		results, err := s.tcpScanner.Scan(ctx, targets.tcp)
		if err != nil {
			return &scanResult{nil, fmt.Errorf("TCP scan error: %w", err)}
		}

		return &scanResult{results, nil}
	}

	if len(targets.icmp) > 0 && len(targets.tcp) == 0 {
		results, err := s.icmpScanner.Scan(ctx, targets.icmp)
		if err != nil {
			return &scanResult{nil, fmt.Errorf("ICMP scan error: %w", err)}
		}

		return &scanResult{results, nil}
	}

	return nil
}

// handleMixedScanners manages scanning with both TCP and ICMP scanners.
func (s *CombinedScanner) handleMixedScanners(ctx context.Context, targets scanTargets) (<-chan models.Result, error) {
	results := make(chan models.Result)

	var wg sync.WaitGroup

	errChan := make(chan error, errorChannelSize)

	// Start TCP scanner if needed
	if err := s.startTCPScanner(ctx, targets.tcp, &wg, results); err != nil {
		return nil, err
	}

	// Start ICMP scanner if available and needed
	if s.icmpScanner != nil && len(targets.icmp) > 0 {
		if err := s.startICMPScanner(ctx, targets.icmp, &wg, results); err != nil {
			return nil, err
		}
	} else if len(targets.icmp) > 0 {
		log.Printf("Skipping ICMP scan of %d targets - ICMP scanner not available", len(targets.icmp))
	}

	// Close results when both scanners are done
	go func() {
		wg.Wait()
		close(results)
		close(errChan)
	}()

	return results, nil
}

// startTCPScanner initializes and starts the TCP scanner if TCP targets exist.
func (s *CombinedScanner) startTCPScanner(
	ctx context.Context, targets []models.Target, wg *sync.WaitGroup, results chan models.Result) error {
	if len(targets) == 0 {
		return nil
	}

	tcpResults, err := s.tcpScanner.Scan(ctx, targets)
	if err != nil {
		return fmt.Errorf("TCP scan error: %w", err)
	}

	wg.Add(1)

	go func() {
		defer wg.Done()

		s.forwardResults(ctx, tcpResults, results)
	}()

	return nil
}

// startICMPScanner initializes and starts the ICMP scanner if ICMP targets exist.
func (s *CombinedScanner) startICMPScanner(
	ctx context.Context, targets []models.Target, wg *sync.WaitGroup, results chan models.Result) error {
	if len(targets) == 0 {
		return nil
	}

	icmpResults, err := s.icmpScanner.Scan(ctx, targets)
	if err != nil {
		return fmt.Errorf("ICMP scan error: %w", err)
	}

	wg.Add(1)

	go func() {
		defer wg.Done()
		s.forwardResults(ctx, icmpResults, results)
	}()

	return nil
}

func (*CombinedScanner) separateTargets(targets []models.Target) scanTargets {
	separated := scanTargets{
		tcp:  make([]models.Target, 0, len(targets)),
		icmp: make([]models.Target, 0, len(targets)),
	}

	for _, target := range targets {
		switch target.Mode {
		case models.ModeTCP:
			separated.tcp = append(separated.tcp, target)
		case models.ModeICMP:
			separated.icmp = append(separated.icmp, target)
		default:
			log.Printf("Unknown scan mode for target %v: %v", target, target.Mode)
		}
	}

	return separated
}

func (s *CombinedScanner) forwardResults(ctx context.Context, in <-chan models.Result, out chan<- models.Result) {
	for {
		select {
		case r, ok := <-in:
			if !ok {
				return
			}
			select {
			case out <- r:
			case <-ctx.Done():
				return
			case <-s.done:
				return
			}
		case <-ctx.Done():
			return
		case <-s.done:
			return
		}
	}
}

func (s *CombinedScanner) Stop() error {
	close(s.done)
	_ = s.tcpScanner.Stop()
	_ = s.icmpScanner.Stop()

	return nil
}

type scanTargets struct {
	tcp  []models.Target
	icmp []models.Target
}
