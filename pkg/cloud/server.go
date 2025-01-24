package cloud

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/mfreeman451/serviceradar/pkg/cloud/alerts"
	"github.com/mfreeman451/serviceradar/pkg/cloud/api"
	"github.com/mfreeman451/serviceradar/pkg/db"
	"github.com/mfreeman451/serviceradar/pkg/grpc"
	"github.com/mfreeman451/serviceradar/proto"
)

const (
	shutdownTimeout          = 10 * time.Second
	oneDay                   = 24 * time.Hour
	oneWeek                  = 7 * oneDay
	serviceradarDirPerms     = 0700
	nodeHistoryLimit         = 1000
	nodeDiscoveryTimeout     = 30 * time.Second
	nodeNeverReportedTimeout = 30 * time.Second
	pollerTimeout            = 30 * time.Second
	defaultDBPath            = "/var/lib/serviceradar/serviceradar.db"
	KB                       = 1024
	MB                       = 1024 * KB
	maxMessageSize           = 4 * MB
	statusUnknown            = "unknown"
	sweepService             = "sweep"
)

var (
	errEmptyPollerID    = errors.New("empty poller ID")
	errDatabaseError    = errors.New("database error")
	errInvalidSweepData = errors.New("invalid sweep data")
)

type Config struct {
	ListenAddr     string                 `json:"listen_addr"`
	GrpcAddr       string                 `json:"grpc_addr"`
	DBPath         string                 `json:"db_path"`
	AlertThreshold time.Duration          `json:"alert_threshold"`
	Webhooks       []alerts.WebhookConfig `json:"webhooks,omitempty"`
	KnownPollers   []string               `json:"known_pollers,omitempty"`
}

type Server struct {
	proto.UnimplementedPollerServiceServer
	mu             sync.RWMutex
	db             *db.DB
	alertThreshold time.Duration
	webhooks       []*alerts.WebhookAlerter
	apiServer      *api.APIServer
	ShutdownChan   chan struct{}
	knownPollers   []string
	grpcServer     *grpc.Server
}

func NewServer(_ context.Context, config *Config) (*Server, error) {
	// Use default DB path if not specified
	dbPath := config.DBPath
	if dbPath == "" {
		dbPath = defaultDBPath
	}

	// Ensure the directory exists
	if err := os.MkdirAll("/var/lib/serviceradar", serviceradarDirPerms); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	// Initialize database
	database, err := db.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errDatabaseError, err)
	}

	server := &Server{
		db:             database,
		alertThreshold: config.AlertThreshold,
		webhooks:       make([]*alerts.WebhookAlerter, 0),
		ShutdownChan:   make(chan struct{}),
		knownPollers:   config.KnownPollers,
	}

	// Initialize webhooks
	server.initializeWebhooks(config.Webhooks)

	return server, nil
}

// Start implements the lifecycle.Service interface.
func (s *Server) Start(ctx context.Context) error {
	log.Printf("Starting cloud service...")

	// Start GRPC server
	if s.grpcServer != nil {
		go func() {
			if err := s.grpcServer.Start(); err != nil {
				log.Printf("gRPC server error: %v", err)
			}
		}()
	}

	// Send startup notification
	if err := s.sendStartupNotification(ctx); err != nil {
		log.Printf("Failed to send startup notification: %v", err)
	}

	// Start periodic cleanup
	go s.periodicCleanup(ctx)

	// Start node monitoring with proper delays
	go func() {
		log.Printf("Starting node monitoring...")

		// Initial delay to allow nodes to report in
		time.Sleep(nodeDiscoveryTimeout)
		s.checkInitialStates(ctx)

		// Check never-reported pollers after another delay
		time.Sleep(nodeNeverReportedTimeout)
		s.checkNeverReportedPollers(ctx, &Config{KnownPollers: s.knownPollers})

		// Start continuous monitoring
		s.MonitorPollers(ctx)
	}()

	return nil
}

// Stop implements the lifecycle.Service interface.
func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Send shutdown notification
	if err := s.sendShutdownNotification(ctx); err != nil {
		log.Printf("Failed to send shutdown notification: %v", err)
	}

	// Stop GRPC server if it exists
	if s.grpcServer != nil {
		s.grpcServer.Stop()
	}

	// Close database
	if err := s.db.Close(); err != nil {
		log.Printf("Error closing database: %v", err)
	}

	// Signal all background tasks to stop
	close(s.ShutdownChan)

	return nil
}

