package helper

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pubsubadmin "cloud.google.com/go/pubsub/v2/apiv1"
	pubsubpb "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/config"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log/slog"
)

type AdapterDeployment struct {
	ReleaseName  string
	AdapterName  string
	ResourceType string
}

type AdapterDeploymentList struct {
	mu    sync.RWMutex
	items []AdapterDeployment
}

func (l *AdapterDeploymentList) Add(deployment AdapterDeployment) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, deployment)
}

// Snapshot returns a thread-safe copy of all adapter deployments
func (l *AdapterDeploymentList) Snapshot() []AdapterDeployment {
	l.mu.RLock()
	defer l.mu.RUnlock()
	snapshot := make([]AdapterDeployment, len(l.items))
	copy(snapshot, l.items)
	return snapshot
}

func InitAdapterDeploymentList() *AdapterDeploymentList {
	return &AdapterDeploymentList{
		items: make([]AdapterDeployment, 0),
	}
}

// generateRandomString generates a random alphanumeric string of the specified length
func generateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			// Fallback: use current time nanoseconds for basic randomness
			b[i] = charset[(time.Now().UnixNano()+int64(i))%int64(len(charset))]
		} else {
			b[i] = charset[n.Int64()]
		}
	}
	return string(b)
}

// AdapterDeploymentOptions contains configuration for deploying an adapter via Helm
type AdapterDeploymentOptions struct {
	ReleaseName  string
	Namespace    string
	ChartPath    string
	AdapterName  string
	Timeout      time.Duration
	SetValues    map[string]string // Additional Helm --set values
	ResourceType string
}

// GenerateAdapterReleaseName generates a deterministic Helm release name for an adapter deployment.
// The release name format is: adapter-<resource_type>-<adapter_name>
// Deterministic naming allows helm upgrade --install to upgrade in place and avoids duplicate releases.
// The name is truncated to 48 characters to leave room for Helm's deployment/pod suffixes (Kubernetes has a 63-char limit).
// If truncation is needed, a deterministic hash is appended to prevent collisions between long names.
const maxReleaseNameLength = 48

func GenerateAdapterReleaseName(resourceType, adapterName string) string {
	releaseName := fmt.Sprintf("adapter-%s-%s", resourceType, adapterName)

	if len(releaseName) > maxReleaseNameLength {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(releaseName)))[:8]
		truncLen := maxReleaseNameLength - len(hash) - 1
		releaseName = releaseName[:truncLen] + "-" + hash
	}

	return releaseName
}

