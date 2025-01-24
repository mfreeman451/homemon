package scan

import (
	"context"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/mfreeman451/serviceradar/pkg/models"
)

type TCPScanner struct {
	timeout     time.Duration
	concurrency int
	done        chan struct{}
	scan        func(context.Context, []models.Target) (<-chan models.Result, error)
}

func NewTCPScanner(timeout time.Duration, concurrency int) *TCPScanner {
	return &TCPScanner{
		timeout:     timeout,
		concurrency: concurrency,
		done:        make(chan struct{}),
	}
}

func (s *TCPScanner) Stop() error {
	close(s.done)
	return nil
}

func (s *TCPScanner) Scan(ctx context.Context, targets []models.Target) (<-chan models.Result, error) {
	if s.scan != nil {
		return s.scan(ctx, targets)
	}

	results := make(chan models.Result)
	targetChan := make(chan models.Target)

	var wg sync.WaitGroup
	// Start worker pool
	for i := 0; i < s.concurrency; i++ {
		wg.Add(1)

		go s.worker(ctx, &wg, targetChan, results)
	}

	// Feed targets to workers
	go func() {
		defer close(targetChan)

		for _, target := range targets {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				return
			case targetChan <- target:
			}
		}
	}()

	// Close results when all workers are done
	go func() {
		wg.Wait()
		close(results)
	}()

	return results, nil
}

func (s *TCPScanner) worker(ctx context.Context, wg *sync.WaitGroup, targets <-chan models.Target, results chan<- models.Result) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case target, ok := <-targets:
			if !ok {
				return
			}

			s.scanTarget(ctx, target, results)
		}
	}
}

func (s *TCPScanner) scanTarget(ctx context.Context, target models.Target, results chan<- models.Result) {
	start := time.Now()
	result := models.Result{
		Target:    target,
		FirstSeen: start,
		LastSeen:  start,
	}

	// Create connection timeout context
	connCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// Try to connect
	var d net.Dialer

	addr := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))

	conn, err := d.DialContext(connCtx, "tcp", addr)

	result.RespTime = time.Since(start)
	if err != nil {
		result.Error = err
		result.Available = false
	} else {
		result.Available = true

		if err := conn.Close(); err != nil {
			log.Print("Error closing connection: ", err)
			return
		}
	}

	select {
	case <-ctx.Done():
	case <-s.done:
	case results <- result:
	}
}