func (s *Server) sendStartupNotification(ctx context.Context) error {
	if len(s.webhooks) == 0 {
		return nil
	}

	alert := &alerts.WebhookAlert{
		Level:     alerts.Info,
		Title:     "Cloud Service Started",
		Message:   fmt.Sprintf("ServiceRadar cloud service initialized at %s", time.Now().Format(time.RFC3339)),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		NodeID:    "cloud",
		Details: map[string]any{
			"version":  "1.0.5",
			"hostname": getHostname(),
		},
	}

	return s.sendAlert(ctx, alert)
}

func (s *Server) sendShutdownNotification(ctx context.Context) error {
	if len(s.webhooks) == 0 {
		return nil
	}

	alert := &alerts.WebhookAlert{
		Level:     alerts.Warning,
		Title:     "Cloud Service Stopping",
		Message:   fmt.Sprintf("ServiceRadar cloud service shutting down at %s", time.Now().Format(time.RFC3339)),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		NodeID:    "cloud",
		Details: map[string]any{
			"hostname": getHostname(),
		},
	}

	return s.sendAlert(ctx, alert)
}

// checkPollers is the monitoring function that checks poller health.
func (s *Server) checkPollers(ctx context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	alertThreshold := now.Add(-s.alertThreshold)

	// Check all known pollers
	for _, pollerID := range s.knownPollers {
		if err := s.checkPollerHealth(ctx, pollerID, alertThreshold); err != nil {
			log.Printf("Error checking poller %s: %v", pollerID, err)
		}
	}

	return nil
}

func (s *Server) checkPollerHealth(ctx context.Context, pollerID string, threshold time.Time) error {
	// Get last known status
	var lastSeen time.Time

	var isHealthy bool

	err := s.db.QueryRow(`
        SELECT last_seen, is_healthy 
        FROM nodes 
        WHERE node_id = ?
    `, pollerID).Scan(&lastSeen, &isHealthy)

	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to query poller status: %w", err)
		}
		// If no rows, treat as never seen
		lastSeen = time.Time{}
		isHealthy = false
	}

	// Check if the poller has gone offline
	if lastSeen.Before(threshold) && isHealthy {
		log.Printf("Poller %s appears to be offline (last seen: %v)",
			pollerID, lastSeen.Format(time.RFC3339))

		if err := s.markNodeDown(ctx, pollerID, lastSeen); err != nil {
			return fmt.Errorf("failed to mark poller as down: %w", err)
		}
	}

	return nil
}

func (s *Server) Shutdown(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()

	if err := s.db.Close(); err != nil {
		log.Printf("Error closing database: %v", err)
	}

	if len(s.webhooks) > 0 {
		alert := alerts.WebhookAlert{
			Level:     alerts.Warning,
			Title:     "Cloud Service Stopping",
			Message:   fmt.Sprintf("ServiceRadar cloud service shutting down at %s", time.Now().Format(time.RFC3339)),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			NodeID:    "cloud",
			Details: map[string]any{
				"hostname": getHostname(),
				"pid":      os.Getpid(),
			},
		}

		err := s.sendAlert(ctx, &alert)
		if err != nil {
			log.Printf("Error sending shutdown alert: %v", err)

			return
		}
	}

	close(s.ShutdownChan)
}

func (s *Server) SetAPIServer(apiServer *api.APIServer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.apiServer = apiServer

	// Set up the history handler
	apiServer.SetNodeHistoryHandler(func(nodeID string) ([]api.NodeHistoryPoint, error) {
		points, err := s.db.GetNodeHistoryPoints(nodeID, nodeHistoryLimit)
		if err != nil {
			return nil, fmt.Errorf("failed to get node history: %w", err)
		}

		// Convert db.NodeHistoryPoint to api.NodeHistoryPoint
		apiPoints := make([]api.NodeHistoryPoint, len(points))
		for i, p := range points {
			apiPoints[i] = api.NodeHistoryPoint{
				Timestamp: p.Timestamp,
				IsHealthy: p.IsHealthy,
			}
		}

		return apiPoints, nil
	})
}

