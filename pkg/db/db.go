// Package db pkg/db/db.go provides SQLite database functionality for HomeMon
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var (
	errFailedToClean     = errors.New("failed to clean")
	errFailedToBeginTx   = errors.New("failed to begin transaction")
	errFailedToScan      = errors.New("failed to scan")
	errFailedToQuery     = errors.New("failed to query")
	errFailedToInsert    = errors.New("failed to insert")
	errFailedToTrim      = errors.New("failed to trim")
	errFailedToUpsert    = errors.New("failed to upsert node")
	errFailedToRollback  = errors.New("failed to rollback")
	errFailedToInit      = errors.New("failed to initialize schema")
	errFailedToEnableWAL = errors.New("failed to enable WAL mode")
	errFailedOpenDB      = fmt.Errorf("failed to open database")
)

const (
	// Maximum number of history points to keep per node.
	maxHistoryPoints = 1000

	// SQL statements for database initialization.
	createTablesSQL = `
	-- Node information
	CREATE TABLE IF NOT EXISTS nodes (
		node_id TEXT PRIMARY KEY,
		first_seen TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_seen TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		is_healthy BOOLEAN NOT NULL DEFAULT 0
	);

	-- Node status history
	CREATE TABLE IF NOT EXISTS node_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		node_id TEXT NOT NULL,
		timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		is_healthy BOOLEAN NOT NULL DEFAULT 0,
		FOREIGN KEY (node_id) REFERENCES nodes(node_id) ON DELETE CASCADE
	);

	-- Service status
	CREATE TABLE IF NOT EXISTS service_status (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		node_id TEXT NOT NULL,
		service_name TEXT NOT NULL,
		service_type TEXT NOT NULL,
		available BOOLEAN NOT NULL DEFAULT 0,
		details TEXT,
		timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (node_id) REFERENCES nodes(node_id) ON DELETE CASCADE
	);

	-- Service history
	CREATE TABLE IF NOT EXISTS service_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		service_status_id INTEGER NOT NULL,
		timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		available BOOLEAN NOT NULL DEFAULT 0,
		details TEXT,
		FOREIGN KEY (service_status_id) REFERENCES service_status(id) ON DELETE CASCADE
	);

	-- Indexes for better query performance
	CREATE INDEX IF NOT EXISTS idx_node_history_node_time 
		ON node_history(node_id, timestamp);
	CREATE INDEX IF NOT EXISTS idx_service_status_node_time 
		ON service_status(node_id, timestamp);
	CREATE INDEX IF NOT EXISTS idx_service_status_type 
		ON service_status(service_type);
	CREATE INDEX IF NOT EXISTS idx_service_history_status_time 
		ON service_history(service_status_id, timestamp);

	-- Enable WAL mode for better concurrent access
	PRAGMA journal_mode=WAL;
	PRAGMA foreign_keys=ON;
	`

	// SQL to trim old history points.
	trimHistorySQL = `
		WITH RankedHistory AS (
			SELECT id,
				   ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY timestamp DESC) as rn
			FROM node_history
			WHERE node_id = ?
		)
		DELETE FROM node_history
		WHERE id IN (
			SELECT id FROM RankedHistory WHERE rn > ?
		);
	`
)

// DB represents the database connection and operations.
type DB struct {
	*sql.DB
}

