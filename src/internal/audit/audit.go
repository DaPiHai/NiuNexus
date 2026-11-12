// Package audit records security-relevant configuration changes. Entries are
// JSON Lines and never contain proxy passwords or RPC tokens.
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxReadBytes = 10 << 20
	maxLineBytes = 64 << 10
)

// Entry describes one change attempt.
type Entry struct {
	Time       time.Time `json:"time"`
	User       string    `json:"user"`
	UID        string    `json:"uid,omitempty"`
	Action     string    `json:"action"`
	Target     string    `json:"target,omitempty"`
	Result     string    `json:"result"`
	Message    string    `json:"message,omitempty"`
	ChangeID   string    `json:"changeId,omitempty"`
	DurationMs int64     `json:"durationMs,omitempty"`
}

// Logger appends entries to a single file with a process-wide lock.
type Logger struct {
	path string
	mu   sync.Mutex
}

// New opens (or creates) the audit log with root-only permissions.
func New(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_ = file.Close()
	_ = os.Chmod(path, 0o600)
	return &Logger{path: path}, nil
}

// Log appends an entry. A failure to write must not break the user's request;
// callers receive the error for logging but should continue.
func (l *Logger) Log(entry Entry) error {
	if l == nil {
		return nil
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	if entry.Result == "" {
		entry.Result = "success"
	}
	entry.User = strings.TrimSpace(entry.User)
	if entry.User == "" {
		entry.User = "unknown"
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if err := rotateIfNeeded(l.path); err != nil {
		return err
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(payload); err != nil {
		return err
	}
	return file.Sync()
}

// Read returns up to limit entries, newest first.
func (l *Logger) Read(limit int) ([]Entry, error) {
	if l == nil {
		return []Entry{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	file, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, nil
		}
		return nil, err
	}
	defer file.Close()

	entries := make([]Entry, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxLineBytes)
	var consumed int64
	for scanner.Scan() {
		line := scanner.Bytes()
		consumed += int64(len(line)) + 1
		if consumed > maxReadBytes {
			// Avoid unbounded memory on a very large log. The file is rotated
			// at 5 MiB, so this is a defensive ceiling only.
			break
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// Newest first.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func rotateIfNeeded(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Size() < 5<<20 {
		return nil
	}
	return os.Rename(path, path+".1")
}

// Sanitize removes obvious secret assignments from free-form messages before
// they reach the log. Callers should still avoid passing secrets at all.
func Sanitize(message string) string {
	message = strings.TrimSpace(message)
	lower := strings.ToLower(message)
	for _, key := range []string{"password", "passwd", "secret", "token"} {
		if index := strings.Index(lower, key+"="); index >= 0 {
			end := index + len(key) + 1
			if space := strings.IndexAny(message[end:], " \t,;"); space >= 0 {
				message = message[:end] + "***" + message[end+space:]
			} else {
				message = message[:end] + "***"
			}
			lower = strings.ToLower(message)
		}
	}
	return message
}

// String returns a compact representation used by diagnostics.
func (e Entry) String() string {
	return fmt.Sprintf("%s %s %s/%s: %s", e.Time.Format(time.RFC3339), e.User, e.Action, e.Result, e.Message)
}
