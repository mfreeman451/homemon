package dusk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mfreeman451/serviceradar/pkg/config"
	"github.com/mfreeman451/serviceradar/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	lengthPrefixSize = 4 // 4
	blockHistorySize = 100
)

var (
	errSubscriptionFail = fmt.Errorf("subscription failed")
	errMsgTooShort      = fmt.Errorf("message too short")
)

type BlockData struct {
	Height    uint64    `json:"height"`
	Hash      string    `json:"hash"`
	Timestamp time.Time `json:"timestamp"`
	LastSeen  time.Time `json:"last_seen"`
}

type DuskChecker struct {
	Config        Config
	ws            *websocket.Conn
	sessionID     string
	lastBlock     time.Time
	mu            sync.RWMutex
	Done          chan struct{}
	lastBlockData BlockData
	blockHistory  []BlockData // Keep last N blocks
}

type HealthServer struct {
	grpc_health_v1.UnimplementedHealthServer
	checker *DuskChecker
}

// DuskBlockService provides block data via gRPC.
type DuskBlockService struct {
	proto.UnimplementedAgentServiceServer
	checker *DuskChecker
}

func NewDuskBlockService(checker *DuskChecker) *DuskBlockService {
	return &DuskBlockService{
		checker: checker,
	}
}

// GetStatus implements the AgentService GetStatus method.
func (s *DuskBlockService) GetStatus(ctx context.Context, _ *proto.StatusRequest) (*proto.StatusResponse, error) {
	s.checker.mu.RLock()
	defer s.checker.mu.RUnlock()

	// Cast config.Duration -> time.Duration
	timeout := time.Duration(s.checker.Config.Timeout)

	_, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	log.Printf("DuskBlockService.GetStatus called. Last block: Height=%d Hash=%s",
		s.checker.lastBlockData.Height,
		s.checker.lastBlockData.Hash)

	if s.checker.ws == nil {
		return &proto.StatusResponse{
			Available: false,
			Message:   "WebSocket connection not established",
		}, nil
	}

	// Create block details structure
	blockData := map[string]interface{}{
		"height":    s.checker.lastBlockData.Height,
		"hash":      s.checker.lastBlockData.Hash,
		"timestamp": s.checker.lastBlockData.Timestamp.Format(time.RFC3339),
		"last_seen": s.checker.lastBlockData.LastSeen.Format(time.RFC3339),
	}

	blockDetailsJSON, err := json.Marshal(blockData)
	if err != nil {
		log.Printf("Error marshaling block details: %v", err)

		return &proto.StatusResponse{
			Available: true,
			Message:   "Dusk node is healthy but failed to marshal block details",
		}, nil
	}

	log.Printf("DuskBlockService: Returning block details: %s", string(blockDetailsJSON))

	return &proto.StatusResponse{
		Available: true,
		Message:   string(blockDetailsJSON),
	}, nil
}

