package intent

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

var validate = func() *validator.Validate {
	v := validator.New()
	// "safe_string" rejects any string that could break out of its
	// downstream syntax: HOCON line, YAML scalar, systemd directive.
	// We refuse anything containing \n, \r, or other ASCII control
	// chars (except \t which is harmless in our renders). This is a
	// blunt instrument but every field that uses this tag is rendered
	// either as a YAML scalar, a shell argument, or a systemd value —
	// none of those have a legitimate use for embedded newlines.
	_ = v.RegisterValidation("safe_string", func(fl validator.FieldLevel) bool {
		return !containsControlChars(fl.Field().String())
	})
	// "https_url" only accepts https:// URLs (not http, file, ftp, …).
	// Used for jar download URLs.
	_ = v.RegisterValidation("https_url", func(fl validator.FieldLevel) bool {
		s := fl.Field().String()
		if s == "" {
			return true
		}
		u, err := url.Parse(s)
		if err != nil {
			return false
		}
		return u.Scheme == "https" && u.Host != ""
	})
	// "sha256_hex" matches a 64-char lowercase hex string.
	_ = v.RegisterValidation("sha256_hex", func(fl validator.FieldLevel) bool {
		s := fl.Field().String()
		if s == "" {
			return true
		}
		return sha256HexPattern.MatchString(s)
	})
	return v
}()