func (s *Server) checkInitialStates(ctx context.Context) {
	log.Printf("Checking initial states of all nodes")

	// Query all nodes from database
	const querySQL = `
        SELECT node_id, is_healthy, last_seen 
        FROM nodes 
        ORDER BY last_seen DESC
    `

	rows, err := s.db.Query(querySQL) //nolint:rowserrcheck // rows.Close() is deferred
	if err != nil {
		log.Printf("Error querying nodes: %v", err)
		return
	}

	defer func(rows *sql.Rows) {
		err := rows.Close()
		if err != nil {
			log.Printf("Error closing rows: %v", err)
		}
	}(rows)

	checkedNodes := make(map[string]bool)

	for rows.Next() {
		var nodeID string

		var isHealthy bool

		var lastSeen time.Time

		if err := rows.Scan(&nodeID, &isHealthy, &lastSeen); err != nil {
			log.Printf("Error scanning node row: %v", err)
			continue
		}

		checkedNodes[nodeID] = true
		duration := time.Since(lastSeen)

		if duration > s.alertThreshold {
			log.Printf("Node %s found offline during initial check (last seen: %v ago)",
				nodeID, duration.Round(time.Second))

			err := s.markNodeDown(ctx, nodeID, time.Now())
			if err != nil {
				log.Printf("Error marking node down: %v", err)
				return
			}
		}
	}

	// Check for known pollers that have never reported
	for _, pollerID := range s.knownPollers {
		if !checkedNodes[pollerID] {
			err := s.markNodeDown(ctx, pollerID, time.Now())
			if err != nil {
				log.Printf("Error marking node down: %v", err)

				return
			}
		}
	}
}

// ReportStatus handles incoming status reports from pollers.
func (s *Server) ReportStatus(ctx context.Context, req *proto.PollerStatusRequest) (*proto.PollerStatusResponse, error) {
	if req.PollerId == "" {
		return nil, errEmptyPollerID
	}

	now := time.Unix(req.Timestamp, 0)

	log.Printf("Received status report from %s with timestamp: %s", req.PollerId, now.Format(time.RFC3339))

	apiStatus, err := s.processStatusReport(ctx, req, now)
	if err != nil {
		return nil, fmt.Errorf("failed to process status report: %w", err)
	}

	s.updateAPIState(req.PollerId, apiStatus)

	return &proto.PollerStatusResponse{Received: true}, nil
}

// updateAPIState updates the API server with the latest node status.
func (s *Server) updateAPIState(pollerID string, apiStatus *api.NodeStatus) {
	if s.apiServer == nil {
		log.Printf("Warning: API server not initialized, state not updated")

		return
	}

	s.apiServer.UpdateNodeStatus(pollerID, apiStatus)

	log.Printf("Updated API server state for node: %s", pollerID)
}

// getNodeHealthState retrieves the current health state of a node.
func (s *Server) getNodeHealthState(pollerID string) (bool, error) {
	var currentState bool

	err := s.db.QueryRow("SELECT is_healthy FROM nodes WHERE node_id = ?", pollerID).Scan(&currentState)

	return currentState, err
}

func (s *Server) processStatusReport(
	ctx context.Context, req *proto.PollerStatusRequest, now time.Time) (*api.NodeStatus, error) {
	currentState, err := s.getNodeHealthState(req.PollerId)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("Error checking node state: %v", err)
	}

	apiStatus := s.createNodeStatus(req, now)

	s.processServices(req.PollerId, apiStatus, req.Services, now)

	if err := s.updateNodeState(ctx, req.PollerId, apiStatus, currentState, now); err != nil {
		return nil, err
	}

	return apiStatus, nil
}

func (*Server) createNodeStatus(req *proto.PollerStatusRequest, now time.Time) *api.NodeStatus {
	return &api.NodeStatus{
		NodeID:     req.PollerId,
		LastUpdate: now,
		IsHealthy:  true,
		Services:   make([]api.ServiceStatus, 0, len(req.Services)),
	}
}

