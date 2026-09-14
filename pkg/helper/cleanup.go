package helper

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/client/kubernetes"
	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/client/maestro"
	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/config"
	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/helper/helm"
	"log/slog"
)

type CleanupHelper struct {
	cfg                      *config.Config
	k8sClient                *kubernetes.Client
	labelSelectorListOptions metav1.ListOptions
	adapterDeploymentList    *AdapterDeploymentList
	maestroClient            *maestro.Client
}

// NewCleanupHelper creates a new CleanupHelper
// It creates a new helper instance and uses it to cleanup resources
func NewCleanupHelper() (*CleanupHelper, error) {
	cfg := GetSuiteConfig()
	if cfg == nil {
		return nil, fmt.Errorf("config must be set for resource cleanup tracking")
	}
	k8sClient, err := kubernetes.NewClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	adapterDeploymentList := GetAdapterDeploymentList()
	if adapterDeploymentList == nil {
		return nil, fmt.Errorf("adapter deployment list must be set for resource cleanup tracking")
	}

	// RUN_ID is set when tests are executed, forms the label: e2e.hyperfleet.io/run-id=<run_id>
	// Label applied to adapters and resources created during test suite execution
	labelSelector := fmt.Sprintf("e2e.hyperfleet.io/run-id=%s", cfg.RunID)
	labelSelectorListOptions := metav1.ListOptions{
		LabelSelector: labelSelector,
	}

	// Create Maestro Client for cleanup workflow
	maestroClient := maestro.NewClient("")

	return &CleanupHelper{k8sClient: k8sClient, labelSelectorListOptions: labelSelectorListOptions, cfg: cfg, adapterDeploymentList: adapterDeploymentList, maestroClient: maestroClient}, nil
}

// CleanupPubSubResources sweeps Pub/Sub topics and subscriptions created by adapters
// deployed during this process's test execution. Safe to call from every Ginkgo process
// since each process tracks its own AdapterDeploymentList in memory.
func CleanupPubSubResources() {
	c, err := NewCleanupHelper()
	if err != nil {
		slog.Error("failed to create cleanup helper for Pub/Sub sweep", "error", err)
		return
	}

	if c.cfg.BrokerType != "googlepubsub" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := c.SweepPubsubTestAdapterResources(ctx); err != nil {
		slog.Error("failed to cleanup Pub/Sub test resources", "error", err)
	}
}

