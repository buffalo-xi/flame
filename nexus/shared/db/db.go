package db

import (
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	"flame/nexus/shared/config"
	"flame/tools/xlog"

	_ "github.com/lib/pq" // PostgreSQL driver
)

// DB is the global database connection.
var DB *sql.DB

// dbConnected is the connection state (atomic, so concurrent readers and writers do not race).
var dbConnected atomic.Bool

// IsConnected returns the database connection state.
func IsConnected() bool { return dbConnected.Load() }

// OpenDatabase opens a database connection, sets the pool parameters and Pings right away to verify connectivity.
// It is reused by the main database and by separate ones such as LiteLLM's, so sql.Open + pool setup is not repeated
// everywhere.
func OpenDatabase(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Pool parameters
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Test the connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}
	return db, nil
}

// InitDatabase initialises the database connection.
func InitDatabase() error {
	cfg := config.Cfg
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}

	// Build the connection string
	connStr := cfg.Database.GetDSN()

	var err error
	DB, err = OpenDatabase(connStr)
	if err != nil {
		return err
	}

	dbConnected.Store(true)
	xlog.Info("Database connected successfully")
	return nil
}

// CheckDatabaseConnection checks the database connection state.
func CheckDatabaseConnection() bool {
	if DB == nil {
		dbConnected.Store(false)
		return false
	}

	err := DB.Ping()
	if err != nil {
		dbConnected.Store(false)
		return false
	}
	return true
}

// ReconnectDatabase reconnects to the database.
func ReconnectDatabase() bool {
	err := InitDatabase()
	if err != nil {
		xlog.Error("Failed to reconnect to database: %v", err)
		return false
	}
	return true
}

// CloseDatabase closes the database connection.
func CloseDatabase() error {
	if DB != nil {
		return DB.Close()
	}
	return nil
}
