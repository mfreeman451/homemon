package scan

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mfreeman451/serviceradar/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

var (
	errTCPScanFailed  = fmt.Errorf("TCP scan failed")
	errICMPScanFailed = fmt.Errorf("ICMP scan failed")
)

func TestCombinedScanner_Scan_Mock(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockTCP := NewMockScanner(ctrl)
	mockICMP := NewMockScanner(ctrl)

	scanner := &CombinedScanner{
		tcpScanner:  mockTCP,
		icmpScanner: mockICMP,
		done:        make(chan struct{}),
	}

	targets := []models.Target{
		{Host: "127.0.0.1", Port: 22, Mode: models.ModeTCP},
		{Host: "127.0.0.1", Mode: models.ModeICMP},
	}

	tcpResults := make(chan models.Result, 1)
	icmpResults := make(chan models.Result, 1)

	// Use WaitGroup to ensure results are sent before closing channels
	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()
		defer close(tcpResults)
		tcpResults <- models.Result{
			Target:    targets[0],
			Available: true,
		}
	}()

	go func() {
		defer wg.Done()
		defer close(icmpResults)
		icmpResults <- models.Result{
			Target:    targets[1],
			Available: true,
		}
	}()

	mockTCP.EXPECT().Scan(gomock.Any(), gomock.Any()).Return(tcpResults, nil)
	mockICMP.EXPECT().Scan(gomock.Any(), gomock.Any()).Return(icmpResults, nil)

	results, err := scanner.Scan(context.Background(), targets)
	require.NoError(t, err)

	// Wait for all results to be sent
	wg.Wait()

	// Collect results
	var resultCount int

	for range results {
		resultCount++
	}

	require.Equal(t, len(targets), resultCount, "Expected results for all targets")
}

func TestNewCombinedScanner_ICMPError(t *testing.T) {
	// Simulate an error by passing invalid parameters
	scanner := NewCombinedScanner(1*time.Second, 1, 0, 1, 1, 1)
	require.NotNil(t, scanner)
	require.Nil(t, scanner.icmpScanner, "ICMP scanner should be nil due to error")
}

func TestCombinedScanner_Scan_MixedTargets(t *testing.T) {
	t.Parallel() // Allow parallel test execution

	ctrl, ctx := gomock.WithContext(context.Background(), t)
	defer ctrl.Finish()

	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()

	mockTCP := NewMockScanner(ctrl)
	mockICMP := NewMockScanner(ctrl)

	targets := []models.Target{
		{Host: "192.168.1.1", Port: 80, Mode: models.ModeTCP},
		{Host: "192.168.1.2", Mode: models.ModeICMP},
	}

	tcpResults := make(chan models.Result, 1)
	icmpResults := make(chan models.Result, 1)

	// Send results and close channels in goroutines
	go func() {
		tcpResults <- models.Result{
			Target: models.Target{
				Host: "192.168.1.1",
				Port: 80,
				Mode: models.ModeTCP,
			},
			Available: true,
		}
		close(tcpResults)
	}()

	go func() {
		icmpResults <- models.Result{
			Target: models.Target{
				Host: "192.168.1.2",
				Mode: models.ModeICMP,
			},
			Available: true,
		}
		close(icmpResults)
	}()

	mockTCP.EXPECT().
		Scan(gomock.Any(), matchTargets(models.ModeTCP)).
		Return(tcpResults, nil)

	mockICMP.EXPECT().
		Scan(gomock.Any(), matchTargets(models.ModeICMP)).
		Return(icmpResults, nil)

	scanner := &CombinedScanner{
		tcpScanner:  mockTCP,
		icmpScanner: mockICMP,
		done:        make(chan struct{}),
	}

	results, err := scanner.Scan(ctx, targets)
	require.NoError(t, err)

	// Collect results with timeout
	var gotResults []models.Result

	resultsDone := make(chan struct{})

	go func() {
		defer close(resultsDone)

		for result := range results {
			gotResults = append(gotResults, result)
		}
	}()

	select {
	case <-resultsDone:
	case <-ctx.Done():
		t.Fatal("Test timed out waiting for results")
	}

	require.Len(t, gotResults, 2, "Expected 2 results")
}

// TestCombinedScanner_ScanBasic tests basic scanner functionality.
func TestCombinedScanner_ScanBasic(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockTCP := NewMockScanner(ctrl)
	mockICMP := NewMockScanner(ctrl)

	// Test empty targets
	scanner := &CombinedScanner{
		tcpScanner:  mockTCP,
		icmpScanner: mockICMP,
		done:        make(chan struct{}),
	}

	ctx := context.Background()
	results, err := scanner.Scan(ctx, []models.Target{})

	require.NoError(t, err)
	require.NotNil(t, results)

	count := 0
	for range results {
		count++
	}

	assert.Equal(t, 0, count)
}

