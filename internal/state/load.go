package state

// LoadNode opens path, loads deployment state, and returns the named node.
// Missing nodes are returned as a nil node without an error.
func LoadNode(path, name string) (*Store, *DeploymentState, *ManagedNode, error) {
	store, st, err := Load(path)
	if err != nil {
		return nil, nil, nil, err
	}
	node := store.GetNode(st, name)
	return store, st, node, nil
}

// Load opens a Store and loads its deployment state without acquiring a lock.
// Callers that mutate state must retain their existing write-lock discipline.
func Load(path string) (*Store, *DeploymentState, error) {
	store, err := NewStore(path)
	if err != nil {
		return nil, nil, err
	}
	st, err := store.Load()
	if err != nil {
		return store, nil, err
	}
	return store, st, nil
}