// DeployAdapter deploys an adapter using Helm upgrade --install
// This is a common function that can be reused across test cases
// The release name must be provided via opts.ReleaseName - use GenerateAdapterReleaseName() to create a unique name
func (h *Helper) DeployAdapter(ctx context.Context, opts AdapterDeploymentOptions) error {
	// Validate required fields
	if opts.Namespace == "" {
		return fmt.Errorf("AdapterDeploymentOptions.Namespace is required")
	}
	if opts.ChartPath == "" {
		return fmt.Errorf("AdapterDeploymentOptions.ChartPath is required")
	}
	if opts.AdapterName == "" {
		return fmt.Errorf("AdapterDeploymentOptions.AdapterName is required")
	}
	if opts.ReleaseName == "" {
		return fmt.Errorf("AdapterDeploymentOptions.ReleaseName is required - use GenerateAdapterReleaseName() to create a unique name")
	}

	// Set default timeout if not specified
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}

	releaseName := opts.ReleaseName

	slog.Info("deploying adapter via Helm",
		"adapter_name", opts.AdapterName,
		"release_name", releaseName,
		"namespace", opts.Namespace)

	// Copy adapter config folder to chart directory
	sourceAdapterDir := filepath.Join(h.Cfg.TestDataDir, AdapterConfigsDir, opts.AdapterName)
	destAdapterDir := filepath.Join(opts.ChartPath, opts.AdapterName)

	// Remove existing adapter config directory if it exists
	if _, err := os.Stat(destAdapterDir); err == nil {
		slog.Info("removing existing adapter config directory", "path", destAdapterDir)
		if err := os.RemoveAll(destAdapterDir); err != nil {
			return fmt.Errorf("failed to remove existing adapter config directory: %w", err)
		}
	}

	// Copy adapter config directory to chart
	slog.Info("copying adapter config", "from", sourceAdapterDir, "to", destAdapterDir)
	if err := copyDir(sourceAdapterDir, destAdapterDir); err != nil {
		return fmt.Errorf("failed to copy adapter config directory: %w", err)
	}

	// Determine the values.yaml file path in the copied adapter directory
	valuesFilePath := filepath.Join(destAdapterDir, "values.yaml")

	// Default BROKER_TYPE to googlepubsub if not set so envsubst produces a valid value
	if os.Getenv("BROKER_TYPE") == "" {
		if err := os.Setenv("BROKER_TYPE", "googlepubsub"); err != nil {
			return fmt.Errorf("failed to set default BROKER_TYPE: %w", err)
		}
		defer func() { _ = os.Unsetenv("BROKER_TYPE") }()
	}

	// Compute extra environment variables for the envsubst subprocess.
	// These are scoped to the subprocess and do not mutate the process-global environment.
	var extraEnv []string

	// When using GCP Pub/Sub, ensure the subscription is created if it doesn't exist.
	// This is required for adapters deployed for the first time (no pre-existing subscription).
	if os.Getenv("BROKER_TYPE") == "googlepubsub" && os.Getenv("ADAPTER_GOOGLEPUBSUB_CREATE_SUBSCRIPTION_IF_MISSING") == "" {
		extraEnv = append(extraEnv, "ADAPTER_GOOGLEPUBSUB_CREATE_SUBSCRIPTION_IF_MISSING=true")
	}

	// Resolve the in-cluster API URL for adapters. This is intentionally separate from
	// HYPERFLEET_API_URL, which points wherever the e2e test process itself reaches the API
	// (e.g. a port-forward or external LB address) and is not routable from in-cluster pods.
	// Adapters reach the API via the in-cluster hyperfleet-gateway Service, which lives in the
	// same namespace adapters are deployed into.
	apiURL := os.Getenv("ADAPTER_HYPERFLEET_API_URL")
	if apiURL == "" {
		apiURL = config.DefaultHyperfleetAPIBaseURL
	}

	// Expand environment variables in values.yaml in-place using envsubst
	slog.Info("expanding environment variables in values.yaml in-place", "values_file", valuesFilePath)

	expandedContent, err := expandEnvVarsInYAMLToBytes(ctx, valuesFilePath, extraEnv)
	if err != nil {
		return fmt.Errorf("failed to expand environment variables in values.yaml: %w", err)
	}
	if err := os.WriteFile(valuesFilePath, expandedContent, 0600); err != nil {
		return fmt.Errorf("failed to overwrite values.yaml with expanded content: %w", err)
	}

	slog.Info("successfully expanded environment variables in values.yaml")

	// Expand environment variables in adapter-config.yaml in-place using envsubst.
	// This allows adapter configs to reference env vars like ${HYPERFLEET_API_URL}
	// so the correct API endpoint is injected at deploy time regardless of namespace.
	adapterConfigPath := filepath.Join(destAdapterDir, "adapter-config.yaml")
	if _, statErr := os.Stat(adapterConfigPath); statErr == nil {
		expandedAdapterConfig, err := expandEnvVarsInYAMLToBytes(ctx, adapterConfigPath, extraEnv)
		if err != nil {
			return fmt.Errorf("failed to expand environment variables in adapter-config.yaml: %w", err)
		}
		if err := os.WriteFile(adapterConfigPath, expandedAdapterConfig, 0600); err != nil {
			return fmt.Errorf("failed to overwrite adapter-config.yaml with expanded content: %w", err)
		}
		slog.Info("successfully expanded environment variables in adapter-config.yaml")
	}

	// Build Helm command with values file
	helmArgs := []string{
		"upgrade", "--install",
		releaseName,
		opts.ChartPath,
		"--namespace", opts.Namespace,
		"--create-namespace",
		"--wait",
		"--timeout", opts.Timeout.String(),
		"-f", valuesFilePath,
	}

	// Override chart's default hyperfleetApi.baseUrl with the resolved API URL via --set.
	helmArgs = append(helmArgs, "--set", "adapterConfig.hyperfleetApi.baseUrl="+apiURL)

	// Append conditional --set flags (opts.SetValues is applied last, so tests can still override
	// the base URL, e.g. to simulate an unreachable API)
	helmArgs = append(helmArgs, h.adapterHelmSetArgs(releaseName, opts)...)

	slog.Info("executing Helm command", "args", helmArgs)

	// Create context with timeout
	cmdCtx, cancel := context.WithTimeout(ctx, opts.Timeout+30*time.Second)
	defer cancel()

	// Execute Helm command
	cmd := exec.CommandContext(cmdCtx, "helm", helmArgs...) // #nosec G204 -- helmArgs is constructed from trusted config
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("helm upgrade failed", "error", err, "output", string(output))

		// Collect diagnostic information when deployment fails
		h.saveDiagnosticLogs(ctx, opts.AdapterName, releaseName, opts.Namespace)

		return fmt.Errorf("helm upgrade failed: %w (output: %s)", err, string(output))
	}

	// Add adapter deployment to list for cleanup
	h.AdapterDeploymentList.Add(AdapterDeployment{
		ReleaseName:  releaseName,
		AdapterName:  opts.AdapterName,
		ResourceType: opts.ResourceType,
	})

	slog.Info("adapter deployed successfully",
		"release_name", releaseName,
		"output", string(output))

	return nil
}

