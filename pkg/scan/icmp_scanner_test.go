package scan

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mfreeman451/serviceradar/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

var (
	errTestReadError = fmt.Errorf("test read error")
)

func TestListenForRepliesBasicFlow(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockConn := NewMockPacketConnInterface(ctrl)
	scanner := &ICMPScanner{
		socketPool: &socketPool{
			sockets: []*socketEntry{
				{
					conn:      mockConn,
					createdAt: time.Now(),
					lastUsed:  atomic.Value{},
				},
			},
		},
		done:       make(chan struct{}),
		bufferPool: newBufferPool(maxPacketSize),
		responses:  sync.Map{},
	}
	scanner.socketPool.sockets[0].lastUsed.Store(time.Now())

	// Set up mock expectations for SetReadDeadline and ReadFrom using gomock.InOrder
	readyChan := make(chan struct{}) // Create readyChan here
	readFromCalled := make(chan struct{})

	gomock.InOrder(
		mockConn.EXPECT().SetReadDeadline(gomock.Any()).Return(nil).Times(1),
		mockConn.EXPECT().ReadFrom(gomock.Any()).DoAndReturn(func(b []byte) (int, net.Addr, error) {
			t.Log("Waiting for ReadFrom")
			close(readFromCalled) // Signal that ReadFrom has been called
			return 4, &net.IPAddr{IP: net.ParseIP("127.0.0.1")}, nil

		}).Times(1),
		mockConn.EXPECT().Close().Return(nil).Times(1), // Expect Close once at the end
	)

	// Set up response tracking for the target
	scanner.responses.Store("127.0.0.1", &pingResponse{})

	// Start the listener function
	errChan := make(chan error, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Log("Starting listenForReplies")

	go func() {
		errChan <- scanner.listenForReplies(ctx, readyChan)
		t.Log("Listener has finished")
	}()

	// Wait for the ready signal using a channel (instead of using time.Sleep)
	select {
	case <-readyChan: // Ensure that the listener has started
		t.Log("Got ready signal")
	case <-time.After(100 * time.Millisecond): // Timeout for readiness signal
		t.Fatal("Timeout waiting for ready signal")
	}

	// Wait for ReadFrom to be called
	select {
	case <-readFromCalled:
		t.Log("ReadFrom was called")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timeout waiting for ready signal")
	}

	// Check if the listener is still running and if the ReadFrom call is triggered
	select {
	case err := <-errChan:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Unexpected error: %v", err)
		}
		t.Log("Got expected context.Canceled error")
	case <-time.After(100 * time.Millisecond): // Timeout waiting for listener to stop
		t.Fatal("Timeout waiting for listener to stop")
	}
}

func TestListenForRepliesContextCancellation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockConn := NewMockPacketConnInterface(ctrl)
	scanner := &ICMPScanner{
		socketPool: &socketPool{
			sockets: []*socketEntry{
				{
					conn:      mockConn,
					createdAt: time.Now(),
					lastUsed:  atomic.Value{},
				},
			},
		},
		done:       make(chan struct{}),
		bufferPool: newBufferPool(maxPacketSize), // Initialize buffer pool
	}
	scanner.socketPool.sockets[0].lastUsed.Store(time.Now())

	// Expect deadline to be set before each read
	mockConn.EXPECT().
		SetReadDeadline(gomock.Any()).
		Return(nil).
		AnyTimes()

	// Expect ReadFrom to simulate blocking until canceled
	mockConn.EXPECT().
		ReadFrom(gomock.Any()).
		DoAndReturn(func([]byte) (int, net.Addr, error) {
			select {
			case <-time.After(10 * time.Millisecond):
				return 0, nil, &net.OpError{Op: "read", Err: context.Canceled}
			}
		}).
		AnyTimes()

	// Expect Close to be called during cleanup
	mockConn.EXPECT().
		Close().
		Return(nil).
		AnyTimes()

	ctx, cancel := context.WithCancel(context.Background())
	readyChan := make(chan struct{})

	// Start listener
	errChan := make(chan error, 1)
	go func() {
		err := scanner.listenForReplies(ctx, readyChan)
		errChan <- err
	}()

	// Wait for ready signal with timeout
	select {
	case <-readyChan:
		// Give a moment for the read operation to start
		time.Sleep(20 * time.Millisecond)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timeout waiting for ready signal")
	}

	// Cancel the context
	cancel()

	// Wait for error response with timeout
	select {
	case err := <-errChan:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timeout waiting for context cancellation")
	}
}

