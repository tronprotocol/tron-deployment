package schema

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Manifest is the serialised view of trond's CLI surface. Built by
// walking a cobra command tree once and attaching matching JSON
// Schemas where they exist. Stable enough for agents to memoise.
type Manifest struct {
	SchemaVersion string    `json:"schema_version"`
	ToolName      string    `json:"tool"`
	Description   string    `json:"description,omitempty"`
	Commands      []Command `json:"commands"`
}

// Command describes one trond subcommand, including its flags, the
// commands nested below it, and (when registered) the JSON Schema for
// its --output json result.
type Command struct {
	Name            string         `json:"name"`
	FullName        string         `json:"full_name"`
	Short           string         `json:"short,omitempty"`
	Long            string         `json:"long,omitempty"`
	Use             string         `json:"use"`
	Aliases         []string       `json:"aliases,omitempty"`
	Examples        string         `json:"examples,omitempty"`
	Flags           []Flag         `json:"flags,omitempty"`
	Subcommands     []Command      `json:"subcommands,omitempty"`
	OutputSchema    map[string]any `json:"output_schema,omitempty"`
	OutputSchemaURL string         `json:"output_schema_url,omitempty"`
}

// Flag captures the agent-relevant attributes of one cobra flag.
// Hidden / deprecated flags are excluded from the manifest.
type Flag struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Default   string `json:"default,omitempty"`
	Usage     string `json:"usage"`
	Required  bool   `json:"required,omitempty"`
}

// Build walks the cobra root and builds a Manifest. schemaLookup maps
// a command's "full name" (space-separated path) to its schema short
// name (the basename in schemas/output/). Pass nil to attach no schemas.
func Build(root *cobra.Command, schemaLookup map[string]string) Manifest {
	if schemaLookup == nil {
		schemaLookup = DefaultSchemaLookup
	}
	return Manifest{
		SchemaVersion: SchemaVersion,
		ToolName:      root.Name(),
		Description:   firstLine(root.Long),
		Commands:      childCommands(root, schemaLookup),
	}
}

// DefaultSchemaLookup is the canonical mapping from cobra full-name
// path → schema short name. Update this whenever a new command gets a
// JSON Schema.
var DefaultSchemaLookup = map[string]string{
	"trond apply":              "apply",
	"trond build":              "build",
	"trond build list":         "build-list",
	"trond build inspect":      "build-inspect",
	"trond build prune":        "build-prune",
	"trond plan":               "plan",
	"trond status":             "status",
	"trond list":               "list",
	"trond inspect":            "inspect",
	"trond diagnose":           "diagnose",
	"trond health":             "health",
	"trond verify":             "verify",
	"trond preflight":          "preflight",
	"trond doctor":             "doctor",
	"trond version":            "version",
	"trond events":             "events",
	"trond config validate":    "config-validate",
	"trond config render":      "config-render",
	"trond network create":     "network-create",
	"trond network status":     "network-status",
	"trond snapshot sources":   "snapshot-sources",
	"trond snapshot list":      "snapshot-list",
	"trond snapshot download":  "snapshot-download",
	"trond snapshot clone":     "snapshot-clone",
	"trond snapshot jobs":      "snapshot-jobs",
	"trond recipe list":        "recipe-list",
	"trond recipe show":        "recipe-show",
	"trond recipe run":         "recipe-run",
	"trond shadow-fork keygen": "shadow-fork-keygen",
	"trond shadow-fork mutate": "shadow-fork-mutate",
}

// childCommands recursively gathers cobra children, skipping the
// cobra-builtin "help" and "completion" entries which agents can't
// usefully consume.
func childCommands(parent *cobra.Command, lookup map[string]string) []Command {
	var out []Command
	for _, c := range parent.Commands() {
		if c.Hidden || c.IsAdditionalHelpTopicCommand() {
			continue
		}
		if c.Name() == "help" {
			continue
		}
		out = append(out, buildCommand(c, lookup))
	}
	return out
}

func buildCommand(c *cobra.Command, lookup map[string]string) Command {
	full := c.CommandPath()
	cmd := Command{
		Name:        c.Name(),
		FullName:    full,
		Short:       c.Short,
		Long:        firstParagraph(c.Long),
		Use:         c.Use,
		Aliases:     c.Aliases,
		Examples:    strings.TrimSpace(c.Example),
		Flags:       collectFlags(c),
		Subcommands: childCommands(c, lookup),
	}
	if name, ok := lookup[full]; ok {
		if doc, ok := Get(name); ok {
			cmd.OutputSchema = doc
			cmd.OutputSchemaURL = URLFor(name)
		}
	}
	return cmd
}

// collectFlags reads both local and inherited persistent flags so the
// manifest reflects what an agent would actually pass on the command
// line for that subcommand.
func collectFlags(c *cobra.Command) []Flag {
	var out []Flag
	seen := map[string]bool{}
	add := func(name, short, typ, def, usage string, required bool) {
		if seen[name] {
			return
		}
		seen[name] = true
		out = append(out, Flag{
			Name:      name,
			Shorthand: short,
			Type:      typ,
			Default:   def,
			Usage:     usage,
			Required:  required,
		})
	}
	// Local flags are most relevant; capture them first so persistent
	// shadows from a parent (rare) can't displace command-specific docs.
	c.LocalFlags().VisitAll(func(f *pflag.Flag) {
		add(f.Name, f.Shorthand, f.Value.Type(), f.DefValue, f.Usage, isFlagRequired(c, f.Name))
	})
	c.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		add(f.Name, f.Shorthand, f.Value.Type(), f.DefValue, f.Usage, isFlagRequired(c, f.Name))
	})
	return out
}

// isFlagRequired probes cobra's hidden annotation that MarkFlagRequired
// sets. We can't import cobra's internal cobra.BashCompOneRequiredFlag
// constant directly because it lives in the cobra package, but the
// annotation key is documented and stable.
func isFlagRequired(c *cobra.Command, name string) bool {
	flag := c.Flags().Lookup(name)
	if flag == nil {
		return false
	}
	if vals, ok := flag.Annotations["cobra_annotation_bash_completion_one_required_flag"]; ok {
		return slices.Contains(vals, "true")
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func firstParagraph(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