// adapterHelmSetArgs builds the conditional --set flags for adapter Helm deployments.
// Extracted for testability - DeployAdapter calls this to append flags after the base args.
func (h *Helper) adapterHelmSetArgs(releaseName string, opts AdapterDeploymentOptions) []string {
	var args []string

	// Ensure consistent release naming
	args = append(args, "--set", fmt.Sprintf("fullnameOverride=%s", releaseName))

	// Add run-id label for resource tracking and cleanup
	if h.Cfg.RunID != "" {
		args = append(args, "--labels", fmt.Sprintf("e2e.hyperfleet.io/run-id=%s", h.Cfg.RunID))
	}

	// Override image pull policy if set (e.g. IfNotPresent for local kind clusters)
	if policy := os.Getenv("IMAGE_PULL_POLICY"); policy != "" {
		args = append(args, "--set", fmt.Sprintf("image.pullPolicy=%s", policy))
	}

	// Enable adapter API auth when JWT is enabled on the API server
	if h.Cfg.Identity.TokenRequest.IsEnabled() {
		args = append(args, "--set", "adapterConfig.hyperfleetApi.auth.enabled=true")
	}

	// Add additional --set values if provided
	for key, value := range opts.SetValues {
		args = append(args, "--set", fmt.Sprintf("%s=%s", key, value))
	}

	return args
}

// UninstallAdapter uninstalls an adapter using Helm uninstall
// This is a common function that can be reused across test cases
func (h *Helper) UninstallAdapter(ctx context.Context, releaseName, namespace string) error {
	slog.Info("uninstalling adapter via Helm",
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
		// Check if the error is because the release doesn't exist
		if strings.Contains(string(output), "not found") {
			slog.Info("adapter release not found, skipping uninstall", "release_name", releaseName)
			// Clean up orphaned cluster-scoped resources even when release is not found
			// This handles cases like interrupted installs or manual deletions
			h.cleanupClusterScopedResources(ctx, releaseName)
			return nil
		}
		slog.Error("helm uninstall failed", "error", err, "output", string(output))
		return fmt.Errorf("helm uninstall failed: %w (output: %s)", err, string(output))
	}

	slog.Info("adapter uninstalled successfully",
		"release_name", releaseName,
		"output", string(output))

	// Clean up any orphaned cluster-scoped resources (ClusterRoles, ClusterRoleBindings)
	// These can be left behind if a previous test run failed or was interrupted
	h.cleanupClusterScopedResources(ctx, releaseName)

	return nil
}

