package scan

import (
	"context"
	"net"
	"time"

	"github.com/mfreeman451/serviceradar/pkg/models"
)

//go:generate mockgen -destination=mock_scanner.go -package=scan github.com/mfreeman451/serviceradar/pkg/scan Scanner,ResultProcessor,PacketConnInterface

type PacketConnInterface interface {
	SetReadDeadline(t time.Time) error
	ReadFrom(b []byte) (n int, addr net.Addr, err error)
	WriteTo(p []byte, addr net.Addr) (n int, err error)
	Close() error
}

// Scanner defines how to perform network sweeps.
type Scanner interface {
	// Scan performs the sweep and returns results through the channel
	Scan(context.Context, []models.Target) (<-chan models.Result, error)
	// Stop gracefully stops any ongoing scans
	Stop(ctx context.Context) error
}

// ResultProcessor defines how to process and aggregate sweep results.
type ResultProcessor interface {
	// Process takes a Result and updates internal state
	Process(result *models.Result) error
	// GetSummary returns the current summary of all processed results
	GetSummary() (*models.SweepSummary, error)
	// Reset clears the processor's state
	Reset()
}