func (s *Server) processServices(pollerID string, apiStatus *api.NodeStatus, services []*proto.ServiceStatus, now time.Time) {
	for _, svc := range services {
		apiService := api.ServiceStatus{
			Name:      svc.ServiceName,
			Type:      svc.ServiceType,
			Available: svc.Available,
			Message:   svc.Message,
		}

		if !svc.Available {
			apiStatus.IsHealthy = false
		}

		// Process JSON details if available
		if svc.Message != "" {
			var details json.RawMessage
			if err := json.Unmarshal([]byte(svc.Message), &details); err == nil {
				apiService.Details = details
			}
		}

		if err := s.handleService(pollerID, &apiService, now); err != nil {
			log.Printf("Error handling service %s: %v", svc.ServiceName, err)
		}

		apiStatus.Services = append(apiStatus.Services, apiService)
	}
}

func (s *Server) handleService(pollerID string, svc *api.ServiceStatus, now time.Time) error {
	if svc.Type == sweepService {
		if err := s.processSweepData(svc, now); err != nil {
			return fmt.Errorf("failed to process sweep data: %w", err)
		}
	}

	return s.saveServiceStatus(pollerID, svc, now)
}

func (*Server) processSweepData(svc *api.ServiceStatus, now time.Time) error {
	var sweepData proto.SweepServiceStatus
	if err := json.Unmarshal([]byte(svc.Message), &sweepData); err != nil {
		return fmt.Errorf("%w: %w", errInvalidSweepData, err)
	}

	log.Printf("Received sweep data with timestamp: %v", time.Unix(sweepData.LastSweep, 0).Format(time.RFC3339))

	// If LastSweep is not set or is invalid (0 or negative), use current time
	if sweepData.LastSweep > now.Add(oneDay).Unix() {
		log.Printf("Invalid or missing LastSweep timestamp (%d), using current time", sweepData.LastSweep)
		sweepData.LastSweep = now.Unix()

		// Update the message with corrected timestamp
		updatedData := proto.SweepServiceStatus{
			Network:        sweepData.Network,
			TotalHosts:     sweepData.TotalHosts,
			AvailableHosts: sweepData.AvailableHosts,
			LastSweep:      now.Unix(),
		}

		updatedMessage, err := json.Marshal(&updatedData)
		if err != nil {
			return fmt.Errorf("failed to marshal updated sweep data: %w", err)
		}

		svc.Message = string(updatedMessage)

		log.Printf("Updated sweep data with current timestamp: %v", now.Format(time.RFC3339))
	} else {
		// Log the existing timestamp for debugging
		log.Printf("Processing sweep data with timestamp: %v",
			time.Unix(sweepData.LastSweep, 0).Format(time.RFC3339))
	}

	return nil
}

func (s *Server) saveServiceStatus(pollerID string, svc *api.ServiceStatus, now time.Time) error {
	status := &db.ServiceStatus{
		NodeID:      pollerID,
		ServiceName: svc.Name,
		ServiceType: svc.Type,
		Available:   svc.Available,
		Details:     svc.Message,
		Timestamp:   now,
	}

	if err := s.db.UpdateServiceStatus(status); err != nil {
		return fmt.Errorf("%w: failed to update service status", errDatabaseError)
	}

	return nil
}

// storeNodeStatus updates the node status in the database.
func (s *Server) storeNodeStatus(pollerID string, isHealthy bool, now time.Time) error {
	nodeStatus := &db.NodeStatus{
		NodeID:    pollerID,
		IsHealthy: isHealthy,
		LastSeen:  now,
	}

	if err := s.db.UpdateNodeStatus(nodeStatus); err != nil {
		return fmt.Errorf("failed to store node status: %w", err)
	}

	return nil
}

// handleNodeRecovery processes a node recovery event.
func (s *Server) handleNodeRecovery(ctx context.Context, pollerID string, apiStatus *api.NodeStatus, now time.Time) {
	log.Printf("Node %s has recovered, last seen at %s", pollerID, now.Format(time.RFC3339))

	lastDownTime := s.getLastDowntime(pollerID)

	downtimeDuration := statusUnknown

	if !lastDownTime.IsZero() {
		downtimeDuration = now.Sub(lastDownTime).String()
	}

	alert := &alerts.WebhookAlert{
		Level:     alerts.Info,
		Title:     "Node Recovered",
		Message:   fmt.Sprintf("Node '%s' is back online", pollerID),
		NodeID:    pollerID,
		Timestamp: now.UTC().Format(time.RFC3339),
		Details: map[string]any{
			"hostname":      getHostname(),
			"downtime":      downtimeDuration,
			"recovery_time": now.Format(time.RFC3339),
			"services":      len(apiStatus.Services),
		},
	}

	err := s.sendAlert(ctx, alert)
	if err != nil {
		log.Printf("Error sending recovery alert: %v", err)
		return
	}
}

