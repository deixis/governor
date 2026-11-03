// Package config loads and validates the optional .governor YAML file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Default values for runner configuration.
const (
	DefaultTimeout   = 5 * time.Minute
	DefaultMaxOutput = 1 << 20 // 1 MB
)

// Config holds the parsed .governor configuration.
// All fields are optional; zero values represent defaults.
type Config struct {
	Version      int               `yaml:"version"`
	RawTimeout   string            `yaml:"timeout"`    // e.g. "5m", "30s"
	RawMaxOutput int               `yaml:"max_output"` // bytes
	Test         TestConfig        `yaml:"test"`
	Lint         LintConfig        `yaml:"lint"`
	Staticcheck  StaticcheckConfig `yaml:"staticcheck"`
	Check        CheckConfig       `yaml:"check"`
	Audit        AuditConfig       `yaml:"audit"`
}

// Timeout returns the configured timeout or the default.
func (c *Config) Timeout() time.Duration {
	if c.RawTimeout != "" {
		d, err := time.ParseDuration(c.RawTimeout)
		if err == nil && d > 0 {
			return d
		}
	}
	return DefaultTimeout
}

// MaxOutputBytes returns the configured max output size or the default.
func (c *Config) MaxOutputBytes() int {
	if c.RawMaxOutput > 0 {
		return c.RawMaxOutput
	}
	return DefaultMaxOutput
}

// Every step takes an Exclude list: package patterns (e.g. ./e2e/...)
// subtracted from the selection for that step only.

// TestConfig controls how gov_test is executed.
type TestConfig struct {
	Args    []string `yaml:"args"` // extra flags appended to go test -json (e.g. -race, -count=1)
	Exclude []string `yaml:"exclude"`
}

// LintConfig controls how gov_lint is executed.
type LintConfig struct {
	Config  string   `yaml:"config"` // path to golangci-lint config file
	Args    []string `yaml:"args"`   // extra flags (e.g. --timeout=5m)
	Exclude []string `yaml:"exclude"`
}

// CheckConfig defines the steps for gov_check.
type CheckConfig struct {
	Steps []string `yaml:"steps"` // default: [test, lint, staticcheck]
}

// StaticcheckConfig controls how staticcheck is executed.
type StaticcheckConfig struct {
	Checks  []string `yaml:"checks"` // e.g. ["all", "-ST1000"]
	Args    []string `yaml:"args"`   // extra flags
	Exclude []string `yaml:"exclude"`
}

// AuditConfig defines the steps and per-check settings for gov_audit.
type AuditConfig struct {
	Steps      []string         `yaml:"steps"` // default: [coverage, complexity, deadcode, dupl, vulncheck]
	Coverage   CoverageConfig   `yaml:"coverage"`
	Complexity ComplexityConfig `yaml:"complexity"`
	Deadcode   DeadcodeConfig   `yaml:"deadcode"`
	Dupl       DuplConfig       `yaml:"dupl"`
	Vulncheck  VulncheckConfig  `yaml:"vulncheck"`
}

// VulncheckConfig controls how govulncheck is executed.
type VulncheckConfig struct {
	Args    []string `yaml:"args"` // extra flags for govulncheck
	Exclude []string `yaml:"exclude"`
}

// CoverageConfig controls how test coverage is collected.
type CoverageConfig struct {
	Args    []string `yaml:"args"` // extra flags for go test -coverprofile
	Exclude []string `yaml:"exclude"`
}

// ComplexityConfig controls how cognitive complexity is measured.
type ComplexityConfig struct {
	Args    []string `yaml:"args"` // extra flags for gocognit
	Exclude []string `yaml:"exclude"`
}

// DeadcodeConfig controls how dead code detection is run.
type DeadcodeConfig struct {
	Args    []string `yaml:"args"` // extra flags for deadcode
	Exclude []string `yaml:"exclude"`
}

// DuplConfig controls how duplicate code detection is run.
type DuplConfig struct {
	Threshold int      `yaml:"threshold"` // minimum token length (default: 50)
	Args      []string `yaml:"args"`      // extra flags for dupl
	Exclude   []string `yaml:"exclude"`
}

// DefaultCheckSteps are used when no steps are configured.
var DefaultCheckSteps = []string{"test", "lint", "staticcheck"}

// DefaultAuditSteps are used when no audit steps are configured.
var DefaultAuditSteps = []string{"coverage", "complexity", "deadcode", "dupl", "vulncheck"}

// CheckSteps returns the configured check steps, falling back to defaults.
func (c *Config) CheckSteps() []string {
	if len(c.Check.Steps) > 0 {
		return c.Check.Steps
	}
	return DefaultCheckSteps
}

// AuditSteps returns the configured audit steps, falling back to defaults.
func (c *Config) AuditSteps() []string {
	if len(c.Audit.Steps) > 0 {
		return c.Audit.Steps
	}
	return DefaultAuditSteps
}

// DuplThreshold returns the configured dupl token threshold, falling back to 50.
func (c *Config) DuplThreshold() int {
	if c.Audit.Dupl.Threshold > 0 {
		return c.Audit.Dupl.Threshold
	}
	return 50
}

// LoadResult holds the parsed config and the discovered repository root.
type LoadResult struct {
	Config   *Config
	RepoRoot string // directory containing go.mod; falls back to workspace
}

// FileNames lists the accepted config file names, in lookup order.
var FileNames = []string{".governor", ".governor.yaml", ".governor.yml"}

// Load reads the config file from the repository root. The repository
// root is discovered by walking upward from workspace looking for go.mod.
// Any name in FileNames is accepted; more than one present is an error,
// since a silently ignored file is worse than no file. With no file, a
// default Config is returned.
func Load(workspace string) (*LoadResult, error) {
	root, err := findRepoRoot(workspace)
	if err != nil {
		// No go.mod found; use workspace as root.
		root = workspace
	}

	var found []string
	for _, name := range FileNames {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			found = append(found, name)
		}
	}
	switch len(found) {
	case 0:
		return &LoadResult{Config: &Config{}, RepoRoot: root}, nil
	case 1:
	default:
		return nil, fmt.Errorf("multiple config files in %s: %s; keep one", root, strings.Join(found, ", "))
	}

	name := found[0]
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", name, err)
	}
	return &LoadResult{Config: cfg, RepoRoot: root}, nil
}

// findRepoRoot walks upward from dir looking for a directory containing go.mod.
func findRepoRoot(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}
