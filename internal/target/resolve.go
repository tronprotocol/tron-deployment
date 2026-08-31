package target

import (
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// FromIntent resolves the target declared by an intent. The caller owns Close.
func FromIntent(i *intent.Intent) (Target, error) {
	if i.Target.Type == "ssh" {
		t := NewSSHTarget(i.Target.Host, i.Target.Port, i.Target.User, i.Target.IdentityFile)
		if err := t.Connect(); err != nil {
			return nil, err
		}
		return t, nil
	}
	return NewLocalTarget(), nil
}

// FromManagedNode resolves the target recorded in managed-node state. The caller owns Close.
func FromManagedNode(n *state.ManagedNode) (Target, error) {
	if n.Target.Type == "ssh" {
		t := NewSSHTarget(n.Target.Host, n.Target.Port, n.Target.User, n.Target.IdentityFile)
		if err := t.Connect(); err != nil {
			return nil, err
		}
		return t, nil
	}
	return NewLocalTarget(), nil
}
