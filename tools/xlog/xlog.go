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
	// MaxLogFileSize is the maximum size of a single log file (30MB).
	MaxLogFileSize = 30 * 1024 * 1024
)

var (
	// InfoLogger writes informational logs.
	InfoLogger *log.Logger
	// ErrorLogger writes error logs.
	ErrorLogger *log.Logger
	// DebugLogger writes debug logs.
	DebugLogger *log.Logger

	logFile     *os.File
	logDir      string
	filePrefix  string
	currentDate string
	fileSize    int64

	// mirrorConsole controls whether logs also go to stdout (the console).
	//
	// The test is "is stdout attached to a terminal": double-clicked or run in a window → yes, logs
	// show up; redirected to a file or taken over by a service manager → no, file only, so nothing is
	// duplicated. Use XLOG_CONSOLE=1 / 0 to force it on or off.
	mirrorConsole bool

	mu sync.Mutex
)

// Init initialises the logging system. prefix decides the log file name prefix ("nexus", "sandboxd"),
// passed in by the caller after the actual service name, producing <prefix>-<date>[-<n>].log.
//
// Logs are written to both the file and the console by default (the console part only when stdout is a
// terminal). Why mirror at all: the useful facts from startup (listening address, isolation mode in
// effect, whether the browser is available) used to go to a file only, so whoever ran the service in
// the foreground had to go digging — the opposite of "configuration must be visible at a glance".
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

	// Start the log rotation goroutine.
	go checkLogFile()

	return nil
}

// wantConsole decides whether logs should also go to the console.
//
// The environment variable wins (XLOG_CONSOLE=1/0); otherwise it checks whether stdout is a character
// device: a Windows console or a Unix terminal is one, a file or a pipe is not.
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

// fileWriter writes logs into the current log file and accumulates its size.
//
// It is a writer rather than the logger holding *os.File directly so that fileSize actually grows: the
// previous implementation accumulated only inside a Write() nobody called, which meant "rotate by size"
// never triggered.
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

// rotateLocked rotates the log file. The caller must already hold mu.
//
// Note: checkLogFile used to hold the lock and then call rotateLogFile (which locked again), and
// sync.Mutex is not reentrant — as soon as the day changed or MaxLogFileSize was exceeded, the logging
// goroutine deadlocked forever and every later log write blocked with it. Hence the "already holding
// the lock" variant.
func rotateLocked() error {
	// Close the current log file.
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

// checkLogFile rotates on day change and on size.
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

// Write writes p into the log file (for callers that need to treat this package as an io.Writer).
func Write(p []byte) (n int, err error) {
	return fileWriter{}.Write(p)
}

// Close closes the log file.
func Close() error {
	mu.Lock()
	defer mu.Unlock()

	if logFile != nil {
		return logFile.Close()
	}
	return nil
}

// fallbackLogger is the output used before Init (stderr).
//
// Why it exists: InfoLogger and friends are package variables assigned inside Init; calling Printf on
// them before initialisation is a call on a nil method receiver — not "the log line is lost" but a
// panic of the whole process. Early startup (configuration not read yet) and unit tests both fall into
// that category and must not become crashes.
var fallbackLogger = log.New(os.Stderr, "", log.LstdFlags)

// orFallback returns a usable logger, falling back to stderr before initialisation.
func orFallback(l *log.Logger) *log.Logger {
	if l != nil {
		return l
	}
	return fallbackLogger
}

// Info logs an informational message.
func Info(format string, v ...interface{}) {
	orFallback(InfoLogger).Printf(format, v...)
}

// Error logs an error message.
func Error(format string, v ...interface{}) {
	orFallback(ErrorLogger).Printf(format, v...)
}

// Debug logs a debug message.
func Debug(format string, v ...interface{}) {
	orFallback(DebugLogger).Printf(format, v...)
}
