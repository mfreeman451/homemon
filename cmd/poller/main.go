package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/mfreeman451/homemon/pkg/poller"
)

func main() {
	configPath := flag.String("config", "/etc/homemon/poller.json", "Path to config file")
	flag.Parse()

	config, err := poller.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Create context that can be canceled
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create poller with context
	p, err := poller.New(ctx, config)
	if err != nil {
		log.Fatalf("Failed to create poller: %v", err)
	}
	defer p.Close()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start poller in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Start(ctx)
	}()

	// Wait for either error or shutdown signal
	select {
	case err := <-errChan:
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("Poller failed: %v", err)
		}
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down", sig)
		cancel()
	}

	// Wait for shutdown to complete
	<-errChan
	log.Println("Shutdown complete")
}