// cleanupClusterScopedResources removes orphaned cluster-scoped resources that may be left
// after Helm uninstall. This is a best-effort cleanup and logs errors without failing.
// Uses label selectors instead of names so it works regardless of the chart's naming scheme.
func (h *Helper) cleanupClusterScopedResources(ctx context.Context, releaseName string) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	labelSelector := fmt.Sprintf("app.kubernetes.io/instance=%s", releaseName)

	// Try to delete ClusterRole
	clusterRoleCmd := exec.CommandContext(cmdCtx, "kubectl", "delete", "clusterrole", //nolint:gosec // labelSelector is derived from releaseName, not user input
		"-l", labelSelector,
		"--ignore-not-found=true")
	if output, err := clusterRoleCmd.CombinedOutput(); err != nil {
		slog.Info("could not delete ClusterRole (may not exist)",
			"release_name", releaseName,
			"output", string(output))
	} else {
		slog.Info("cleaned up ClusterRole", "release_name", releaseName)
	}

	// Try to delete ClusterRoleBinding
	clusterRoleBindingCmd := exec.CommandContext(cmdCtx, "kubectl", "delete", "clusterrolebinding", //nolint:gosec // labelSelector is derived from releaseName, not user input
		"-l", labelSelector,
		"--ignore-not-found=true")
	if output, err := clusterRoleBindingCmd.CombinedOutput(); err != nil {
		slog.Info("could not delete ClusterRoleBinding (may not exist)",
			"release_name", releaseName,
			"output", string(output))
	} else {
		slog.Info("cleaned up ClusterRoleBinding", "release_name", releaseName)
	}
}

