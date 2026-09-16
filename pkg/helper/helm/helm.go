package helm

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"

	"log/slog"
)

// Client wraps Helm SDK functionality
type Client struct {
	settings     *cli.EnvSettings
	namespace    string
	actionConfig *action.Configuration
	configOnce   sync.Once
	configErr    error
}

// NewClient creates a new Helm client using default environment settings
func NewClient(namespace string) *Client {
	return &Client{
		settings:  cli.New(),
		namespace: namespace,
	}
}

// initActionConfig initializes Helm action configuration once and caches it
// Subsequent calls return the cached config
func (c *Client) initActionConfig() (*action.Configuration, error) {
	c.configOnce.Do(func() {
		actionConfig := new(action.Configuration)

		// Use the default Helm driver (secrets)
		helmDriver := ""

		// Initialize with REST client getter, namespace, and driver
		if err := actionConfig.Init(c.settings.RESTClientGetter(), c.namespace, helmDriver, func(format string, v ...interface{}) {
			slog.Info(fmt.Sprintf(format, v...))
		}); err != nil {
			c.configErr = fmt.Errorf("failed to init Helm action config: %w", err)
			return
		}

		c.actionConfig = actionConfig
		slog.Info("initialized Helm action config", "namespace", c.namespace)
	})

	return c.actionConfig, c.configErr
}

// ListReleases lists all Helm releases across client namespace with the given label selector
// labelSelector uses Kubernetes label selector format (e.g., "e2e.hyperfleet.io/run-id=test-123")
// Returns a list of release names
func (c *Client) ListReleasesBySelector(labelSelector string) ([]string, error) {
	// Initialize action config for all namespaces (empty string means all)
	actionConfig, err := c.initActionConfig()
	if err != nil {
		return nil, err
	}

	listClient := action.NewList(actionConfig)
	listClient.All = true               // List releases in all states (not just deployed)
	listClient.AllNamespaces = false    // Search only in the configured namespace
	listClient.SetStateMask()           // Set state mask to include all states
	listClient.Selector = labelSelector // Use Kubernetes label selector

	results, err := listClient.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to list Helm releases: %w", err)
	}

	releases := []string{}
	for _, rel := range results {
		// check that helm list is only listing releases in namespace
		if rel.Namespace != c.namespace {
			slog.Warn("helm incorrectly listing releases outside namespace")
			continue
		}
		releases = append(releases, rel.Name)
		slog.Info("found Helm release", "release", rel.Name)
	}

	return releases, nil
}

// UninstallRelease uninstalls the helm release. This workflow matches the way the adapters are currently installed.
// Future work can be done to move helm releases to be installed with helm sdk
func (c *Client) UninstallRelease(ctx context.Context, releaseName, namespace string) error {
	slog.Info("uninstalling helm release",
		"release_name", releaseName,
		"namespace", namespace)

	// Create context with timeout
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Execute Helm uninstall command
	cmd := exec.CommandContext(cmdCtx, "helm", "uninstall", releaseName,
		"-n", namespace,
		"--wait",
		"--timeout", "5m")

	output, err := cmd.CombinedOutput()

	if err != nil {
		return fmt.Errorf("failed to uninstall release: %w (output: %s)", err, string(output))
	}

	slog.Info("helm uninstall completed", "release", releaseName, "namespace", namespace)
	return nil
}