// containsControlChars returns true for strings containing newline,
// carriage return, or any C0/C1 control char (except tab). Used by the
// "safe_string" validator and applied to every string field that flows
// into compose YAML / systemd unit / HOCON. This is the SECURITY backbone
// — without it, an attacker controlling intent.yaml can inject lines into
// any of those formats.
func containsControlChars(s string) bool {
	for _, r := range s {
		if r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// checkSafeStrings walks every map / slice / struct on the node that
// renders into compose YAML, systemd unit, or HOCON, rejecting any value
// that contains newlines or control chars. This is the second half of
// the safe_string contract — struct-tag validation can't reach map values
// and slice elements, so we do it manually.
func checkSafeStrings(idx int, n *NodeSpec) error {
	chk := func(field, val string) error {
		if containsControlChars(val) {
			return fmt.Errorf("nodes[%d].%s: contains newline or control char (not allowed; would inject into compose YAML / systemd unit / HOCON)", idx, field)
		}
		return nil
	}
	for k, v := range n.ExtraEnv {
		if err := chk("extra_env."+k, k); err != nil {
			return err
		}
		if err := chk("extra_env."+k, v); err != nil {
			return err
		}
	}
	for i, a := range n.ExtraArgs {
		if err := chk(fmt.Sprintf("extra_args[%d]", i), a); err != nil {
			return err
		}
	}
	for k, v := range n.Labels {
		if err := chk("labels."+k, k); err != nil {
			return err
		}
		if err := chk("labels."+k, v); err != nil {
			return err
		}
	}
	for k, v := range n.ExtraHosts {
		if err := chk("extra_hosts."+k, k); err != nil {
			return err
		}
		if err := chk("extra_hosts."+k, v); err != nil {
			return err
		}
	}
	for i, net := range n.Networks {
		if err := chk(fmt.Sprintf("networks[%d]", i), net); err != nil {
			return err
		}
	}
	for i, dep := range n.DependsOn {
		if err := chk(fmt.Sprintf("depends_on[%d]", i), dep); err != nil {
			return err
		}
	}
	for i, e := range n.Entrypoint {
		if err := chk(fmt.Sprintf("entrypoint[%d]", i), e); err != nil {
			return err
		}
	}
	if n.JVM != nil {
		for i, o := range n.JVM.ExtraOpts {
			if err := chk(fmt.Sprintf("jvm.extra_opts[%d]", i), o); err != nil {
				return err
			}
			if err := validateJVMExtraOpt(idx, i, o); err != nil {
				return err
			}
		}
	}
	if err := chk("storage.data", n.Storage.Data); err != nil {
		return err
	}
	if err := chk("storage.logs", n.Storage.Logs); err != nil {
		return err
	}
	if err := chk("storage.path", n.Storage.StoragePath); err != nil {
		return err
	}
	if err := chk("shm_size", n.ShmSize); err != nil {
		return err
	}
	if n.JVM != nil {
		if err := chk("jvm.heap_max", n.JVM.HeapMax); err != nil {
			return err
		}
		if err := chk("jvm.heap_new", n.JVM.HeapNew); err != nil {
			return err
		}
		if err := chk("jvm.direct_memory", n.JVM.DirectMemory); err != nil {
			return err
		}
	}
	if err := chk("resources.memory", n.Resources.Memory); err != nil {
		return err
	}
	if err := chk("resources.cpu", n.Resources.CPU); err != nil {
		return err
	}
	if hc := n.Healthcheck; hc != nil {
		for i, t := range hc.Test {
			if err := chk(fmt.Sprintf("healthcheck.test[%d]", i), t); err != nil {
				return err
			}
		}
		if err := chk("healthcheck.interval", hc.Interval); err != nil {
			return err
		}
		if err := chk("healthcheck.timeout", hc.Timeout); err != nil {
			return err
		}
		if err := chk("healthcheck.start_period", hc.StartPeriod); err != nil {
			return err
		}
	}
	if log := n.Logging; log != nil {
		if err := chk("logging.driver", log.Driver); err != nil {
			return err
		}
		for k, v := range log.Options {
			if err := chk("logging.options."+k, k); err != nil {
				return err
			}
			if err := chk("logging.options."+k, v); err != nil {
				return err
			}
		}
	}
	if wk := n.WitnessKey; wk != nil {
		if err := chk("witness_key.account_address", wk.AccountAddress); err != nil {
			return err
		}
		if err := chk("witness_key.keystore_path", wk.KeystorePath); err != nil {
			return err
		}
	}
	// Network overrides flow straight into HOCON; reject anything fishy.
	if no := &n.NetworkOverrides; no != nil {
		if no.Seeds != nil {
			for i, s := range *no.Seeds {
				if err := chk(fmt.Sprintf("network_overrides.seeds[%d]", i), s); err != nil {
					return err
				}
			}
		}
		if no.ActivePeers != nil {
			for i, s := range *no.ActivePeers {
				if err := chk(fmt.Sprintf("network_overrides.active_peers[%d]", i), s); err != nil {
					return err
				}
			}
		}
		if no.PassivePeers != nil {
			for i, s := range *no.PassivePeers {
				if err := chk(fmt.Sprintf("network_overrides.passive_peers[%d]", i), s); err != nil {
					return err
				}
			}
		}
	}
	// config_overrides values are interpolated into HOCON via fmt.%v; the
	// keys are dotted-key strings that flow verbatim to the left of `=`.
	for k, v := range n.ConfigOverrides {
		if err := chk("config_overrides."+k, k); err != nil {
			return err
		}
		if vs, ok := v.(string); ok {
			if err := chk("config_overrides."+k, vs); err != nil {
				return err
			}
		}
	}
	return nil
}

// namePattern enforces DNS-label-safe names: ^[a-z0-9][a-z0-9-]{0,62}$
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// hexPattern detects raw hex keys accidentally placed in witness_key_env.
var hexPattern = regexp.MustCompile(`^[0-9a-fA-F]{32,}$`)

// sha256HexPattern matches a 64-char lowercase hex digest.
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Load reads an intent YAML file, validates it, applies defaults, and returns the parsed Intent.
func Load(path string) (*Intent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read intent file: %w", err)
	}

	return Parse(data)
}

// Parse parses intent YAML bytes, validates, and applies defaults.
func Parse(data []byte) (*Intent, error) {
	var intent Intent
	if err := yaml.Unmarshal(data, &intent); err != nil {
		return nil, fmt.Errorf("parse intent YAML: %w", err)
	}

	if err := Validate(&intent); err != nil {
		return nil, err
	}

	ApplyDefaults(&intent)
	return &intent, nil
}

// ParseRaw returns the parsed Intent with defaults NOT applied. Used by
// `config validate --explain` to distinguish explicit user values from
// defaults at the field level.
func ParseRaw(data []byte) (*Intent, error) {
	var intent Intent
	if err := yaml.Unmarshal(data, &intent); err != nil {
		return nil, fmt.Errorf("parse intent YAML: %w", err)
	}
	return &intent, nil
}

// LoadRaw is the file-reading counterpart to ParseRaw.
func LoadRaw(path string) (*Intent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read intent file: %w", err)
	}
	return ParseRaw(data)
}

