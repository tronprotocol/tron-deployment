package render

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// embeddedTemplates contains the three base java-tron config files bundled
// into the binary. They are the source of truth when no external template
// directory is configured — release builds always work out of the box.
//
//go:embed templates/*.conf
var embeddedTemplates embed.FS

//go:embed dashboards/*.json
var embeddedDashboards embed.FS

// localTemplatesDir is the working-directory override FindTemplatesDir looks
// for when TROND_TEMPLATES_DIR is unset.
const localTemplatesDir = "templates"

// LoadTemplate returns the raw HOCON template for the given network. When
// templateDir is non-empty and contains the matching file, the on-disk copy
// wins (useful for local development and tests). Otherwise we fall through
// to the embedded filesystem so a release binary always works without any
// co-located files.
func LoadTemplate(templateDir, network string) ([]byte, error) {
	fileName, ok := NetworkTemplate[network]
	if !ok {
		return nil, fmt.Errorf("unknown network %q", network)
	}

	if templateDir != "" {
		data, err := os.ReadFile(filepath.Join(templateDir, fileName))
		if err == nil {
			return data, nil
		}
		// Fall through — the embedded copy is authoritative when on-disk
		// lookup fails. Tests and CI shouldn't need to chdir to find templates.
	}

	data, err := embeddedTemplates.ReadFile("templates/" + fileName)
	if err != nil {
		return nil, fmt.Errorf("load embedded template %s: %w", fileName, err)
	}
	return data, nil
}

// LoadDashboard returns an embedded Grafana dashboard JSON by filename.
func LoadDashboard(name string) ([]byte, error) {
	return embeddedDashboards.ReadFile("dashboards/" + name)
}

// FindTemplatesDir resolves the on-disk templates directory that overrides
// the embedded copies, or "" to use the embedded ones. This is the single
// definition of "what counts as a templates directory" — every command
// resolves through it, so the rule can only ever change in one place.
//
// TROND_TEMPLATES_DIR wins outright. Otherwise a local ./templates directory
// counts only when it actually carries one of the known templates; any single
// one is enough, since keying off one particular network would ignore a
// directory that only carries the others. LoadTemplate still falls back to
// the embedded copy per network, so a partial directory is fine.
func FindTemplatesDir() string {
	if d := os.Getenv("TROND_TEMPLATES_DIR"); d != "" {
		return d
	}
	info, err := os.Stat(localTemplatesDir)
	if err != nil || !info.IsDir() {
		return ""
	}
	for _, name := range NetworkTemplate {
		if _, err := os.Stat(filepath.Join(localTemplatesDir, name)); err == nil {
			return localTemplatesDir
		}
	}
	return ""
}