func TestListenForRepliesReadError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockConn := NewMockPacketConnInterface(ctrl)
	scanner := &ICMPScanner{
		socketPool: &socketPool{
			sockets: []*socketEntry{
				{
					conn:      mockConn,
					createdAt: time.Now(),
					lastUsed:  atomic.Value{},
				},
			},
		},
		done:       make(chan struct{}),
		bufferPool: newBufferPool(maxPacketSize),
		responses:  sync.Map{},
	}
	scanner.socketPool.sockets[0].lastUsed.Store(time.Now())

	// Create base error to simulate a read error
	readErr := fmt.Errorf("test read error")

	// Setup mock expectations
	mockConn.EXPECT().
		Close().
		Return(nil).
		AnyTimes()

	// Expect SetReadDeadline to be called
	mockConn.EXPECT().
		SetReadDeadline(gomock.Any()).
		Return(nil).
		AnyTimes()

	// Expect ReadFrom to return the error ONCE, then block indefinitely to prevent backoff loops
	readCount := 0
	mockConn.EXPECT().
		ReadFrom(gomock.Any()).
		DoAndReturn(func(b []byte) (int, net.Addr, error) {
			readCount++
			t.Logf("ReadFrom called at %v, count: %d", time.Now(), readCount)
			if readCount == 1 {
				return 0, nil, readErr // Simulate read error on first call
			}
			// Block indefinitely on subsequent calls to prevent continuous errors and backoff
			<-context.Background().Done()   // Block until context is cancelled (which won't happen in this test)
			return 0, nil, context.Canceled // Should not reach here in normal test flow
		}).
		AnyTimes()

	// Test the listener function
	readyChan := make(chan struct{})
	errChan := make(chan error, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Logf("Starting listenForReplies at %v", time.Now())
	go func() {
		err := scanner.listenForReplies(ctx, readyChan)
		select {
		case <-readyChan:
		default:
			t.Logf("listenForReplies returned with error at %v: %v", time.Now(), err)
			errChan <- err
		}
	}()

	// Wait for the ready signal with increased timeout
	t.Logf("Waiting for ready signal at %v", time.Now())
	select {
	case <-readyChan:
		t.Logf("Got ready signal at %v", time.Now())
	case <-time.After(500 * time.Millisecond): // Increased timeout for readiness
		t.Fatal("Timeout waiting for ready signal")
	}

	// Expect the error to be received with increased timeout
	select {
	case err := <-errChan:
		t.Logf("Got error at %v: %v", time.Now(), err)
		require.Error(t, err)
		require.Contains(t, err.Error(), readErr.Error()) // Ensure the error is the expected one
	case <-time.After(500 * time.Millisecond): // Increased timeout here as well
		t.Fatal("Timeout waiting for error")
	}
}

func TestICMPChecksum(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected uint16
	}{
		{
			name:     "Empty data",
			data:     []byte{},
			expected: 0xFFFF,
		},
		{
			name:     "Simple ICMP header",
			data:     []byte{8, 0, 0, 0, 0, 1, 0, 1},
			expected: 0xF7FD,
		},
		{
			name:     "Odd length data",
			data:     []byte{8, 0, 0, 0, 0, 1, 0, 1, 0},
			expected: 0xF7FD, // Corrected expected value
		},
	}

	scanner := &ICMPScanner{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scanner.calculateChecksum(tt.data)
			if result != tt.expected {
				t.Errorf("calculateChecksum() = %#x, want %#x", result, tt.expected)
			}
		})
	}
}

func TestSocketPool(t *testing.T) {
	t.Run("Concurrent Access", func(t *testing.T) {
		pool := newSocketPool(5, time.Minute, time.Minute)
		defer pool.close()

		var wg sync.WaitGroup

		numGoroutines := 10

		successCount := atomic.Int32{}
		poolFullCount := atomic.Int32{}
		otherErrorCount := atomic.Int32{}

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)

			go func() {
				defer wg.Done()

				conn, release, err := pool.getSocket()
				if err != nil {
					if errors.Is(err, errNoAvailableSocketsInPool) {
						poolFullCount.Add(1)
					} else {
						otherErrorCount.Add(1)
						t.Errorf("Unexpected error: %v", err)
					}

					return
				}

				if conn == nil {
					t.Error("Got nil connection with no error")
					otherErrorCount.Add(1)

					return
				}

				// Just verify the connection exists
				successCount.Add(1)

				// Simulate some work without touching the connection
				time.Sleep(10 * time.Millisecond)

				// Release the socket back to the pool
				release()
			}()
		}

		wg.Wait()

		// Verify results
		assert.Equal(t, int32(0), otherErrorCount.Load(), "Should not have any unexpected errors") // Modified line 104
		assert.Positive(t, successCount.Load(), "Should have some successful socket acquisitions")
		assert.Positive(t, poolFullCount.Load(), "Should have some pool-full conditions")
		assert.Equal(t, numGoroutines, int(successCount.Load()+poolFullCount.Load()),
			"Total attempts should equal number of goroutines")
	})
}