// saveDiagnosticLogs saves diagnostic information when adapter deployment fails
// Saves to <outputDir>/<adapter-name>-<random-4chars>/ directory
// outputDir is configured via OUTPUT_DIR env var or config file (defaults to "output")
func (h *Helper) saveDiagnosticLogs(ctx context.Context, adapterName, releaseName, namespace string) {
	// Generate output directory with adapter name and random suffix
	randomSuffix := generateRandomString(4)
	outputDir := filepath.Join(h.Cfg.OutputDir, fmt.Sprintf("%s-%s", adapterName, randomSuffix))

	// Create output directory
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		slog.Error("failed to create diagnostic output directory",
			"error", err,
			"output_dir", outputDir)
		return
	}

	slog.Info("saving diagnostic logs",
		"adapter_name", adapterName,
		"release_name", releaseName,
		"namespace", namespace,
		"output_dir", outputDir)

	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 1. Get pods using client-go
	pods, err := h.K8sClient.CoreV1().Pods(namespace).List(cmdCtx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/instance=%s", releaseName),
	})
	if err != nil {
		slog.Error("failed to list pods", "error", err)
		return
	}

	if len(pods.Items) == 0 {
		slog.Info("no pods found for release", "release_name", releaseName)
		return
	}

	slog.Info("found pods for release",
		"total_pods", len(pods.Items),
		"release_name", releaseName)

	// Save logs and description for unhealthy pods only
	for _, pod := range pods.Items {
		// Check if pod is healthy (Running and all containers ready)
		isHealthy := pod.Status.Phase == "Running"
		if isHealthy && len(pod.Status.ContainerStatuses) > 0 {
			for _, cs := range pod.Status.ContainerStatuses {
				if !cs.Ready {
					isHealthy = false
					break
				}
			}
		}

		// Skip healthy pods
		if isHealthy {
			slog.Info("skipping healthy pod", "pod", pod.Name)
			continue
		}

		podName := pod.Name
		slog.Info("saving logs for unhealthy pod",
			"pod", podName,
			"phase", pod.Status.Phase)

		// Save pod logs using kubectl command
		podLogFile := filepath.Join(outputDir, fmt.Sprintf("%s.log", podName))
		podLogCmd := exec.CommandContext(cmdCtx, "kubectl", "logs", // #nosec G204 -- podName and namespace are from trusted k8s API
			podName,
			"-n", namespace,
			"--tail=200")

		var logContent string
		logContent += fmt.Sprintf("$ %s\n\n", podLogCmd.String())
		logOutput, err := podLogCmd.CombinedOutput()
		if err != nil {
			logContent += fmt.Sprintf("Error: %v\n", err)
			logContent += string(logOutput)
		} else {
			logContent += string(logOutput)
		}

		if err := os.WriteFile(podLogFile, []byte(logContent), 0600); err != nil {
			slog.Error("failed to write pod log file",
				"pod", podName,
				"error", err)
		} else {
			slog.Info("saved pod logs",
				"pod", podName,
				"file", podLogFile)
		}

		// Save pod description using kubectl describe command
		podDescFile := filepath.Join(outputDir, fmt.Sprintf("%s-describe.txt", podName))
		podDescCmd := exec.CommandContext(cmdCtx, "kubectl", "describe", "pod", // #nosec G204 -- podName and namespace are from trusted k8s API
			podName,
			"-n", namespace)

		var descContent string
		descContent += fmt.Sprintf("$ %s\n\n", podDescCmd.String())
		descOutput, err := podDescCmd.CombinedOutput()
		if err != nil {
			descContent += fmt.Sprintf("Error: %v\n", err)
			descContent += string(descOutput)
		} else {
			descContent += string(descOutput)
		}

		if err := os.WriteFile(podDescFile, []byte(descContent), 0600); err != nil {
			slog.Error("failed to write pod description file",
				"pod", podName,
				"error", err)
		} else {
			slog.Info("saved pod description",
				"pod", podName,
				"file", podDescFile)
		}
	}

	slog.Info("diagnostic logs saved successfully", "output_dir", outputDir)
}