// Validate checks the intent against schema rules and security constraints.
func Validate(intent *Intent) error {
	// Name format
	if !namePattern.MatchString(intent.Name) {
		return fmt.Errorf("invalid name %q: must match ^[a-z0-9][a-z0-9-]{0,62}$", intent.Name)
	}

	// Struct validation via tags
	if err := validate.Struct(intent); err != nil {
		return fmt.Errorf("intent validation: %w", err)
	}

	// SECURITY: every free-form string field that flows into compose YAML,
	// systemd unit, or HOCON gets the same rejection of control chars +
	// newlines. Without this, an attacker controlling intent.yaml can
	// inject directives into any of those formats. struct-tag validation
	// can't recurse into maps and slices, so this pass walks them by hand.
	for i, n := range intent.Nodes {
		if err := checkSafeStrings(i, &n); err != nil {
			return err
		}
	}

	// Build vs Image vs Jar mutual exclusion (spec/002 FR-005).
	// `build:` produces the artifact; `image:` references a pre-built
	// docker image; `jar:` fetches a pre-built JAR. A node may carry
	// at most one source.
	for i, n := range intent.Nodes {
		sources := 0
		if n.Build != nil {
			sources++
		}
		if n.Image != "" {
			sources++
		}
		if n.Jar != nil {
			sources++
		}
		if sources > 1 {
			return fmt.Errorf("nodes[%d]: build, image, jar are mutually exclusive — pick one source", i)
		}
		if n.Build != nil && n.Build.Artifact == "image" && n.Build.ImageTag == "" {
			return fmt.Errorf("nodes[%d]: build.image_tag is required when build.artifact = image", i)
		}
		// runtime + build.artifact compatibility. Catch this at
		// validate time so `trond config validate` fails fast, not
		// later in apply. Phase 2 wires only jar+jar end-to-end;
		// Phase 3 will land jar+image's compose-side hookup and lift
		// the docker+image restriction below.
		//
		// Validate runs BEFORE ApplyDefaults so intent.Target.Runtime
		// may still be "". DefaultRuntime is the shared rule
		// ApplyDefaults itself uses, so this check sees the same
		// effective runtime an apply call would.
		if n.Build != nil {
			rt := intent.Target.Runtime
			if rt == "" {
				rt = DefaultRuntime(intent)
			}
			artifact := n.Build.Artifact
			if artifact == "" {
				artifact = "jar"
			}
			switch {
			case rt == "docker" && artifact == "jar":
				return fmt.Errorf("nodes[%d]: target.runtime=docker cannot consume build.artifact=jar — set build.artifact=image (the docker runtime path) or switch target.runtime=jar", i)
			case rt == "jar" && artifact == "image":
				return fmt.Errorf("nodes[%d]: target.runtime=jar cannot consume build.artifact=image — set artifact to jar or switch runtime", i)
			}
			// Cross-arch image builds are a footgun. trond's image
			// path mounts /var/run/docker.sock into the builder
			// container so gradle's docker plugin can talk to the
			// HOST docker daemon directly. That daemon runs the
			// host's CPU arch regardless of the builder container's
			// --platform — so an arm64 builder on an amd64 host
			// still produces an amd64 image. The cache key would
			// then claim arm64 but the artifact would be amd64,
			// silently shipping the wrong arch to a deploy target.
			//
			// jar-wrap (Phase 5d) is SAFE for cross-arch: it runs
			// `docker build` directly from the host (no docker.sock
			// bind-mount), so --platform works correctly. The
			// rejection only applies to the gradle strategy.
			imgStrategy := n.Build.ImageStrategy
			if imgStrategy == "" {
				imgStrategy = "gradle"
			}
			if artifact == "image" && imgStrategy == "gradle" && n.Build.Platform != "" {
				hostPlatform := DefaultPlatform()
				if n.Build.Platform != hostPlatform {
					return fmt.Errorf("nodes[%d]: build.artifact=image with platform=%q is unsafe on host=%q using image_strategy=gradle — docker.sock-mounted builds always use the host daemon's arch, so the produced image would NOT be %q. Either: switch to image_strategy=jar-wrap (cross-arch safe), set platform=%q, or use build.artifact=jar", i, n.Build.Platform, hostPlatform, n.Build.Platform, hostPlatform)
				}
			}
		}
	}

	// Witness nodes need a key source. Accept either the legacy top-level
	// witness_key_env shortcut or the structured witness_key block. Both
	// values are validated to be ENV var names (not raw hex keys) so a
	// private key never leaks into the intent file by accident.
	for i, node := range intent.Nodes {
		if node.Type != "witness" {
			continue
		}
		legacy := node.WitnessKeyEnv
		var structured *WitnessKey
		if node.WitnessKey != nil {
			structured = node.WitnessKey
		}

		hasLegacy := legacy != ""
		hasStructured := structured != nil &&
			(structured.PrivateKeyEnv != "" || structured.KeystorePath != "")

		if !hasLegacy && !hasStructured {
			return fmt.Errorf("nodes[%d]: witness node requires witness_key_env or witness_key.{private_key_env,keystore_path}", i)
		}
		if hasLegacy {
			if err := validateWitnessKeyEnv(legacy); err != nil {
				return fmt.Errorf("nodes[%d]: %w", i, err)
			}
		}
		if hasStructured && structured.PrivateKeyEnv != "" {
			if err := validateWitnessKeyEnv(structured.PrivateKeyEnv); err != nil {
				return fmt.Errorf("nodes[%d]: witness_key.private_key_env: %w", i, err)
			}
		}
		if hasStructured && structured.KeystorePasswordEnv != "" {
			if err := validateWitnessKeyEnv(structured.KeystorePasswordEnv); err != nil {
				return fmt.Errorf("nodes[%d]: witness_key.keystore_password_env: %w", i, err)
			}
		}
	}

	// Monitoring validation
	if err := validateMonitoring(intent.Monitoring); err != nil {
		return err
	}

	return nil
}