func (s *Server) updateNodeState(ctx context.Context, pollerID string, apiStatus *api.NodeStatus, wasHealthy bool, now time.Time) error {
	if err := s.storeNodeStatus(pollerID, apiStatus.IsHealthy, now); err != nil {
		return err
	}

	// Check for recovery
	if !wasHealthy && apiStatus.IsHealthy {
		s.handleNodeRecovery(ctx, pollerID, apiStatus, now)
	}

	return nil
}

// sendNodeDownAlert sends an alert when a node goes down.
func (s *Server) sendNodeDownAlert(ctx context.Context, nodeID string, lastSeen time.Time) {
	alert := &alerts.WebhookAlert{
		Level:     alerts.Error,
		Title:     "Node Offline",
		Message:   fmt.Sprintf("Node '%s' is offline", nodeID),
		NodeID:    nodeID,
		Timestamp: lastSeen.UTC().Format(time.RFC3339),
		Details: map[string]any{
			"hostname": getHostname(),
			"duration": time.Since(lastSeen).String(),
		},
	}

	err := s.sendAlert(ctx, alert)
	if err != nil {
		log.Printf("Error sending alert: %v", err)
		return
	}
}

// updateAPINodeStatus updates the node status in the API server.
func (s *Server) updateAPINodeStatus(nodeID string, isHealthy bool, timestamp time.Time) {
	if s.apiServer != nil {
		status := &api.NodeStatus{
			NodeID:     nodeID,
			IsHealthy:  isHealthy,
			LastUpdate: timestamp,
		}
		s.apiServer.UpdateNodeStatus(nodeID, status)
	}
}

// markNodeDown handles marking a node as down and sending alerts.
func (s *Server) markNodeDown(ctx context.Context, nodeID string, lastSeen time.Time) error {
	if err := s.updateNodeDownStatus(nodeID, lastSeen); err != nil {
		return err
	}

	s.sendNodeDownAlert(ctx, nodeID, lastSeen)
	s.updateAPINodeStatus(nodeID, false, lastSeen)

	return nil
}

func (s *Server) updateNodeDownStatus(nodeID string, lastSeen time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func(tx *sql.Tx) {
		err := tx.Rollback()
		if err != nil {
			log.Printf("Error rolling back transaction: %v", err)
		}
	}(tx)

	if err := s.performNodeUpdate(tx, nodeID, lastSeen); err != nil {
		return err
	}

	return tx.Commit()
}

// checkNodeExists verifies if a node exists in the database.
func (*Server) checkNodeExists(tx *sql.Tx, nodeID string) (bool, error) {
	var exists bool

	err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM nodes WHERE node_id = ?)", nodeID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check node existence: %w", err)
	}

	return exists, nil
}

// insertNewNode adds a new node to the database.
func (*Server) insertNewNode(tx *sql.Tx, nodeID string, lastSeen time.Time) error {
	_, err := tx.Exec(`
        INSERT INTO nodes (node_id, last_seen, is_healthy)
        VALUES (?, ?, FALSE)`,
		nodeID, lastSeen)
	if err != nil {
		return fmt.Errorf("failed to insert new node: %w", err)
	}

	return nil
}

// updateExistingNode updates an existing node's status.
func (*Server) updateExistingNode(tx *sql.Tx, nodeID string, lastSeen time.Time) error {
	_, err := tx.Exec(`
        UPDATE nodes 
        SET is_healthy = FALSE, 
            last_seen = ? 
        WHERE node_id = ?`,
		lastSeen, nodeID)
	if err != nil {
		return fmt.Errorf("failed to update existing node: %w", err)
	}

	return nil
}

