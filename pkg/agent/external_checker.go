// pkg/agent/external_checker.go
package agent

import (
	"context"
	"fmt"
	"log"

	grpcpkg "github.com/mfreeman451/homemon/pkg/grpc"
)

// ExternalChecker implements checker.Checker for external checker processes
type ExternalChecker struct {
	name    string
	address string
	client  *grpcpkg.ClientConn
}

// NewExternalChecker creates a new checker that connects to an external process
func NewExternalChecker(ctx context.Context, name, address string) (*ExternalChecker, error) {
	log.Printf("Creating new external checker %s at %s", name, address)

	// Create client using our gRPC package
	client, err := grpcpkg.NewClient(
		ctx,
		address,
		grpcpkg.WithMaxRetries(3),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client: %w", err)
	}

	checker := &ExternalChecker{
		name:    name,
		address: address,
		client:  client,
	}

	// Initial health check
	healthy, err := client.CheckHealth(context.Background(), "")
	if err != nil {
		err := client.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("initial health check failed: %w", err)
	}
	if !healthy {
		err := client.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("service is not healthy")
	}

	log.Printf("Successfully created external checker %s", name)
	return checker, nil
}

func (e *ExternalChecker) Check(ctx context.Context) (bool, string) {
	// Use our client's health check functionality
	healthy, err := e.client.CheckHealth(ctx, "")
	if err != nil {
		return false, fmt.Sprintf("Failed to check %s: %v", e.name, err)
	}

	if !healthy {
		return false, fmt.Sprintf("%s is not healthy", e.name)
	}

	// Get detailed status if available
	details := e.client.GetHealthDetails()
	if details != "" {
		return true, details
	}

	return true, fmt.Sprintf("%s is healthy", e.name)
}

// Close cleans up the checker's resources
func (e *ExternalChecker) Close() error {
	if e.client != nil {
		return e.client.Close()
	}
	return nil
}