// validateMonitoring checks monitoring field constraints.
func validateMonitoring(m *Monitoring) error {
	if m == nil {
		return nil
	}
	if m.Prometheus.Port != 0 && (m.Prometheus.Port < 1024 || m.Prometheus.Port > 65535) {
		return fmt.Errorf("monitoring.prometheus.port must be between 1024 and 65535, got %d", m.Prometheus.Port)
	}
	if m.Grafana.Port != 0 && (m.Grafana.Port < 1024 || m.Grafana.Port > 65535) {
		return fmt.Errorf("monitoring.grafana.port must be between 1024 and 65535, got %d", m.Grafana.Port)
	}
	if m.Prometheus.Retention != "" {
		matched, _ := regexp.MatchString(`^\d+[dwh]$`, m.Prometheus.Retention)
		if !matched {
			return fmt.Errorf("monitoring.prometheus.retention must match ^\\d+[dwh]$ (e.g. \"7d\", \"2w\"), got %q", m.Prometheus.Retention)
		}
	}
	if m.Grafana.AdminPasswordEnv != "" {
		// Same contract as witness_key.private_key_env: the intent holds
		// the NAME of an env var, never the secret. The name is also
		// interpolated into the rendered compose file, so restricting it
		// to env-name characters keeps it from breaking out of ${...}.
		envVarPattern := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
		if !envVarPattern.MatchString(m.Grafana.AdminPasswordEnv) {
			return fmt.Errorf("monitoring.grafana.admin_password_env %q is not a valid environment variable name; it must be the NAME of an env var holding the Grafana admin password (e.g., GRAFANA_ADMIN_PASSWORD), not the password itself", m.Grafana.AdminPasswordEnv)
		}
	}
	if m.Grafana.Expose && m.Grafana.AdminPasswordEnv == "" {
		// Publishing Grafana beyond loopback with the image's default
		// admin/admin login hands the dashboards and the datasource proxy
		// to anyone who can reach the port.
		return fmt.Errorf("monitoring.grafana.expose publishes Grafana on all host interfaces and therefore requires monitoring.grafana.admin_password_env: set it to the NAME of an environment variable holding the admin password (e.g., admin_password_env: GRAFANA_ADMIN_PASSWORD), or drop expose to keep Grafana bound to 127.0.0.1")
	}
	return nil
}