// expandEnvVarsInYAMLToBytes expands environment variables in a YAML file using envsubst
// Returns the expanded content as bytes
func expandEnvVarsInYAMLToBytes(ctx context.Context, yamlPath string, extraEnv []string) ([]byte, error) {
	// Read the YAML file
	content, err := os.ReadFile(yamlPath) // #nosec G304 -- yamlPath is constructed from trusted config
	if err != nil {
		return nil, fmt.Errorf("failed to read YAML file: %w", err)
	}

	// Use envsubst command to expand environment variables
	cmd := exec.CommandContext(ctx, "envsubst")
	cmd.Stdin = bytes.NewReader(content)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("envsubst failed: %w (stderr: %s)", err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// PurgeAdapterQueue purges all pending messages from the broker queue for the given adapter.
// This is used before deploying a test adapter to avoid processing a stale message backlog
// accumulated while the adapter was uninstalled between test runs.
//
// Broker dispatch is determined by the BROKER_TYPE environment variable:
//   - "googlepubsub" (default when unset): deletes the Pub/Sub subscription so the adapter
//     recreates it fresh (the chart sets createSubscriptionIfMissing: true via DeployAdapter).
//     Subscription name pattern: {namespace}-clusters-{adapterName}
//   - "rabbitmq": purges the queue via rabbitmqctl.
//     Queue name pattern: {namespace}-clusters-{adapterName}-CHANGE_ME
//     The "CHANGE_ME" suffix is the literal subscription_id in adapter-config.yaml files.
//
// If the queue/subscription does not exist or the broker is unreachable, this is a no-op.
func (h *Helper) PurgeAdapterQueue(ctx context.Context, adapterName string) error {
	brokerType := os.Getenv("BROKER_TYPE")
	if brokerType == "" {
		brokerType = "googlepubsub" // matches DeployAdapter's default
	}
	switch brokerType {
	case "googlepubsub":
		subscriptionID := fmt.Sprintf("%s-clusters-%s", h.Cfg.Namespace, adapterName)
		return DeletePubSubSubscription(ctx, subscriptionID, h.Cfg.GCPProjectID)
	case "rabbitmq":
		return h.purgeRabbitMQQueue(ctx, adapterName)
	default:
		return fmt.Errorf("unsupported broker type %q", brokerType)
	}
}

// purgeRabbitMQQueue is the RabbitMQ-specific implementation used by PurgeAdapterQueue.
func (h *Helper) purgeRabbitMQQueue(ctx context.Context, adapterName string) error {
	const (
		rabbitMQNamespace    = "rabbitmq"
		rabbitMQPodLabelKey  = "app"
		rabbitMQPodLabelVal  = "rabbitmq"
		brokerSubscriptionID = "CHANGE_ME" // literal subscription_id in adapter-config.yaml
	)

	queueName := fmt.Sprintf("%s-clusters-%s-%s", h.Cfg.Namespace, adapterName, brokerSubscriptionID)
	slog.Info("purging RabbitMQ adapter queue", "queue", queueName, "adapter", adapterName)

	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	pods, err := h.K8sClient.CoreV1().Pods(rabbitMQNamespace).List(cmdCtx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", rabbitMQPodLabelKey, rabbitMQPodLabelVal),
	})
	if err != nil {
		slog.Error("failed to list RabbitMQ pods", "namespace", rabbitMQNamespace, "error", err)
		return fmt.Errorf("failed to list RabbitMQ pods in namespace %q: %w", rabbitMQNamespace, err)
	}
	if len(pods.Items) == 0 {
		slog.Info("no RabbitMQ pods found, skipping queue purge", "namespace", rabbitMQNamespace)
		return nil
	}

	podName := pods.Items[0].Name
	cmd := exec.CommandContext(cmdCtx, "kubectl", "exec", "-n", rabbitMQNamespace, // #nosec G204 -- podName and queueName from trusted config
		podName, "--", "rabbitmqctl", "purge_queue", queueName)

	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		if strings.Contains(outputStr, "not_found") || strings.Contains(outputStr, "does not exist") {
			slog.Info("queue not found, nothing to purge", "queue", queueName)
			return nil
		}
		return fmt.Errorf("failed to purge RabbitMQ queue %s: %w (output: %s)", queueName, err, outputStr)
	}

	slog.Info("RabbitMQ queue purged successfully", "queue", queueName)
	return nil
}

var newPubSubDeleteFunc = func(ctx context.Context, projectID, subID string) (func(context.Context) error, func(), error) {
	client, err := pubsubadmin.NewSubscriptionAdminClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Pub/Sub admin client: %w", err)
	}
	subPath := fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subID)
	deleteFn := func(ctx context.Context) error {
		return client.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{
			Subscription: subPath,
		})
	}
	return deleteFn, func() {
		if err := client.Close(); err != nil {
			slog.Error("failed to close Pub/Sub admin client", "error", err)
		}
	}, nil
}

