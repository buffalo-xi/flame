package db

import (
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	"flame/nexus/shared/config"
	"flame/tools/xlog"

	_ "github.com/lib/pq" // PostgreSQL驱动
)

// DB 全局数据库连接
var DB *sql.DB

// dbConnected 数据库连接状态（atomic，避免并发读写竞争）
var dbConnected atomic.Bool

// IsConnected 返回数据库连接状态
func IsConnected() bool { return dbConnected.Load() }

// OpenDatabase 打开一个数据库连接并设置连接池参数，建连后立即 Ping 校验连通性。
// 供主库与 LiteLLM 独立库等复用，避免各处重复 sql.Open + 连接池配置。
func OpenDatabase(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// 设置连接池参数
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	// 测试连接
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}
	return db, nil
}

// InitDatabase 初始化数据库连接
func InitDatabase() error {
	cfg := config.Cfg
	if cfg == nil {
		return fmt.Errorf("config not initialized")
	}

	// 构建连接字符串
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

// CheckDatabaseConnection 检查数据库连接状态
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

// ReconnectDatabase 重新连接数据库
func ReconnectDatabase() bool {
	err := InitDatabase()
	if err != nil {
		xlog.Error("Failed to reconnect to database: %v", err)
		return false
	}
	return true
}

// CloseDatabase 关闭数据库连接
func CloseDatabase() error {
	if DB != nil {
		return DB.Close()
	}
	return nil
}
