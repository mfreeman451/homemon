package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/mfreeman451/homemon/pkg/cloud"
	"github.com/mfreeman451/homemon/pkg/cloud/api"
	"github.com/mfreeman451/homemon/proto"
	"google.golang.org/grpc"
)

func main() {
	configPath := flag.String("config", "/etc/homemon/cloud.json", "Path to config file")
	flag.Parse()

	config, err := cloud.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	server, err := cloud.NewServer(config)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Create and start API server
	apiServer := api.NewAPIServer()
	server.SetAPIServer(apiServer)

	// Start gRPC server
	lis, err := net.Listen("tcp", ":50052")
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	proto.RegisterPollerServiceServer(grpcServer, server)

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, initiating shutdown", sig)
		server.Shutdown()
		grpcServer.GracefulStop()
	}()

	// Start monitoring goroutine
	go server.MonitorPollers(context.Background())

	// Start HTTP server for API and web interface
	go func() {
		log.Printf("Starting HTTP server on %s", config.ListenAddr)
		if err := apiServer.Start(config.ListenAddr); err != nil {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	log.Printf("gRPC server listening on :50052")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