// DeletePubSubResourcesForAdapter deletes Pub/Sub subscription and dlq topic for a given adapter.
func (h *Helper) DeletePubSubResourcesForAdapter(ctx context.Context, adapterName string, resourceType string) error {
	if h.Cfg.BrokerType != "googlepubsub" {
		slog.Info("skipping Pub/Sub subscription and topic deletion for non-Google Pub/Sub adapter", "adapter", adapterName)
		return nil
	}

	namespace := h.Cfg.Namespace
	projectID := h.Cfg.GCPProjectID
	subscriptionID := fmt.Sprintf("%s-%s-%s", namespace, resourceType, adapterName)
	topicID := fmt.Sprintf("%s-%s-%s-dlq", namespace, resourceType, adapterName)

	var errorList []string
	if err := DeletePubSubSubscription(ctx, subscriptionID, projectID); err != nil {
		errorList = append(errorList, subscriptionID)
	}
	if err := DeletePubSubTopic(ctx, topicID, projectID); err != nil {
		errorList = append(errorList, topicID)
	}
	if len(errorList) > 0 {
		return fmt.Errorf("failed to delete some Pub/Sub resources for adapter %s: %s", adapterName, strings.Join(errorList, ", "))
	}
	slog.Info("deleted Pub/Sub resources for adapter", "adapter", adapterName)
	return nil
}

// DeletePubSubSubscription deletes a Google Pub/Sub subscription using the Go SDK.
// If the subscription does not exist, it is treated as a no-op.
func DeletePubSubSubscription(ctx context.Context, subscriptionID string, projectID string) error {
	if projectID == "" {
		projectID = defaultGCPProjectID
	}

	slog.Info("deleting Pub/Sub subscription",
		"subscription", subscriptionID,
		"project", projectID)

	deleteFn, cleanup, err := newPubSubDeleteFunc(ctx, projectID, subscriptionID)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := deleteFn(ctx); err != nil {
		if status.Code(err) == codes.NotFound {
			slog.Info("Pub/Sub subscription not found, skipping deletion", "subscription", subscriptionID)
			return nil
		}
		slog.Error("failed to delete Pub/Sub subscription",
			"subscription", subscriptionID,
			"project", projectID,
			"error", err)
		return fmt.Errorf("failed to delete Pub/Sub subscription %s: %w", subscriptionID, err)
	}

	slog.Info("Pub/Sub subscription deleted successfully", "subscription", subscriptionID)
	return nil
}

var newPubSubTopicDeleteFunc = func(ctx context.Context, projectID, topicID string) (func(context.Context) error, func(), error) {
	client, err := pubsubadmin.NewTopicAdminClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Pub/Sub topic admin client: %w", err)
	}
	topicPath := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	deleteFn := func(ctx context.Context) error {
		return client.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{
			Topic: topicPath,
		})
	}
	return deleteFn, func() {
		if err := client.Close(); err != nil {
			slog.Error("failed to close Pub/Sub topic admin client", "error", err)
		}
	}, nil
}

// DeletePubSubTopic deletes a Google Pub/Sub topic using the Go SDK.
// If the topic does not exist, it is treated as a no-op.
func DeletePubSubTopic(ctx context.Context, topicID string, projectID string) error {
	if projectID == "" {
		projectID = defaultGCPProjectID
	}

	slog.Info("deleting Pub/Sub topic",
		"topic", topicID,
		"project", projectID)

	deleteFn, cleanup, err := newPubSubTopicDeleteFunc(ctx, projectID, topicID)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := deleteFn(ctx); err != nil {
		if status.Code(err) == codes.NotFound {
			slog.Info("Pub/Sub topic not found, skipping deletion", "topic", topicID)
			return nil
		}
		slog.Error("failed to delete Pub/Sub topic",
			"topic", topicID,
			"project", projectID,
			"error", err)
		return fmt.Errorf("failed to delete Pub/Sub topic %s: %w", topicID, err)
	}

	slog.Info("Pub/Sub topic deleted successfully", "topic", topicID)
	return nil
}

// copyDir recursively copies a directory tree
func copyDir(src, dst string) error {
	// Get source directory info
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	// Create destination directory
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	// Read source directory contents
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	// Copy each entry
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			// Recursively copy subdirectory
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			// Copy file
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// copyFile copies a single file
func copyFile(src, dst string) error {
	srcData, err := os.ReadFile(src) // #nosec G304 -- src is constructed from trusted config
	if err != nil {
		return err
	}

	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	return os.WriteFile(dst, srcData, srcInfo.Mode())
}
