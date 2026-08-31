package paths

import "path/filepath"

// DeploymentConfig returns the rendered config path for a managed deployment.
func DeploymentConfig(name string) string { return filepath.Join(Deployments(), name, name+".conf") }
