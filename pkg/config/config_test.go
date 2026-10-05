package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"

	"github.com/openshift-hyperfleet/hyperfleet-e2e/cmd/hyperfleet-e2e/common"
	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/config"
)

// loadConfig runs the same loading path as the CLI: common.LoadConfig binds the
// HYPERFLEET_* env vars, then config.Load unmarshals, applies defaults and validates.
// viper is process-global, so these tests must not run in parallel.
func loadConfig(t *testing.T, configFile string, env map[string]string) *config.Config {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	// Isolate from the developer's shell: an empty value counts as unset for viper.
	for _, key := range []string{
		"HYPERFLEET_CONFIG",
		"HYPERFLEET_DESIRE_ADAPTER",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("HYPERFLEET_API_URL", "http://hyperfleet-api.invalid:8000")
	t.Setenv("RUN_ID", "unit-test")
	t.Setenv("BROKER_TYPE", "rabbitmq")
	for key, value := range env {
		t.Setenv(key, value)
	}

	if err := common.LoadConfig(configFile); err != nil {
		t.Fatalf("LoadConfig(%q): %v", configFile, err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}

func TestDesireAdapterKey(t *testing.T) {
	tests := []struct {
		name       string
		configYAML string // written to a temp file when set
		configPath string // an existing file, used as-is when set
		env        map[string]string
		want       string
	}{
		{
			name: "default",
			want: config.DefaultDesireAdapter,
		},
		{
			name: "env var overrides the default",
			env:  map[string]string{"HYPERFLEET_DESIRE_ADAPTER": "cl-desire-env"},
			want: "cl-desire-env",
		},
		{
			name:       "config file sets the key",
			configYAML: "desire:\n  adapter: cl-desire-file\n",
			want:       "cl-desire-file",
		},
		{
			name:       "env var overrides the config file",
			configYAML: "desire:\n  adapter: cl-desire-file\n",
			env:        map[string]string{"HYPERFLEET_DESIRE_ADAPTER": "cl-desire-env"},
			want:       "cl-desire-env",
		},
		{
			name:       "shipped configs/config.yaml",
			configPath: filepath.Join("..", "..", "configs", "config.yaml"),
			want:       "cl-desire",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configFile := tt.configPath
			if tt.configYAML != "" {
				configFile = writeConfigFile(t, tt.configYAML)
			}
			cfg := loadConfig(t, configFile, tt.env)

			if got := cfg.Desire.Adapter; got != tt.want {
				t.Errorf("Desire.Adapter = %q, want %q", got, tt.want)
			}
		})
	}
}