func (c *Config) UnmarshalJSON(data []byte) error {
	type Alias Config // Create alias to avoid recursion

	aux := &struct {
		Timeout string `json:"timeout"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Parse the timeout string into a time.Duration, then cast it
	if aux.Timeout != "" {
		duration, err := time.ParseDuration(aux.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout format: %w", err)
		}

		// Cast the parsed time.Duration to config.Duration
		c.Timeout = config.Duration(duration)
	}

	return nil
}

func (d *DuskChecker) StartMonitoring(ctx context.Context) error {
	log.Printf("Connecting to Dusk node at %s", d.Config.NodeAddress)

	// Establish WebSocket connection
	wsConn, err := d.establishWebSocketConnection()
	if err != nil {
		return fmt.Errorf("websocket connection failed: %w", err)
	}

	d.ws = wsConn

	// Get session ID
	sessionID, err := d.getSessionID()
	if err != nil {
		return fmt.Errorf("session ID retrieval failed: %w", err)
	}

	d.sessionID = sessionID

	// Subscribe to block events
	if err := d.subscribeToBlocks(ctx); err != nil {
		return fmt.Errorf("block subscription failed: %w", err)
	}

	// Start listening for events
	go d.listenForEvents()

	return nil
}

func (d *DuskChecker) establishWebSocketConnection() (*websocket.Conn, error) {
	u := url.URL{Scheme: "ws", Host: d.Config.NodeAddress, Path: "/on"}

	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		if resp != nil {
			// Even on error, if we got a response, we must close its body
			closeErr := resp.Body.Close()
			if closeErr != nil {
				return nil, fmt.Errorf("failed to close error response: %w", closeErr)
			}
		}

		return nil, fmt.Errorf("websocket dial failed: %w", err)
	}

	// Successful connection upgrade also requires the response body to be closed
	if resp != nil {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("failed to close successful response: %w", closeErr)
		}
	}

	return conn, nil
}

func (d *DuskChecker) getSessionID() (string, error) {
	_, message, err := d.ws.ReadMessage()
	if err != nil {
		closeErr := d.ws.Close()
		if closeErr != nil {
			log.Printf("Error closing websocket after session ID failure: %v", closeErr)
		}

		return "", fmt.Errorf("failed to read session ID: %w", err)
	}

	sessionID := string(message)
	log.Printf("Received session ID: %s", sessionID)

	return sessionID, nil
}

func (d *DuskChecker) subscribeToBlocks(ctx context.Context) error {
	client := &http.Client{}
	subscribeURL := fmt.Sprintf("http://%s/on/blocks/accepted", d.Config.NodeAddress)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, subscribeURL, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to create subscription request: %w", err)
	}

	// Add required headers as per RUES spec
	req.Header.Set("Rusk-Version", "1.0")
	req.Header.Set("Rusk-Session-Id", d.sessionID)

	log.Printf("Sending subscription request to: %s", subscribeURL)
	log.Printf("With headers: %v", req.Header)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("subscription request failed: %w", err)
	}

	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Error closing response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			log.Printf("Error reading error response body: %v", readErr)

			body = []byte("failed to read error response body")
		}

		return fmt.Errorf("monitoring failed: %d %w %s", resp.StatusCode, errSubscriptionFail, string(body))
	}

	log.Printf("Successfully subscribed to block events")

	return nil
}

func (d *DuskChecker) listenForEvents() {
	log.Printf("Starting event listener for websocket connection")

	defer func(ws *websocket.Conn) {
		err := ws.Close()
		if err != nil {
			log.Printf("Error closing websocket connection: %v", err)
		}
	}(d.ws)

	readTimeout := 30 * time.Second

	for {
		select {
		case <-d.Done:
			if err := d.ws.Close(); err != nil {
				log.Printf("Error closing websocket connection: %v", err)
			}

			return
		default:
			if err := d.ws.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
				log.Printf("Failed to set read deadline: %v", err)

				return
			}

			messageType, data, err := d.ws.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("Unexpected websocket closure: %v", err)
				} else {
					log.Printf("Websocket error: %v", err)
				}

				return
			}

			if messageType != websocket.BinaryMessage {
				continue
			}

			if err := d.processMessage(data); err != nil {
				log.Printf("Error processing message: %v", err)

				// Don't return here - continue processing messages
				continue
			}
		}
	}
}

func (d *DuskChecker) processMessage(data []byte) error {
	// Check message length
	if len(data) < lengthPrefixSize {
		return fmt.Errorf("%w: %d bytes", errMsgTooShort, len(data))
	}

	jsonData := data[lengthPrefixSize:] // Skip length prefix

	// Use a buffered reader to avoid string conversion
	decoder := json.NewDecoder(bytes.NewReader(jsonData))

	// First try to find the boundary between the two JSON objects
	var firstObj struct {
		ContentLocation string `json:"Content-Location"`
	}

	if err := decoder.Decode(&firstObj); err != nil {
		return fmt.Errorf("error parsing content location: %w", err)
	}

	// Now decode the block data
	var blockData struct {
		Header struct {
			Height    uint64 `json:"height"`
			Hash      string `json:"hash"`
			Timestamp int64  `json:"timestamp"`
		} `json:"header"`
	}

	if err := decoder.Decode(&blockData); err != nil {
		return fmt.Errorf("error parsing block data: %w", err)
	}

	// Use a shorter lock duration by preparing data before locking
	newBlockData := BlockData{
		Height:    blockData.Header.Height,
		Hash:      blockData.Header.Hash,
		Timestamp: time.Unix(blockData.Header.Timestamp, 0),
		LastSeen:  time.Now(),
	}

	d.mu.Lock()
	d.lastBlock = time.Now()
	d.lastBlockData = newBlockData

	// Keep last 100 blocks
	if len(d.blockHistory) >= blockHistorySize {
		d.blockHistory = d.blockHistory[1:]
	}

	d.blockHistory = append(d.blockHistory, newBlockData)
	d.mu.Unlock()

	log.Printf("Block processed: Height=%d Hash=%s Timestamp=%v",
		blockData.Header.Height,
		blockData.Header.Hash,
		time.Unix(blockData.Header.Timestamp, 0))

	return nil
}

func (s *HealthServer) Check(ctx context.Context, _ *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	s.checker.mu.RLock()
	defer s.checker.mu.RUnlock()

	if s.checker.ws == nil {
		log.Printf("Health check failed: WebSocket connection not established")

		return &grpc_health_v1.HealthCheckResponse{
			Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING,
		}, nil
	}

	// Create block details structure
	blockData := map[string]interface{}{
		"height":    s.checker.lastBlockData.Height,
		"hash":      s.checker.lastBlockData.Hash,
		"timestamp": s.checker.lastBlockData.Timestamp.Format(time.RFC3339),
		"last_seen": s.checker.lastBlockData.LastSeen.Format(time.RFC3339),
	}

	// Add detailed logging before sending
	log.Printf("Preparing block details for health check. Height=%d Hash=%s LastSeen=%v",
		s.checker.lastBlockData.Height,
		s.checker.lastBlockData.Hash,
		s.checker.lastBlockData.LastSeen.Format(time.RFC3339))

	blockDetailsJSON, err := json.Marshal(blockData)
	if err != nil {
		log.Printf("Error marshaling block details: %v", err)
	} else {
		// Create new outgoing metadata
		md := metadata.Pairs("block-details", string(blockDetailsJSON))
		// Send metadata back as header
		if err := grpc.SetHeader(ctx, md); err != nil {
			log.Printf("Error setting metadata header: %v", err)
		} else {
			log.Printf("Successfully set block details in metadata: %s", string(blockDetailsJSON))
		}
	}

	return &grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	}, nil
}

// Watch implements the health check watch RPC (required by the interface).
func (*HealthServer) Watch(*grpc_health_v1.HealthCheckRequest, grpc_health_v1.Health_WatchServer) error {
	return status.Error(codes.Unimplemented, "watch is not implemented")
}

func (d *DuskChecker) GetStatusData() json.RawMessage {
	d.mu.RLock()
	defer d.mu.RUnlock()

	data, _ := json.Marshal(d.lastBlock)

	return data
}
