package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"time"

	"github.com/tronprotocol/tron-deployment/internal/paths"
)

// AuditEntry represents a single audit log line.
type AuditEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Command    string    `json:"command"`
	Node       string    `json:"node,omitempty"`
	Target     string    `json:"target"`
	IntentHash string    `json:"intent_hash,omitempty"`
	Result     string    `json:"result"`
	DurationMs int64     `json:"duration_ms"`
	ErrorCode  string    `json:"error_code,omitempty"`

	// Detail names WHAT the command acted on, for the verbs where the
	// verb alone does not say. "exec" and "recipe host-step" run
	// caller-supplied programs and "files put" writes caller-supplied
	// bytes: an entry recording only that one of them happened cannot
	// answer the question an audit log exists to answer.
	//
	// It carries an identifier — a program name, a destination path, a
	// recipe source — never a payload. Full argv and file contents are
	// exactly where a caller is most likely to have put a token or key,
	// and an audit log is the wrong place to learn that.
	Detail string `json:"detail,omitempty"`

	// RunID ties entries produced by one recipe run together. A recipe's
	// command steps re-exec trond, so each child writes its own entry
	// under its own verb — correct, but it leaves the log showing
	// "stop n0" with no indication that a recipe drove it, and no way to
	// tell two concurrent runs apart. The parent mints an id and the
	// children inherit it through the environment.
	RunID string `json:"run_id,omitempty"`
}

// AuditLog writes append-only JSONL entries to the audit log file.
type AuditLog struct {
	path string
	mu   sync.Mutex
}

// NewAuditLog creates an audit log writer. If path is empty, uses paths.AuditLog().
func NewAuditLog(path string) (*AuditLog, error) {
	if path == "" {
		path = paths.AuditLog()
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create audit dir: %w", err)
	}

	return &AuditLog{path: path}, nil
}

// Write appends an audit entry to the log.
func (a *AuditLog) Write(entry AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal audit entry: %w", err)
	}

	f, err := os.OpenFile(a.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer f.Close()

	_, err = f.Write(append(data, '\n'))
	return err
}
