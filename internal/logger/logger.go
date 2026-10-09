// Package logger provides a centralized logging facility with support for
// broadcasting logs to listeners (e.g., for the web UI) via Go channels.
package logger

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// LogEntry represents a single log line
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

// bufferSize is the number of logs to keep in memory
const bufferSize = 1000

var (
	instance *Logger
	once     sync.Once
)

// Logger is a custom logger with memory buffer and broadcasting
type Logger struct {
	mu          sync.RWMutex
	buffer      []LogEntry
	subscribers map[chan LogEntry]bool
	out         io.Writer
}

// Get returns the singleton logger instance
func Get() *Logger {
	once.Do(func() {
		instance = &Logger{
			buffer:      make([]LogEntry, 0, bufferSize),
			subscribers: make(map[chan LogEntry]bool),
			out:         os.Stdout,
		}
	})
	return instance
}

// Printf logs a formatted string
func Printf(format string, v ...interface{}) {
	Get().Log(fmt.Sprintf(format, v...))
}

// Println logs a line
func Println(v ...interface{}) {
	Get().Log(fmt.Sprint(v...))
}

// Log adds a message to the buffer and broadcasts it
func (l *Logger) Log(msg string) {
	entry := LogEntry{
		Timestamp: time.Now(),
		Message:   msg,
	}

	// Write to Stdout
	fmt.Fprintln(l.out, entry.Timestamp.Format("2006/01/02 15:04:05"), msg)

	l.mu.Lock()
	defer l.mu.Unlock()

	// Append to buffer
	if len(l.buffer) >= bufferSize {
		// Shift
		l.buffer = l.buffer[1:]
	}
	l.buffer = append(l.buffer, entry)

	// Broadcast
	for ch := range l.subscribers {
		select {
		case ch <- entry:
		default:
			// Drop if subscriber is slow
		}
	}
}

// Subscribe returns a channel to receive live logs
func (l *Logger) Subscribe() chan LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch := make(chan LogEntry, 100)
	l.subscribers[ch] = true
	return ch
}

// Unsubscribe removes a subscriber
func (l *Logger) Unsubscribe(ch chan LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.subscribers, ch)
	close(ch)
}

// GetHistory returns the current buffer
func (l *Logger) GetHistory() []LogEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	// Return copy
	history := make([]LogEntry, len(l.buffer))
	copy(history, l.buffer)
	return history
}

var (
	everyMu sync.Mutex
	lastLog = map[string]time.Time{}
)

// Every logs at most once per interval for a key (e.g. a route or a workload), so an error hit
// by every request doesn't flood the log and push everything else out of the dashboard's view.
func Every(key string, interval time.Duration, format string, v ...interface{}) {
	now := time.Now()
	everyMu.Lock()
	if last, ok := lastLog[key]; ok && now.Sub(last) < interval {
		everyMu.Unlock()
		return
	}
	if len(lastLog) > 10000 {
		for k, t := range lastLog {
			if now.Sub(t) > time.Hour {
				delete(lastLog, k)
			}
		}
	}
	lastLog[key] = now
	everyMu.Unlock()
	Printf(format, v...)
}
