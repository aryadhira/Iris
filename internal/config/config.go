// Package config loads Iris runtime configuration from the environment only.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAddr          = "127.0.0.1:3300"
	DefaultHermesURL     = "http://127.0.0.1:8642"
	DefaultHermesKeyFile = "~/.config/iris-api-key"
	DefaultDataDir       = "~/iris-data"
	DefaultVoiceURL      = "http://127.0.0.1:3310"
	DefaultWebDir        = "web/dist"
)

// Config is the complete runtime configuration. Secrets are held in memory only
// and are never included in String or error messages.
type Config struct {
	Addr          string
	DatabaseURL   string
	HermesURL     string
	HermesKeyFile string
	HermesKey     string
	VoiceURL      string
	Passphrase    string
	CookieSecure  bool
	DataDir       string
	// WebDir is the built SPA irisd serves. It is deliberately optional: a
	// missing build is a broken UI, never a broken API.
	WebDir string
	// ApprovalTimeout mirrors Hermes' approvals.timeout. Iris expires its
	// pending card at the same wall-clock point rather than claiming a decision.
	ApprovalTimeout time.Duration
	// ApprovalFixture registers a synthetic approval endpoint for browser
	// tests. Off by default and never for a real deployment.
	ApprovalFixture bool
}

// Load reads IRIS_* variables, validates them, and refuses clearly when a
// required value or the Hermes key file is missing.
func Load() (*Config, error) {
	cfg := &Config{
		Addr:          envOr("IRIS_ADDR", DefaultAddr),
		DatabaseURL:   strings.TrimSpace(os.Getenv("IRIS_DATABASE_URL")),
		HermesURL:     strings.TrimRight(envOr("IRIS_HERMES_URL", DefaultHermesURL), "/"),
		HermesKeyFile: envOr("IRIS_HERMES_KEY_FILE", DefaultHermesKeyFile),
		VoiceURL:      strings.TrimRight(envOr("IRIS_VOICE_URL", DefaultVoiceURL), "/"),
		Passphrase:    os.Getenv("IRIS_PASSPHRASE"),
	}

	secure, err := parseBool(os.Getenv("IRIS_COOKIE_SECURE"))
	if err != nil {
		return nil, fmt.Errorf("IRIS_COOKIE_SECURE must be true or false: %w", err)
	}
	cfg.CookieSecure = secure

	if cfg.DatabaseURL == "" {
		return nil, errors.New("IRIS_DATABASE_URL is required (example: postgres://lab:labdev@127.0.0.1:5432/iris?sslmode=disable)")
	}
	if cfg.Passphrase == "" {
		return nil, errors.New("IRIS_PASSPHRASE is required; Iris refuses to start without an owner passphrase")
	}

	dataDir, err := ExpandHome(envOr("IRIS_DATA_DIR", DefaultDataDir))
	if err != nil {
		return nil, fmt.Errorf("IRIS_DATA_DIR is not a usable path: %w", err)
	}
	cfg.DataDir = dataDir

	approvalTimeout, err := parseSeconds(envOr("IRIS_APPROVAL_TIMEOUT", "300"))
	if err != nil {
		return nil, fmt.Errorf("IRIS_APPROVAL_TIMEOUT must be a whole number of seconds: %w", err)
	}
	cfg.ApprovalTimeout = approvalTimeout

	fixture, err := parseBool(os.Getenv("IRIS_APPROVAL_FIXTURE"))
	if err != nil {
		return nil, fmt.Errorf("IRIS_APPROVAL_FIXTURE must be true or false: %w", err)
	}
	cfg.ApprovalFixture = fixture

	webDir, err := ExpandHome(envOr("IRIS_WEB_DIR", DefaultWebDir))
	if err != nil {
		return nil, fmt.Errorf("IRIS_WEB_DIR is not a usable path: %w", err)
	}
	cfg.WebDir = webDir

	keyPath, err := ExpandHome(cfg.HermesKeyFile)
	if err != nil {
		return nil, fmt.Errorf("IRIS_HERMES_KEY_FILE is not a usable path: %w", err)
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("Hermes API key file not found at %s; set IRIS_HERMES_KEY_FILE or create the file", keyPath)
		}
		return nil, fmt.Errorf("cannot read Hermes API key file at %s: %v", keyPath, err)
	}
	cfg.HermesKey = strings.TrimSpace(string(keyBytes))
	if cfg.HermesKey == "" {
		return nil, fmt.Errorf("Hermes API key file at %s is empty", keyPath)
	}
	cfg.HermesKeyFile = keyPath

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseSeconds(v string) (time.Duration, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("got %q", v)
	}
	return time.Duration(n) * time.Second, nil
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "false", "0", "no":
		return false, nil
	case "true", "1", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("unrecognised value %q", v)
	}
}

// ExpandHome expands a leading ~ in a path.
func ExpandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}