func (s *Server) performNodeUpdate(tx *sql.Tx, nodeID string, lastSeen time.Time) error {
	exists, err := s.checkNodeExists(tx, nodeID)
	if err != nil {
		return err
	}

	if !exists {
		return s.insertNewNode(tx, nodeID, lastSeen)
	}

	return s.updateExistingNode(tx, nodeID, lastSeen)
}

func (s *Server) initializeWebhooks(configs []alerts.WebhookConfig) {
	for i, config := range configs {
		log.Printf("Processing webhook config %d: enabled=%v", i, config.Enabled)

		if config.Enabled {
			alerter := alerts.NewWebhookAlerter(config)
			s.webhooks = append(s.webhooks, alerter)

			log.Printf("Added webhook alerter: %+v", config.URL)
		}
	}
}

// periodicCleanup runs regular maintenance tasks on the database.
func (s *Server) periodicCleanup(_ context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-s.ShutdownChan:
			return
		case <-ticker.C:
			// Clean up old data (keep 7 days by default)
			if err := s.db.CleanOldData(7 * 24 * time.Hour); err != nil {
				log.Printf("Error during periodic cleanup: %v", err)
			}

			// Vacuum the database every 24 hours to reclaim space
			if time.Now().Hour() == 0 { // Run at midnight
				if _, err := s.db.Exec("VACUUM"); err != nil {
					log.Printf("Error vacuuming database: %v", err)
				}
			}
		}
	}
}

func (s *Server) checkNeverReportedPollers(ctx context.Context, config *Config) {
	const querySQL = `
        SELECT node_id 
        FROM nodes 
        WHERE node_id = ?
    `

	for _, pollerID := range config.KnownPollers {
		var exists string

		err := s.db.QueryRow(querySQL, pollerID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			alert := alerts.WebhookAlert{
				Level:   alerts.Error,
				Title:   fmt.Sprintf("Poller Never Reported: %s", pollerID),
				Message: fmt.Sprintf("Expected poller %s has not reported after startup.", pollerID),
				NodeID:  pollerID,
				Details: map[string]any{
					"hostname": getHostname(),
				},
			}

			log.Printf("Sending 'never-reported' alert for poller %s", pollerID)

			err := s.sendAlert(ctx, &alert)
			if err != nil {
				log.Printf("Error sending 'never-reported' alert: %v", err)
				return
			}
		}
	}
}

func (c *Config) UnmarshalJSON(data []byte) error {
	type Alias Config

	aux := &struct {
		AlertThreshold string `json:"alert_threshold"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Parse the alert threshold
	if aux.AlertThreshold != "" {
		duration, err := time.ParseDuration(aux.AlertThreshold)
		if err != nil {
			return fmt.Errorf("invalid alert threshold format: %w", err)
		}

		c.AlertThreshold = duration
	}

	return nil
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("failed to read config: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("failed to parse config: %w", err)
	}

	return config, nil
}

func (s *Server) MonitorPollers(ctx context.Context) {
	ticker := time.NewTicker(pollerTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := s.checkPollers(ctx)
			if err != nil {
				log.Printf("Error checking pollers: %v", err)
				return
			}
		case <-time.After(oneDay):
			// Daily cleanup of old data
			if err := s.db.CleanOldData(oneWeek); err != nil {
				log.Printf("Error cleaning old data: %v", err)
			}
		}
	}
}

func (s *Server) getLastDowntime(nodeID string) time.Time {
	var downtime time.Time
	err := s.db.QueryRow(`
        SELECT timestamp
        FROM node_history
        WHERE node_id = ? AND is_healthy = FALSE
        ORDER BY timestamp DESC
        LIMIT 1
    `, nodeID).Scan(&downtime)

	if err != nil {
		log.Printf("Error getting last downtime for node %s: %v", nodeID, err)
		return time.Time{} // Return zero time if error
	}

	return downtime
}

func (s *Server) sendAlert(ctx context.Context, alert *alerts.WebhookAlert) error {
	for _, webhook := range s.webhooks {
		if err := webhook.Alert(ctx, alert); err != nil {
			log.Printf("Error sending webhook alert: %v", err)
		}
	}

	return nil
}

func getHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}

	return hostname
}