// CleanupKubeResources sweeps Helm releases and K8s resources labeled with the suite
// run-id. In parallel mode this MUST run exactly once, after all processes finish
// (via SynchronizedAfterSuite's final closure), to avoid sweeping resources belonging
// to specs still running on other processes.
func CleanupKubeResources() {
	c, err := NewCleanupHelper()
	if err != nil {
		slog.Error("failed to create cleanup helper for K8s sweep", "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Step 1: Remove all helm releases installed with the given label selector
	helmClient := helm.NewClient(c.cfg.Namespace)
	releases, err := helmClient.ListReleasesBySelector(c.labelSelectorListOptions.LabelSelector)
	if err != nil {
		// Failed to list releases, so skipping uninstall
		slog.Error("failed to list helm releases", "error", err)
		// Still proceeding with cleanup
	} else {
		slog.Info("found helm releases", "count", len(releases))
		for _, release := range releases {
			err := helmClient.UninstallRelease(ctx, release, c.cfg.Namespace)
			if err != nil {
				slog.Error("failed to uninstall helm release", "name", release, "error", err)
				continue
			}
			slog.Info("uninstalled helm chart", "name", release)
		}
	}

	// Step 2: Sweep resources that contain the given label selector
	if err := c.SweepLabeledResources(ctx); err != nil {
		slog.Error("failed to cleanup test resources", "error", err)
	}

	slog.Info("test resources cleaned up")
}

func (c *CleanupHelper) SweepPubsubTestAdapterResources(ctx context.Context) error {
	// Get a snapshot of the adapter deployment list to avoid race conditions
	deployments := c.adapterDeploymentList.Snapshot()
	var errorList []string
	for _, deployment := range deployments {
		resourceType := deployment.ResourceType
		adapterName := deployment.AdapterName
		namespace := c.cfg.Namespace
		projectID := c.cfg.GCPProjectID
		topicID := fmt.Sprintf("%s-%s-%s-dlq", namespace, resourceType, adapterName)
		subscriptionID := fmt.Sprintf("%s-%s-%s", namespace, resourceType, adapterName)
		if err := DeletePubSubSubscription(ctx, subscriptionID, projectID); err != nil {
			errorList = append(errorList, subscriptionID)
		}
		if err := DeletePubSubTopic(ctx, topicID, projectID); err != nil {
			errorList = append(errorList, topicID)
		}
	}
	if len(errorList) > 0 {
		return fmt.Errorf("failed to delete some Pub/Sub resources: %s", strings.Join(errorList, ", "))
	}
	slog.Info("deleted Pub/Sub resources for adapters", "count", len(deployments))
	return nil
}

// SweepLabeledResources iterates through labeled resources and deletes any orphaned resources
func (c *CleanupHelper) SweepLabeledResources(ctx context.Context) error {
	slog.Info("Starting robust test cleanup with label selector:", "labelSelector", c.labelSelectorListOptions.LabelSelector)

	// Phase 1: Delete ResourceBundles Created by Run ID
	slog.Info("Phase 1: Best effort to delete Resource Bundles created by run ID")
	if rbs, err := c.maestroClient.FindResourceBundlesByRunID(ctx, c.cfg.RunID); err == nil {
		for _, rb := range rbs {
			// Best effort deletion of resource bundles
			err := c.maestroClient.DeleteResourceBundle(ctx, rb.ID)
			if err != nil {
				slog.Error("failed to delete resource bundle", "error", err)
			}
		}
	} else {
		slog.Error("failed to get resource bundles by run Id", "error", err)
	}

	// Phase 2: Delete resources (Background propagation - don't wait for cascade)
	slog.Info("Phase 2: Best effort to delete resources")
	if err := c.deleteJobs(ctx); err != nil {
		slog.Error("failed to delete jobs", "error", err)
	}
	if err := c.deleteDeployments(ctx); err != nil {
		slog.Error("failed to delete deployments", "error", err)
	}
	if err := c.deleteConfigMaps(ctx); err != nil {
		slog.Error("failed to delete configmaps", "error", err)
	}
	if err := c.deleteNamespacesForce(ctx); err != nil {
		slog.Error("failed to delete namespaces", "error", err)
	}

	// Phase 3: Wait for deletion to complete (poll until resources are gone or timeout)
	slog.Info("Phase 3: Waiting for deletion to complete (polling for up to 3 minutes)")
	err := wait.PollUntilContextTimeout(ctx, 1*time.Minute, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		remaining, err := c.countRemainingResources(ctx)
		if err != nil {
			slog.Error("failed to count remaining resources", "error", err)
			return false, nil
		}
		if remaining == 0 {
			slog.Info("all resources deleted successfully")
			return true, nil
		}
		slog.Info("resources still being deleted", "remaining", remaining)
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("not all resources were deleted in time: %w", err)
	}

	slog.Info("cleanup completed successfully with all resources deleted")
	return nil
}

// countRemainingResources returns the count of resources still present
// If any error occurs, the count that was obtained is returned and an error is returned
func (c *CleanupHelper) countRemainingResources(ctx context.Context) (int, error) {
	count := 0
	errorList := []string{}

	// Count Namespaces
	namespaces, err := c.k8sClient.CoreV1().Namespaces().List(ctx, c.labelSelectorListOptions)
	if err != nil {
		slog.Error("failed to list Namespaces", "error", err)
		errorList = append(errorList, "Namespaces")
	} else {
		count += len(namespaces.Items)
	}

	// Count Jobs
	jobs, err := c.k8sClient.BatchV1().Jobs("").List(ctx, c.labelSelectorListOptions)
	if err != nil {
		slog.Error("failed to list Jobs", "error", err)
		errorList = append(errorList, "Jobs")
	} else {
		count += len(jobs.Items)
	}

	// Count Deployments
	deployments, err := c.k8sClient.AppsV1().Deployments("").List(ctx, c.labelSelectorListOptions)
	if err != nil {
		slog.Error("failed to list Deployments", "error", err)
		errorList = append(errorList, "Deployments")
	} else {
		count += len(deployments.Items)
	}

	// Count ConfigMaps
	configMaps, err := c.k8sClient.CoreV1().ConfigMaps("").List(ctx, c.labelSelectorListOptions)
	if err != nil {
		slog.Error("failed to list ConfigMaps", "error", err)
		errorList = append(errorList, "ConfigMaps")
	} else {
		count += len(configMaps.Items)
	}

	if len(errorList) > 0 {
		// Return the count that was obtained and an error message of the resources that failed to list
		return count, fmt.Errorf("failed to list some resources: %s", strings.Join(errorList, ", "))
	}

	return count, nil
}

// deleteJobs deletes all jobs matching the label selector
func (c *CleanupHelper) deleteJobs(ctx context.Context) error {
	jobs, err := c.k8sClient.BatchV1().Jobs("").List(ctx, c.labelSelectorListOptions)
	if err != nil {
		return fmt.Errorf("failed to list Jobs: %w", err)
	}
	for _, job := range jobs.Items {
		slog.Warn("deleting Job", "name", job.Name, "namespace", job.Namespace)
		if err := c.k8sClient.BatchV1().Jobs(job.Namespace).Delete(ctx, job.Name, metav1.DeleteOptions{}); err != nil {
			slog.Error("failed to delete Job", "name", job.Name, "namespace", job.Namespace, "error", err)
		}
	}
	return nil
}

func (c *CleanupHelper) deleteDeployments(ctx context.Context) error {
	deployments, err := c.k8sClient.AppsV1().Deployments("").List(ctx, c.labelSelectorListOptions)
	if err != nil {
		return fmt.Errorf("failed to list Deployments: %w", err)
	}
	for _, deployment := range deployments.Items {
		slog.Warn("deleting Deployment", "name", deployment.Name, "namespace", deployment.Namespace)
		if err := c.k8sClient.AppsV1().Deployments(deployment.Namespace).Delete(ctx, deployment.Name, metav1.DeleteOptions{}); err != nil {
			slog.Error("failed to delete Deployment", "name", deployment.Name, "namespace", deployment.Namespace, "error", err)
		}
	}
	return nil
}

func (c *CleanupHelper) deleteConfigMaps(ctx context.Context) error {
	configMaps, err := c.k8sClient.CoreV1().ConfigMaps("").List(ctx, c.labelSelectorListOptions)
	if err != nil {
		return fmt.Errorf("failed to list ConfigMaps: %w", err)
	}
	for _, configMap := range configMaps.Items {
		slog.Warn("deleting ConfigMap", "name", configMap.Name, "namespace", configMap.Namespace)
		if err := c.k8sClient.CoreV1().ConfigMaps(configMap.Namespace).Delete(ctx, configMap.Name, metav1.DeleteOptions{}); err != nil {
			slog.Error("failed to delete ConfigMap", "name", configMap.Name, "namespace", configMap.Namespace, "error", err)
		}
	}
	return nil
}

func (c *CleanupHelper) deleteNamespacesForce(ctx context.Context) error {
	namespaces, err := c.k8sClient.CoreV1().Namespaces().List(ctx, c.labelSelectorListOptions)
	if err != nil {
		return fmt.Errorf("failed to list Namespaces: %w", err)
	}

	propagationPolicy := metav1.DeletePropagationBackground
	zeroInt64 := int64(0)
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy:  &propagationPolicy,
		GracePeriodSeconds: &zeroInt64,
	}
	for _, namespace := range namespaces.Items {
		slog.Warn("deleting Namespace", "name", namespace.Name)
		if err := c.k8sClient.CoreV1().Namespaces().Delete(ctx, namespace.Name, deleteOptions); err != nil {
			slog.Error("failed to delete Namespace", "name", namespace.Name, "error", err)
		}
	}
	return nil
}