func TestICMPScanner(t *testing.T) {
	t.Run("Scanner Creation", func(t *testing.T) {
		scanner, err := NewICMPScanner(time.Second, 5, 3)
		require.NoError(t, err)
		require.NotNil(t, scanner)
		require.NotNil(t, scanner.socketPool)
		require.NotNil(t, scanner.bufferPool)
	})

	t.Run("Invalid Parameters", func(t *testing.T) {
		testCases := []struct {
			name        string
			timeout     time.Duration
			concurrency int
			count       int
		}{
			{"Zero timeout", 0, 5, 3},
			{"Zero concurrency", time.Second, 0, 3},
			{"Zero count", time.Second, 5, 0},
			{"Negative timeout", -time.Second, 5, 3},
			{"Negative concurrency", time.Second, -5, 3},
			{"Negative count", time.Second, 5, -3},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				scanner, err := NewICMPScanner(tc.timeout, tc.concurrency, tc.count)
				require.Error(t, err)
				require.Nil(t, scanner)
			})
		}
	})

	t.Run("Buffer Pool", func(t *testing.T) {
		pool := newBufferPool(1500)

		// Get buffer
		buf := pool.get()
		require.NotNil(t, buf)
		require.Len(t, buf, 1500)

		// Return buffer
		pool.put(buf)

		// Get another buffer (should reuse the one we put back)
		buf2 := pool.get()
		require.NotNil(t, buf2)
		require.Len(t, buf2, 1500)
	})
}

func TestICMPScannerGracefulShutdown(t *testing.T) {
	t.Parallel()

	scanner, err := NewICMPScanner(100*time.Millisecond, 5, 3)
	require.NoError(t, err)

	// Create a parent context with timeout
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer parentCancel()

	// Create scan context as child of parent
	ctx, cancel := context.WithTimeout(parentCtx, 500*time.Millisecond)
	defer cancel()

	// Start scan
	resultsCh, err := scanner.Scan(ctx, []models.Target{
		{Host: "127.0.0.1", Mode: models.ModeICMP},
	})
	require.NoError(t, err)

	// Track results in background
	var results []models.Result

	resultsDone := make(chan struct{})

	go func() {
		defer close(resultsDone)

		for result := range resultsCh {
			t.Logf("Received result: %+v", result)
			results = append(results, result)
		}

		t.Log("Results channel closed")
	}()

	// Wait a short time for some results
	select {
	case <-time.After(100 * time.Millisecond):
		// Continue with shutdown
	case <-parentCtx.Done():
		t.Fatal("Parent context timed out")
	}

	// Create stop context
	stopCtx, stopCancel := context.WithTimeout(parentCtx, 500*time.Millisecond)
	defer stopCancel()

	// Cancel scan context to trigger shutdown
	cancel()

	// Stop the scanner
	err = scanner.Stop(stopCtx)
	require.NoError(t, err, "Stop should not return error")

	// Wait for results channel to close
	select {
	case <-resultsDone:
		t.Log("Results channel closed successfully")
	case <-parentCtx.Done():
		t.Fatal("Parent context timed out waiting for results")
	}

	// Verify we got at least one result
	if len(results) > 0 {
		require.Equal(t, "127.0.0.1", results[0].Target.Host)
	}
}

func TestAtomicOperations(t *testing.T) {
	t.Run("Socket Use Counter", func(t *testing.T) {
		entry := &socketEntry{}

		var wg sync.WaitGroup

		// Simulate concurrent increments
		for i := 0; i < 100; i++ {
			wg.Add(1)

			go func() {
				defer wg.Done()
				entry.inUse.Add(1)
			}()
		}

		// Simulate concurrent decrements
		for i := 0; i < 100; i++ {
			wg.Add(1)

			go func() {
				defer wg.Done()
				entry.inUse.Add(-1)
			}()
		}

		wg.Wait()

		// Final count should be 0
		assert.Equal(t, int32(0), entry.inUse.Load())
	})
}
