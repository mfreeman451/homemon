package scan

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"sync"
	"time"

	"github.com/mfreeman451/serviceradar/pkg/models"
)

const (
	numShards    = 32 // Experiment with different values
	readDeadline = 100 * time.Millisecond
)

// connEntry represents a connection in the pool along with its last used timestamp.
type connEntry struct {
	conn      net.Conn
	lastUsed  time.Time
	createdAt time.Time
}

type shard struct {
	mu          sync.Mutex
	connections map[string][]*connEntry
}

// connectionPool manages a pool of reusable TCP connections.
type connectionPool struct {
	shards      [numShards]shard
	maxIdle     int
	maxLifetime time.Duration
	idleTimeout time.Duration
	dialer      *net.Dialer
}

// newConnectionPool creates a new connection pool.
func newConnectionPool(maxIdle int, maxLifetime, idleTimeout time.Duration, dialer *net.Dialer) *connectionPool {
	pool := &connectionPool{
		maxIdle:     maxIdle,
		maxLifetime: maxLifetime,
		idleTimeout: idleTimeout,
		dialer:      dialer,
	}

	for i := 0; i < numShards; i++ {
		pool.shards[i].connections = make(map[string][]*connEntry)
	}

	return pool
}

func (p *connectionPool) getShard(address string) *shard {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(address)) // Ignoring error as hash.Write never returns an error
	shardIndex := hash.Sum32() % numShards

	return &p.shards[shardIndex]
}

// get retrieves a connection from the pool for the given address.
func (p *connectionPool) get(ctx context.Context, address string) (net.Conn, error) {
	shard := p.getShard(address)
	shard.mu.Lock()

	// Clean up expired connections first
	p.cleanupLocked(shard)

	// Get an existing connection
	if entries, ok := shard.connections[address]; ok {
		for i := len(entries) - 1; i >= 0; i-- {
			entry := entries[i]
			if time.Since(entry.lastUsed) < p.idleTimeout && time.Since(entry.createdAt) < p.maxLifetime {
				// Remove the connection from the slice
				shard.connections[address] = append(entries[:i], entries[i+1:]...)
				shard.mu.Unlock()

				return entry.conn, nil
			}
		}
	}

	shard.mu.Unlock()

	// Create a new connection
	conn, err := p.dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// put returns a connection to the pool.
func (p *connectionPool) put(address string, conn net.Conn) {
	shard := p.getShard(address)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	now := time.Now()

	// Guard clause: If the pool is full, close the connection and return early
	entries := shard.connections[address]
	if len(entries) >= p.maxIdle {
		if err := conn.Close(); err != nil {
			log.Printf("Error closing connection when pool is full: %v", err)
		}

		return
	}

	// If the address has no entries, initialize an empty slice
	if _, ok := shard.connections[address]; !ok {
		shard.connections[address] = []*connEntry{}
	}

	// Add the connection to the pool
	shard.connections[address] = append(shard.connections[address], &connEntry{
		conn:      conn,
		lastUsed:  now,
		createdAt: now,
	})
}

func (*connectionPool) cleanupLocked(shard *shard) {
	// (Optionally, add logic here to only remove expired connections.)
	for addr, entries := range shard.connections {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].conn != nil {
				// You might choose to ignore errors here
				_ = entries[i].conn.Close()
			}
		}

		delete(shard.connections, addr)
	}
}

// close closes all connections in the pool.
func (p *connectionPool) close() {
	for i := 0; i < numShards; i++ {
		shard := &p.shards[i]
		shard.mu.Lock()
		for _, entries := range shard.connections {
			for _, entry := range entries {
				if err := entry.conn.Close(); err != nil {
					log.Printf("Error closing connection: %v", err)
				}
			}
		}

		shard.connections = make(map[string][]*connEntry)

		shard.mu.Unlock()
	}
}

type TCPScanner struct {
	timeout     time.Duration
	concurrency int
	dialer      net.Dialer
	pool        *connectionPool
}

func NewTCPScanner(timeout time.Duration, concurrency, maxIdle int, maxLifetime, idleTimeout time.Duration) *TCPScanner {
	dialer := &net.Dialer{
		Timeout: timeout,
		Resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				// Use a very short timeout for DNS lookups.
				d := net.Dialer{Timeout: 200 * time.Millisecond}
				return d.DialContext(ctx, network, address)
			},
		},
	}

	pool := newConnectionPool(maxIdle, maxLifetime, idleTimeout, dialer)

	return &TCPScanner{
		timeout:     timeout,
		concurrency: concurrency,
		dialer:      *dialer,
		pool:        pool,
	}
}

func (s *TCPScanner) Scan(ctx context.Context, targets []models.Target) (<-chan models.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	results := make(chan models.Result, len(targets))
	targetChan := make(chan models.Target, len(targets))

	// Create context that can be canceled
	scanCtx, cancel := context.WithCancel(ctx)

	// Start worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < s.concurrency; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for {
				select {
				case target, ok := <-targetChan:
					if !ok {
						return
					}

					s.scanTarget(scanCtx, target, results)
				case <-scanCtx.Done():
					return
				}
			}
		}()
	}

	// Feed targets to workers
	go func() {
		defer close(targetChan)

		for _, target := range targets {
			select {
			case targetChan <- target:
			case <-scanCtx.Done():
				return
			}
		}
	}()

	// Wait for all workers and close results
	go func() {
		defer cancel() // Cancel context when done
		defer close(results)
		wg.Wait()
	}()

	return results, nil
}

func (s *TCPScanner) scanTarget(ctx context.Context, target models.Target, results chan<- models.Result) {
	// Check context before doing anything
	if ctx.Err() != nil {
		return
	}

	// Create a timeout context for this scan
	scanCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	result := models.Result{
		Target:    target,
		FirstSeen: time.Now(),
		LastSeen:  time.Now(),
	}

	address := fmt.Sprintf("%s:%d", target.Host, target.Port)
	conn, err := s.pool.get(scanCtx, address)

	if err != nil {
		result.Error = err
		result.Available = false
		select {
		case results <- result:
		case <-ctx.Done():
		}

		return
	}

	defer s.pool.put(address, conn)

	result.RespTime = time.Since(result.FirstSeen)
	result.Available = s.checkConnection(conn)

	select {
	case results <- result:
	case <-ctx.Done():
	}
}

func (*TCPScanner) checkConnection(conn net.Conn) bool {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return true // Accept non-TCP connections as valid
	}

	// Set a longer read deadline (100ms) to avoid false negatives
	err := tcpConn.SetReadDeadline(time.Now().Add(readDeadline))
	if err != nil {
		log.Printf("Error setting read deadline: %v", err)
		return false
	}

	defer func() {
		if err = tcpConn.SetReadDeadline(time.Time{}); err != nil {
			log.Printf("Error resetting read deadline: %v", err)
		}
	}()

	// Try to read a single byte
	buf := make([]byte, 1)
	_, err = tcpConn.Read(buf)

	// Connection is considered valid if:
	// 1. Read succeeds (some services send banner)
	// 2. Read times out (most common for services that wait for client)
	// 3. Connection is closed by remote (service accepts and closes)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return true // Timeout is expected and indicates port is open
		}

		if err.Error() == "EOF" {
			return true // EOF means connection was accepted then closed
		}
	}

	return err == nil // If no error, read succeeded
}

func (s *TCPScanner) Stop(context.Context) error {
	if s.pool != nil {
		s.pool.close()
	}

	return nil
}