// validateWitnessKeyEnv ensures the value is an env var name, not a raw private key.
func validateWitnessKeyEnv(value string) error {
	// Reject if it looks like a hex key
	if hexPattern.MatchString(value) {
		return fmt.Errorf("witness_key_env %q looks like a raw private key; it must be an environment variable NAME (e.g., SR_PRIVATE_KEY)", value)
	}

	// Reject if it starts with 0x (hex prefix)
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		return fmt.Errorf("witness_key_env %q looks like a raw private key; it must be an environment variable NAME", value)
	}

	// Reject if it contains characters not valid in env var names
	envVarPattern := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	if !envVarPattern.MatchString(value) {
		return fmt.Errorf("witness_key_env %q is not a valid environment variable name", value)
	}

	return nil
}

// validateJVMExtraOpt enforces the shape of one jvm.extra_opts entry.
//
// These flags land in two places a stray character can break: the compose
// `command:` list, as one Go-quoted YAML scalar, and the systemd ExecStart
// line. On top of that render.JVMArgs joins the whole set with spaces, so an
// entry containing whitespace does not stay one argument — it silently
// becomes two, and `-Dfoo=bar -XX:+Evil` smuggled through a single list item
// would look like one innocuous property in the intent.
//
// Only -D<key>=<value> and -XX:… are accepted. That is the tuning surface;
// the flags that load code or rewrite the classpath (-javaagent, -agentlib,
// -cp/-classpath, @argfile, -jar) are excluded by construction rather than by
// blocklist, so a JVM flag added upstream tomorrow cannot slip in.
func validateJVMExtraOpt(nodeIdx, optIdx int, a string) error {
	where := fmt.Sprintf("nodes[%d].jvm.extra_opts[%d]", nodeIdx, optIdx)

	if a == "" {
		return fmt.Errorf("%s: empty JVM option", where)
	}
	for _, r := range a {
		if r == ' ' || r == '\t' {
			return fmt.Errorf("%s: %q contains whitespace; JVM options are joined "+
				"with spaces, so one entry must be exactly one argument (list them separately)",
				where, a)
		}
		if r == '"' || r == '\'' || r == '\\' || r == '$' || r == '`' {
			return fmt.Errorf("%s: %q contains %q, which would not survive the "+
				"compose command list / systemd ExecStart line intact", where, a, r)
		}
	}

	switch {
	case strings.HasPrefix(a, "-XX:") && len(a) > len("-XX:"):
		return nil
	case strings.HasPrefix(a, "-D") && len(a) > 2:
		if eq := strings.IndexByte(a[2:], '='); eq <= 0 {
			return fmt.Errorf("%s: malformed system property %q: expected -D<key>=<value>",
				where, a)
		}
		return nil
	}
	return fmt.Errorf("%s: disallowed JVM option %q: only -D<key>=<value> and -XX:… "+
		"are permitted here (heap and GC have dedicated fields; flags that load code "+
		"— -javaagent, -agentlib, -cp, @argfile — are not accepted)", where, a)
}
