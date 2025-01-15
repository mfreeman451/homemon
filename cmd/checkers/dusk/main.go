// cmd/checkers/dusk/main.go
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mfreeman451/homemon/pkg/checker/dusk"
	"github.com/mfreeman451/homemon/pkg/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

const (
	maxRecvSize = 4 * 1024 * 1024 // 4MB
	maxSendSize = 4 * 1024 * 1024 // 4MB
)

func main() {
	log.Printf("Starting Dusk checker...")

	configFile := flag.String("config", "/etc/homemon/checkers/dusk.json", "Path to config file")
	flag.Parse()

	log.Printf("Loading config from: %s", *configFile)
	config, err := dusk.LoadConfig(*configFile)

	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Loaded config: %+v", config)

	// Create context that can be canceled
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	checker := &dusk.DuskChecker{
		Config: config,
		Done:   make(chan struct{}),
	}

	// Start monitoring Dusk node
	log.Printf("Starting monitoring...")

	if err := checker.StartMonitoring(ctx); err != nil {
		log.Panicf("Failed to start monitoring: %v", err)
	}

	// Create gRPC server with options
	grpcServer := grpc.NewServer(config.ListenAddr,
		grpc.WithMaxRecvSize(maxRecvSize),
		grpc.WithMaxSendSize(maxSendSize),
	)

	// Register health server
	healthServer := dusk.NewHealthServer(checker)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	log.Printf("Registered health server")

	// Handle shutdown gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start gRPC server
	go func() {
		log.Printf("Starting gRPC server on %s", config.ListenAddr)

		if err := grpcServer.Start(); err != nil {
			log.Printf("gRPC server failed: %v", err)
			cancel() // Cancel context if server fails
		}
	}()

	// Wait for shutdown signal
	select {
	case sig := <-sigCh:
		log.Printf("Received signal %v, shutting down", sig)
	case <-ctx.Done():
		log.Printf("Context canceled, shutting down")
	}

	// Initiate graceful shutdown
	close(checker.Done) // Stop the checker
	grpcServer.Stop()   // Stop the gRPC server
	log.Printf("Shutdown complete")
}
