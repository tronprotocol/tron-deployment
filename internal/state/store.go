package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const stateFileVersion = 1

// Store manages the deployment state file.
type Store struct {
	path string
}

// NewStore creates a state store at the given path.
// If path is empty, defaults to ~/.trond/state.json.
func NewStore(path string) (*Store, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		path = filepath.Join(home, ".trond", "state.json")
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	return &Store{path: path}, nil
}

// Path returns the state file path.
func (s *Store) Path() string {
	return s.path
}

// Load reads the state file. Returns empty state if file doesn't exist.
func (s *Store) Load() (*DeploymentState, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return &DeploymentState{
			Version: stateFileVersion,
			Nodes:   []ManagedNode{},
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state file: %w", err)
	}

	var state DeploymentState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}

	return &state, nil
}

// Save writes the state to disk atomically.
func (s *Store) Save(state *DeploymentState) error {
	state.Version = stateFileVersion

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	// Atomic write: write to a temp file then rename. The temp name is
	// unique per call — a fixed ".tmp" is shared by concurrent writers,
	// which then overwrite each other's half-written file and rename
	// whatever is left, so the rename stops being atomic in practice.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create state temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write state temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close state temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod state temp file: %w", err)
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename state file: %w", err)
	}

	return nil
}

// GetNode returns the managed node by name, or nil if not found.
func (s *Store) GetNode(state *DeploymentState, name string) *ManagedNode {
	for i := range state.Nodes {
		if state.Nodes[i].Name == name {
			return &state.Nodes[i]
		}
	}
	return nil
}

// UpsertNode adds or updates a node in the state.
func (s *Store) UpsertNode(state *DeploymentState, node ManagedNode) {
	for i, n := range state.Nodes {
		if n.Name == node.Name {
			state.Nodes[i] = node
			return
		}
	}
	state.Nodes = append(state.Nodes, node)
}

// RemoveNode removes a node from the state by name.
func (s *Store) RemoveNode(state *DeploymentState, name string) bool {
	for i, n := range state.Nodes {
		if n.Name == name {
			state.Nodes = append(state.Nodes[:i], state.Nodes[i+1:]...)
			return true
		}
	}
	return false
}

// HasChanged returns true if the intent or config hash differs from the stored node.
func (s *Store) HasChanged(existing *ManagedNode, intentHash, configHash string) bool {
	return existing.IntentHash != intentHash || existing.ConfigHash != configHash
}
