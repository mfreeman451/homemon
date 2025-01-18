package agent

import (
	"context"

	"github.com/mfreeman451/homemon/proto"
)

type Service interface {
	Start(context.Context) error
	Stop() error
}

// SweepStatusProvider is an interface for services that can provide sweep status
type SweepStatusProvider interface {
	GetStatus(context.Context) (*proto.StatusResponse, error)
}