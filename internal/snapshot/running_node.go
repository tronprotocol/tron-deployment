package snapshot

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// RunningNodeDestinationError indicates that a snapshot destination belongs
// to a managed node which may still have its chain database open.
type RunningNodeDestinationError struct{ NodeName string }

func (e *RunningNodeDestinationError) Error() string {
	return fmt.Sprintf("refusing to write snapshot into running node %q data directory", e.NodeName)
}

// RefuseRunningNodeDestination protects managed node databases from being
// overwritten while java-tron (jar) or Docker holds them open. Force never
// bypasses this guard.
func RefuseRunningNodeDestination(dest, explicitNode string) error {
	store, err := state.NewStore(paths.State())
	if err != nil {
		return err
	}
	st, err := store.Load()
	if err != nil {
		return err
	}
	var matched *state.ManagedNode
	if explicitNode != "" {
		matched = store.GetNode(st, explicitNode)
	} else {
		for i := range st.Nodes {
			if st.Nodes[i].Status != "running" && st.Nodes[i].Status != "error" {
				continue
			}
			if managedNodeDestinationMatches(dest, &st.Nodes[i]) {
				matched = &st.Nodes[i]
				break
			}
		}
	}
	if matched == nil || (matched.Status != "running" && matched.Status != "error") {
		return nil
	}
	return &RunningNodeDestinationError{NodeName: matched.Name}
}

func managedNodeDestinationMatches(dest string, node *state.ManagedNode) bool {
	if node == nil || dest == "" {
		return false
	}
	destAbs, err := filepath.Abs(filepath.Clean(dest))
	if err != nil {
		return false
	}
	roots := make([]string, 0, 2)
	if node.InstallPath != "" && node.Runtime != "docker" {
		if root, e := filepath.Abs(filepath.Clean(node.InstallPath)); e == nil {
			roots = append(roots, root)
		}
	}
	if node.StorageRoot != "" {
		if root, e := filepath.Abs(filepath.Clean(node.StorageRoot)); e == nil {
			roots = append(roots, root)
		}
	}
	if node.Runtime == "docker" && node.StorageRoot == "" {
		if root, ok := legacyDockerStorageRoot(node.Name); ok {
			roots = append(roots, root)
		}
	}
	for _, root := range roots {
		candidates := []string{root, filepath.Join(root, "output-directory"), filepath.Join(root, "output-directory", "database"), filepath.Join(root, "database")}
		if filepath.Base(root) == "output-directory" {
			candidates = append(candidates, filepath.Dir(root))
		}
		for _, candidate := range candidates {
			if samePath(destAbs, candidate) {
				return true
			}
		}
	}
	return false
}

func legacyDockerStorageRoot(name string) (string, bool) {
	compose := filepath.Join(paths.Deployments(), name, "docker-compose.yaml")
	f, err := os.Open(compose)
	if err != nil {
		return "", false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		marker := ":/java-tron/output-directory"
		if i := strings.Index(line, marker); i >= 0 {
			source := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[:i]), "-"))
			if strings.HasPrefix(source, "/") {
				return source, true
			}
			if strings.HasPrefix(source, "./") {
				return filepath.Join(paths.Deployments(), name, source), true
			}
			return "", false
		}
	}
	return "", false
}

func samePath(left, right string) bool { return canonicalPath(left) == canonicalPath(right) }

func canonicalPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	missing := []string{}
	for current := path; ; current = filepath.Dir(current) {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		missing = append(missing, filepath.Base(current))
	}
	return path
}
