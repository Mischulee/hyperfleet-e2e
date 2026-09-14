package helper

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"

	"log/slog"
)

// HelmChartCloneOptions contains configuration for cloning a Helm chart repository
type HelmChartCloneOptions struct {
	// Component is the component name (e.g., "adapter", "api", "sentinel")
	Component string

	// RepoURL is the Git repository URL
	RepoURL string

	// Ref is the branch or tag to clone
	// Note: Commit SHAs are not supported due to git clone --branch limitations
	Ref string

	// ChartPath is the path within the repository to the chart directory
	// This will be used for sparse checkout to minimize download size
	ChartPath string

	// WorkDir is the base work directory for cloning
	// If empty, uses "./test-work" in current directory
	WorkDir string
}

// CloneHelmChart clones a Helm chart repository using sparse checkout to minimize download size.
// It returns the full path to the cloned chart and a cleanup function.
func (h *Helper) CloneHelmChart(ctx context.Context, opts HelmChartCloneOptions) (chartPath string, cleanup func() error, err error) {
	// Validate required fields
	if opts.Component == "" {
		return "", nil, fmt.Errorf("component is required")
	}
	if opts.RepoURL == "" {
		return "", nil, fmt.Errorf("repoURL is required")
	}
	if opts.Ref == "" {
		return "", nil, fmt.Errorf("ref is required")
	}
	if opts.ChartPath == "" {
		return "", nil, fmt.Errorf("ChartPath is required")
	}

	// Set default work directory if not specified
	workDir := opts.WorkDir
	if workDir == "" {
		// Default to ./.test-work in current directory
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, fmt.Errorf("failed to get current directory: %w", err)
		}
		workDir = filepath.Join(cwd, TestWorkDir)
	}

	// Ensure work directory exists before cloning
	if err := os.MkdirAll(workDir, 0750); err != nil {
		return "", nil, fmt.Errorf("failed to create work directory: %w", err)
	}

	// Create an isolated component-specific directory per invocation
	// This prevents race conditions when parallel tests clone the same component
	componentDir, err := os.MkdirTemp(workDir, opts.Component+"-")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create component work directory: %w", err)
	}

	// Cleanup function to remove the cloned repository
	cleanup = func() error {
		slog.Info("cleaning up cloned Helm chart", "path", componentDir)
		if err := os.RemoveAll(componentDir); err != nil {
			return fmt.Errorf("failed to remove cloned chart directory: %w", err)
		}
		return nil
	}

	// Redact credentials from RepoURL before logging
	redactedRepo := opts.RepoURL
	if u, err := url.Parse(opts.RepoURL); err == nil && u.User != nil {
		u.User = url.User("***")
		redactedRepo = u.String()
	}

	slog.Info("cloning Helm chart repository",
		"component", opts.Component,
		"repo", redactedRepo,
		"ref", opts.Ref,
		"chart_path", opts.ChartPath,
		"dest", componentDir)

	// Step 1: Clone with sparse checkout (no files yet)
	slog.Info("executing sparse checkout git clone")
	cmd := exec.CommandContext(ctx, "git", "clone", // #nosec G204 -- opts are from trusted config
		"--depth", "1",
		"--filter=blob:none",
		"--sparse",
		"--no-checkout",
		"--branch", opts.Ref,
		opts.RepoURL,
		componentDir)

	if output, err := cmd.CombinedOutput(); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("git clone failed: %w\nOutput: %s", err, string(output))
	}

	// Step 2: Configure sparse checkout - only checkout the chart path
	slog.Info("configuring sparse checkout", "sparse_path", opts.ChartPath)

	// Initialize sparse checkout (no cone mode)
	cmd = exec.CommandContext(ctx, "git", "sparse-checkout", "init", "--no-cone")
	cmd.Dir = componentDir
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("sparse-checkout init failed: %w\nOutput: %s", err, string(output))
	}

	// Set sparse checkout path
	cmd = exec.CommandContext(ctx, "git", "sparse-checkout", "set", opts.ChartPath) // #nosec G204 -- opts.ChartPath is from trusted config
	cmd.Dir = componentDir
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("sparse-checkout set failed: %w\nOutput: %s", err, string(output))
	}

	// Checkout the files
	slog.Info("checking out files")
	cmd = exec.CommandContext(ctx, "git", "checkout", opts.Ref) // #nosec G204 -- opts.Ref is from trusted config
	cmd.Dir = componentDir
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("git checkout failed: %w\nOutput: %s", err, string(output))
	}

	// Verify Chart.yaml exists in the cloned chart directory
	fullChartPath := filepath.Join(componentDir, opts.ChartPath)
	chartYamlPath := filepath.Join(fullChartPath, "Chart.yaml")
	if _, err := os.Stat(chartYamlPath); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("chart.yaml not found at %s (verify ChartPath is correct): %w", fullChartPath, err)
	}

	slog.Info("Helm chart cloned successfully",
		"component", opts.Component,
		"chart_path", fullChartPath)

	return fullChartPath, cleanup, nil
}