// NodeStatus represents a node's current status.
type NodeStatus struct {
	NodeID    string    `json:"node_id"`
	IsHealthy bool      `json:"is_healthy"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// ServiceStatus represents a service's status.
type ServiceStatus struct {
	NodeID      string    `json:"node_id"`
	ServiceName string    `json:"service_name"`
	ServiceType string    `json:"service_type"`
	Available   bool      `json:"available"`
	Details     string    `json:"details"`
	Timestamp   time.Time `json:"timestamp"`
}

// New creates a new database connection and initializes the schema.
func New(dbPath string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errFailedOpenDB, err)
	}

	// Enable WAL mode for better concurrent access
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("%w: %w", errFailedToEnableWAL, err)
	}

	db := &DB{sqlDB}
	if err := db.initSchema(); err != nil {
		return nil, fmt.Errorf("%w: %w", errFailedToInit, err)
	}

	return db, nil
}

// initSchema creates the database tables if they don't exist.
func (db *DB) initSchema() error {
	_, err := db.Exec(createTablesSQL)

	return err
}

func (db *DB) UpdateNodeStatus(status *NodeStatus) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer rollbackOnError(tx, &err)

	err = db.updateExistingNode(tx, status)
	if errors.Is(err, sql.ErrNoRows) {
		err = db.insertNewNode(tx, status)
	}
	if err != nil {
		return fmt.Errorf("failed to update node status: %w", err)
	}

	err = db.addNodeHistory(tx, status)
	if err != nil {
		return fmt.Errorf("failed to add node history: %w", err)
	}

	return tx.Commit()
}

func (db *DB) updateExistingNode(tx *sql.Tx, status *NodeStatus) error {
	result, err := tx.Exec(`
        UPDATE nodes 
        SET last_seen = ?,
            is_healthy = ?
        WHERE node_id = ?
    `, status.LastSeen, status.IsHealthy, status.NodeID)
	if err != nil {
		return fmt.Errorf("failed to update node: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func (db *DB) insertNewNode(tx *sql.Tx, status *NodeStatus) error {
	_, err := tx.Exec(`
        INSERT INTO nodes (node_id, first_seen, last_seen, is_healthy)
        VALUES (?, CURRENT_TIMESTAMP, ?, ?)
    `, status.NodeID, status.LastSeen, status.IsHealthy)

	if err != nil {
		return fmt.Errorf("failed to insert node: %w", err)
	}
	return nil
}

func (db *DB) addNodeHistory(tx *sql.Tx, status *NodeStatus) error {
	_, err := tx.Exec(`
        INSERT INTO node_history (node_id, timestamp, is_healthy)
        VALUES (?, ?, ?)
    `, status.NodeID, status.LastSeen, status.IsHealthy)

	if err != nil {
		return fmt.Errorf("failed to insert history: %w", err)
	}
	return nil
}

func rollbackOnError(tx *sql.Tx, err *error) {
	if *err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			log.Printf("Error rolling back transaction: %v", rbErr)
		}
	}
}

// UpdateServiceStatus updates a service's status.
func (db *DB) UpdateServiceStatus(status *ServiceStatus) error {
	const insertSQL = `
		INSERT INTO service_status 
			(node_id, service_name, service_type, available, details, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)
	`

	_, err := db.Exec(insertSQL,
		status.NodeID,
		status.ServiceName,
		status.ServiceType,
		status.Available,
		status.Details,
		status.Timestamp)

	if err != nil {
		return fmt.Errorf("%w service status: %w", errFailedToInsert, err)
	}

	return nil
}

func (db *DB) GetNodeStatus(nodeID string) (*NodeStatus, error) {
	const query = `
        SELECT node_id, first_seen, last_seen, is_healthy
        FROM nodes
        WHERE node_id = ?
    `

	var status NodeStatus
	err := db.QueryRow(query, nodeID).Scan(
		&status.NodeID,
		&status.FirstSeen,
		&status.LastSeen,
		&status.IsHealthy,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query node status: %w", err)
	}

	return &status, nil
}

func (db *DB) GetNodeServices(nodeID string) ([]ServiceStatus, error) {
	const querySQL = `
        SELECT service_name, service_type, available, details, timestamp
        FROM service_status
        WHERE node_id = ?
        ORDER BY service_type, service_name
    `

	rows, err := db.Query(querySQL, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query services: %w", err)
	}
	defer rows.Close()

	var services []ServiceStatus
	for rows.Next() {
		var s ServiceStatus
		s.NodeID = nodeID
		if err := rows.Scan(&s.ServiceName, &s.ServiceType, &s.Available, &s.Details, &s.Timestamp); err != nil {
			return nil, fmt.Errorf("failed to scan service: %w", err)
		}
		services = append(services, s)
	}

	return services, nil
}

// GetNodeHistory retrieves the history for a node.
func (db *DB) GetNodeHistory(nodeID string) ([]NodeStatus, error) {
	const querySQL = `
		SELECT timestamp, is_healthy
		FROM node_history
		WHERE node_id = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`

	rows, err := db.Query(querySQL, nodeID, maxHistoryPoints)
	if err != nil {
		return nil, fmt.Errorf("%w node history: %w", errFailedToQuery, err)
	}

	defer func(rows *sql.Rows) {
		err := rows.Close()
		if err != nil {
			log.Printf("failed to close rows: %v", err)
		}
	}(rows)

	var history []NodeStatus

	for rows.Next() {
		var h NodeStatus

		h.NodeID = nodeID

		if err := rows.Scan(&h.LastSeen, &h.IsHealthy); err != nil {
			return nil, fmt.Errorf("%w history row: %w", errFailedToScan, err)
		}

		history = append(history, h)
	}

	return history, nil
}

// GetServiceHistory retrieves the recent history for a service.
func (db *DB) GetServiceHistory(nodeID, serviceName string, limit int) ([]ServiceStatus, error) {
	const querySQL = `
		SELECT timestamp, available, details
		FROM service_status
		WHERE node_id = ? AND service_name = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`

	rows, err := db.Query(querySQL, nodeID, serviceName, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query service history: %w", err)
	}

	defer func(rows *sql.Rows) {
		err := rows.Close()
		if err != nil {
			log.Printf("failed to close rows: %v", err)
		}
	}(rows)

	var history []ServiceStatus

	for rows.Next() {
		var s ServiceStatus
		s.NodeID = nodeID
		s.ServiceName = serviceName

		if err := rows.Scan(&s.Timestamp, &s.Available, &s.Details); err != nil {
			return nil, fmt.Errorf("failed to scan service history row: %w", err)
		}

		history = append(history, s)
	}

	return history, nil
}

// CleanOldData removes data older than the retention period.
func (db *DB) CleanOldData(retentionPeriod time.Duration) error {
	cutoff := time.Now().Add(-retentionPeriod)

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("%w: %w", errFailedToBeginTx, err)
	}

	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				log.Printf("failed to rollback: %v", rbErr)
			}

			return
		}

		err = tx.Commit()
	}()

	// Clean up node history
	if _, err := tx.Exec(
		"DELETE FROM node_history WHERE timestamp < ?",
		cutoff,
	); err != nil {
		return fmt.Errorf("%w node history %w", errFailedToClean, err)
	}

	// Clean up service status
	if _, err := tx.Exec(
		"DELETE FROM service_status WHERE timestamp < ?",
		cutoff,
	); err != nil {
		return fmt.Errorf("%w service status: %w", errFailedToClean, err)
	}

	return nil
}