package xlog

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// MaxLogFileSize 最大日志文件大小（30MB）
	MaxLogFileSize = 30 * 1024 * 1024
)

var (
	// InfoLogger 信息日志
	InfoLogger *log.Logger
	// ErrorLogger 错误日志
	ErrorLogger *log.Logger
	// DebugLogger 调试日志
	DebugLogger *log.Logger

	logFile     *os.File
	logDir      string
	filePrefix  string
	currentDate string
	fileSize    int64

	// mirrorConsole 日志是否同时写 stdout（控制台）。
	//
	// 判据是「stdout 是不是连着终端」：直接双击 / 在窗口里跑 → 是，日志上屏；
	// 重定向到文件或由服务管理器接管 → 不是，只落盘，不会出现两份。
	// 需要强制打开/关闭时用环境变量 XLOG_CONSOLE=1 / 0。
	mirrorConsole bool

	mu sync.Mutex
)

// Init 初始化日志系统。prefix 决定日志文件名的前缀（如 "nexus"、"sandboxd"），
// 由调用方按实际服务名传入，最终生成 <prefix>-<date>[-<n>].log。
//
// 日志默认「落盘 + 上屏」双写（上屏仅在 stdout 是终端时生效）。
// 上屏的意义：启动阶段的有效信息（监听地址、生效的隔离方式、浏览器是否可用）
// 本来只写进文件，前台跑服务的人看不到，只能去翻日志——这与「配置必须一眼可见」
// 的初衷相悖。
func Init(dir, prefix string) error {
	logDir = dir
	filePrefix = prefix
	mirrorConsole = wantConsole()
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("failed to create log directory: %v", err)
	}

	mu.Lock()
	err := rotateLocked()
	mu.Unlock()
	if err != nil {
		return err
	}

	w := consoleWriter()
	InfoLogger = log.New(w, "INFO: ", log.Ldate|log.Ltime)
	ErrorLogger = log.New(w, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
	DebugLogger = log.New(w, "DEBUG: ", log.Ldate|log.Ltime|log.Lshortfile)

	// 启动日志检查协程
	go checkLogFile()

	return nil
}

// wantConsole 判断是否要把日志同时打到控制台。
//
// 环境变量优先（XLOG_CONSOLE=1/0），否则看 stdout 是不是字符设备：
// Windows 控制台、Unix 终端都是；重定向到文件 / 管道则不是。
func wantConsole() bool {
	switch os.Getenv("XLOG_CONSOLE") {
	case "1", "true", "on":
		return true
	case "0", "false", "off":
		return false
	}
	st, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func consoleWriter() io.Writer {
	if !mirrorConsole {
		return fileWriter{}
	}
	return io.MultiWriter(fileWriter{}, os.Stdout)
}

// fileWriter 把日志写进当前日志文件并累计大小。
//
// 用 writer 而不是让 logger 直接持有 *os.File，是为了让 fileSize 真的能增长：
// 之前的实现只在没人调用的 Write() 里累加，导致「按大小轮转」永远不会触发。
type fileWriter struct{}

func (fileWriter) Write(p []byte) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	if logFile == nil {
		return 0, nil
	}
	n, err := logFile.Write(p)
	fileSize += int64(n)
	return n, err
}

// rotateLocked 轮转日志文件。调用方必须已持有 mu。
//
// 注意：checkLogFile 过去持锁再调 rotateLogFile（内部又 Lock 一次），
// sync.Mutex 不可重入 —— 一到跨天或超过 MaxLogFileSize，日志协程就会永久卡死，
// 之后任何一次写日志都会跟着阻塞。所以这里拆成「已持锁」版本。
func rotateLocked() error {
	// 关闭当前日志文件
	if logFile != nil {
		_ = logFile.Close()
	}

	today := time.Now().Format("2006-01-02")
	currentDate = today

	suffix := ""
	count := 1
	for {
		logFileName := fmt.Sprintf("%s-%s%s.log", filePrefix, today, suffix)
		logFilePath := filepath.Join(logDir, logFileName)

		if _, err := os.Stat(logFilePath); os.IsNotExist(err) {
			f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return fmt.Errorf("failed to open log file: %v", err)
			}
			logFile = f
			fileSize = 0
			return nil
		}

		fileInfo, err := os.Stat(logFilePath)
		if err != nil {
			return fmt.Errorf("failed to stat log file: %v", err)
		}
		if fileInfo.Size() < MaxLogFileSize {
			f, err := os.OpenFile(logFilePath, os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return fmt.Errorf("failed to open log file: %v", err)
			}
			logFile = f
			fileSize = fileInfo.Size()
			return nil
		}

		count++
		suffix = fmt.Sprintf("-%d", count)
	}
}

// checkLogFile 按天 / 按大小轮转。
func checkLogFile() {
	for {
		time.Sleep(1 * time.Minute)

		mu.Lock()
		today := time.Now().Format("2006-01-02")
		if today != currentDate || fileSize >= MaxLogFileSize {
			_ = rotateLocked()
		}
		mu.Unlock()
	}
}

// Write 把 p 写进日志文件（供外部把它当 io.Writer 用的场景）。
func Write(p []byte) (n int, err error) {
	return fileWriter{}.Write(p)
}

// Close 关闭日志文件
func Close() error {
	mu.Lock()
	defer mu.Unlock()

	if logFile != nil {
		return logFile.Close()
	}
	return nil
}

// fallbackLogger 未 Init 时的兜底输出（stderr）。
//
// 存在的理由：InfoLogger 等是 Init 里才赋值的包变量，未初始化时直接
// Printf 是对 nil 方法接收者的调用 —— 不是「日志没了」，而是整个进程 panic。
// 启动早期（配置还没读完）、单测里调用日志都属这一类，不该被打成崩溃。
var fallbackLogger = log.New(os.Stderr, "", log.LstdFlags)

// orFallback 返回可用的 logger：未初始化时退到 stderr。
func orFallback(l *log.Logger) *log.Logger {
	if l != nil {
		return l
	}
	return fallbackLogger
}

// Info 记录信息日志
func Info(format string, v ...interface{}) {
	orFallback(InfoLogger).Printf(format, v...)
}

// Error 记录错误日志
func Error(format string, v ...interface{}) {
	orFallback(ErrorLogger).Printf(format, v...)
}

// Debug 记录调试日志
func Debug(format string, v ...interface{}) {
	orFallback(DebugLogger).Printf(format, v...)
}