func TestCombinedScanner_ScanMixed(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)
	defer ctrl.Finish()

	mockTCP := NewMockScanner(ctrl)
	mockICMP := NewMockScanner(ctrl)

	targets := []models.Target{
		{Host: "192.168.1.1", Port: 80, Mode: models.ModeTCP},
		{Host: "192.168.1.2", Mode: models.ModeICMP},
	}

	tcpResults := make(chan models.Result, 1)
	icmpResults := make(chan models.Result, 1)

	// Define expected results
	tcpResult := models.Result{
		Target: models.Target{
			Host: "192.168.1.1",
			Port: 80,
			Mode: models.ModeTCP,
		},
		Available: true,
	}

	icmpResult := models.Result{
		Target: models.Target{
			Host: "192.168.1.2",
			Mode: models.ModeICMP,
		},
		Available: true,
	}

	// Send results and close channels
	go func() {
		tcpResults <- tcpResult
		close(tcpResults)
	}()

	go func() {
		icmpResults <- icmpResult
		close(icmpResults)
	}()

	mockTCP.EXPECT().
		Scan(gomock.Any(), matchTargets(models.ModeTCP)).
		Return(tcpResults, nil)

	mockICMP.EXPECT().
		Scan(gomock.Any(), matchTargets(models.ModeICMP)).
		Return(icmpResults, nil)

	mockTCP.EXPECT().Stop(ctx).Return(nil).AnyTimes()
	mockICMP.EXPECT().Stop(ctx).Return(nil).AnyTimes()

	scanner := &CombinedScanner{
		tcpScanner:  mockTCP,
		icmpScanner: mockICMP,
		done:        make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	results, err := scanner.Scan(ctx, targets)
	require.NoError(t, err)
	require.NotNil(t, results)

	// Collect results
	gotResults := make([]models.Result, 0, len(targets))
	for result := range results {
		gotResults = append(gotResults, result)
	}

	// Should get exactly 2 results
	require.Len(t, gotResults, 2)

	// Create maps to match results by mode since order isn't guaranteed
	expectedMap := map[models.SweepMode]models.Result{
		models.ModeTCP:  tcpResult,
		models.ModeICMP: icmpResult,
	}

	gotMap := map[models.SweepMode]models.Result{}
	for _, result := range gotResults {
		gotMap[result.Target.Mode] = result
	}

	// Compare results by mode
	for mode, expected := range expectedMap {
		got, exists := gotMap[mode]
		if assert.True(t, exists, "Missing result for mode %s", mode) {
			assert.Equal(t, expected.Target, got.Target, "Target mismatch for mode %s", mode)
			assert.Equal(t, expected.Available, got.Available, "Availability mismatch for mode %s", mode)
		}
	}
}

// TestCombinedScanner_ScanErrors tests error handling.
func TestCombinedScanner_ScanErrors(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)
	defer ctrl.Finish()

	tests := []struct {
		name       string
		targets    []models.Target
		setupMocks func(mockTCP, mockICMP *MockScanner)
		wantErr    bool
		wantErrStr string
	}{
		{
			name: "TCP scanner error",
			targets: []models.Target{
				{Host: "192.168.1.1", Port: 80, Mode: models.ModeTCP},
			},
			setupMocks: func(mockTCP, mockICMP *MockScanner) {
				mockTCP.EXPECT().
					Scan(gomock.Any(), gomock.Any()).
					Return(nil, errTCPScanFailed)
				mockTCP.EXPECT().Stop(ctx).Return(nil).AnyTimes()
				mockICMP.EXPECT().Stop(ctx).Return(nil).AnyTimes()
			},
			wantErr:    true,
			wantErrStr: "TCP scan error: TCP scan failed",
		},
		{
			name: "ICMP scanner error",
			targets: []models.Target{
				{Host: "192.168.1.2", Mode: models.ModeICMP},
			},
			setupMocks: func(mockTCP, mockICMP *MockScanner) {
				mockICMP.EXPECT().
					Scan(gomock.Any(), gomock.Any()).
					Return(nil, errICMPScanFailed)
				mockTCP.EXPECT().Stop(ctx).Return(nil).AnyTimes()
				mockICMP.EXPECT().Stop(ctx).Return(nil).AnyTimes()
			},
			wantErr:    true,
			wantErrStr: "ICMP scan error: ICMP scan failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockTCP := NewMockScanner(ctrl)
			mockICMP := NewMockScanner(ctrl)

			tt.setupMocks(mockTCP, mockICMP)

			scanner := &CombinedScanner{
				tcpScanner:  mockTCP,
				icmpScanner: mockICMP,
				done:        make(chan struct{}),
			}

			_, err := scanner.Scan(context.Background(), tt.targets)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantErrStr, err.Error())

				return
			}

			require.NoError(t, err)
		})
	}
}

// Helper functions

func matchTargets(mode models.SweepMode) gomock.Matcher {
	return targetModeMatcher{mode: mode}
}

type targetModeMatcher struct {
	mode models.SweepMode
}

func (m targetModeMatcher) Matches(x interface{}) bool {
	targets, ok := x.([]models.Target)
	if !ok {
		return false
	}

	for _, t := range targets {
		if t.Mode != m.mode {
			return false
		}
	}

	return true
}

func (m targetModeMatcher) String() string {
	return fmt.Sprintf("targets with mode %s", m.mode)
}
