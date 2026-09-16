package helper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"

	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/client"
	k8sclient "github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/client/kubernetes"
	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/client/maestro"
	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/config"
	"log/slog"
)

// Helper provides utility functions for e2e tests
type Helper struct {
	Cfg                   *config.Config
	Client                *client.HyperFleetClient
	K8sClient             *k8sclient.Client
	MaestroClient         *maestro.Client
	AdapterDeploymentList *AdapterDeploymentList
}

// TestDataPath resolves a relative path within the testdata directory
// This ensures testdata paths work correctly whether invoked via go test or the e2e binary
func (h *Helper) TestDataPath(relativePath string) string {
	return filepath.Join(h.Cfg.TestDataDir, relativePath)
}

// GetTestCluster creates a new temporary test cluster
func (h *Helper) GetTestCluster(ctx context.Context, payloadPath string) (string, error) {
	cluster, err := h.Client.CreateClusterFromPayload(ctx, payloadPath)
	if err != nil {
		return "", err
	}
	if cluster == nil {
		return "", fmt.Errorf("CreateClusterFromPayload returned nil")
	}
	if cluster.Id == nil {
		return "", fmt.Errorf("created cluster has no ID")
	}
	return *cluster.Id, nil
}

// CleanupTestCluster deletes the test cluster via the HyperFleet API and waits for hard-delete (404).
// The API DELETE owns the full cleanup lifecycle: adapter finalization, Maestro teardown, namespace deletion.
func (h *Helper) CleanupTestCluster(ctx context.Context, clusterID string) error {
	slog.Info("deleting cluster via API", "cluster_id", clusterID)

	if _, err := h.Client.DeleteCluster(ctx, clusterID); err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			slog.Info("cluster already deleted", "cluster_id", clusterID)
			return nil
		}
		return fmt.Errorf("delete cluster %s: %w", clusterID, err)
	}

	pollFn := h.PollClusterHTTPStatus(ctx, clusterID)
	deadline := time.Now().Add(h.Cfg.Timeouts.Cluster.Deleted)
	for time.Now().Before(deadline) {
		status, err := pollFn()
		if err != nil {
			return fmt.Errorf("polling hard-delete for cluster %s: %w", clusterID, err)
		}
		if status == http.StatusNotFound {
			slog.Info("cluster hard-deleted", "cluster_id", clusterID)
			return nil
		}
		if status >= 400 {
			return fmt.Errorf("unexpected HTTP %d while waiting for cluster %s hard-delete", status, clusterID)
		}
		time.Sleep(h.Cfg.Polling.Interval)
	}

	return fmt.Errorf("cluster %s not hard-deleted within %s", clusterID, h.Cfg.Timeouts.Cluster.Deleted)
}

// DeferClusterCleanup registers a DeferCleanup that will delete the cluster after the test,
// regardless of pass/fail. If the cluster was already deleted, cleanup logs a warning.
func (h *Helper) DeferClusterCleanup(clusterID string) {
	ginkgo.DeferCleanup(func(ctx context.Context) {
		if err := h.CleanupTestCluster(ctx, clusterID); err != nil {
			ginkgo.GinkgoWriter.Printf("Warning: failed to cleanup cluster %s: %v\n", clusterID, err)
		}
	})
}

// CleanupTestNodePool deletes the test nodepool via the HyperFleet API and waits for hard-delete (404).
// The API DELETE owns the full cleanup lifecycle.
func (h *Helper) CleanupTestNodePool(ctx context.Context, clusterID, nodepoolID string) error {
	slog.Info("deleting nodepool via API", "cluster_id", clusterID, "nodepool_id", nodepoolID)

	if _, err := h.Client.DeleteNodePool(ctx, clusterID, nodepoolID); err != nil {
		return fmt.Errorf("delete nodepool %s: %w", nodepoolID, err)
	}

	pollFn := h.PollNodePoolHTTPStatus(ctx, clusterID, nodepoolID)
	deadline := time.Now().Add(h.Cfg.Timeouts.NodePool.Deleted)
	for time.Now().Before(deadline) {
		status, err := pollFn()
		if err != nil {
			return fmt.Errorf("polling hard-delete for nodepool %s: %w", nodepoolID, err)
		}
		if status == http.StatusNotFound {
			slog.Info("nodepool hard-deleted", "cluster_id", clusterID, "nodepool_id", nodepoolID)
			return nil
		}
		if status >= 400 {
			return fmt.Errorf("unexpected HTTP %d while waiting for nodepool %s hard-delete", status, nodepoolID)
		}
		time.Sleep(h.Cfg.Polling.Interval)
	}

	return fmt.Errorf("nodepool %s not hard-deleted within %s", nodepoolID, h.Cfg.Timeouts.NodePool.Deleted)
}

// CleanupTestChannel deletes all versions under a channel, then deletes the channel.
// Channels/versions are non-reconcilable resources with no hard-delete — cleanup is just soft-delete, no 404 polling.
func (h *Helper) CleanupTestChannel(ctx context.Context, channelID string) error {
	slog.Info("cleaning up channel", "channel_id", channelID)

	versions, err := h.Client.ListVersions(ctx, channelID, "")
	if err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			slog.Info("channel already gone", "channel_id", channelID)
			return nil
		}
		return fmt.Errorf("list versions for channel %s: %w", channelID, err)
	}

	var deleteErr error
	for _, v := range versions.Items {
		if v.DeletedTime != nil || v.Id == nil {
			continue
		}
		if _, err := h.Client.DeleteVersion(ctx, channelID, *v.Id); err != nil {
			var httpErr *client.HTTPError
			if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
				continue
			}
			slog.Error("failed to delete version during cleanup", "channel_id", channelID, "version_id", *v.Id, "error", err)
			if deleteErr == nil {
				deleteErr = fmt.Errorf("delete version %s: %w", *v.Id, err)
			}
		}
	}

	if _, err := h.Client.DeleteChannel(ctx, channelID); err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
			slog.Info("channel already deleted", "channel_id", channelID)
			return deleteErr
		}
		return fmt.Errorf("delete channel %s: %w", channelID, err)
	}

	slog.Info("channel cleaned up", "channel_id", channelID)
	return deleteErr
}

// CleanupTestWifConfig deletes a WIF config resource.
// WIF configs are non-reconcilable resources with no hard-delete — cleanup is just soft-delete, no 404 polling.
func (h *Helper) CleanupTestWifConfig(ctx context.Context, wifConfigID string) error {
	slog.Info("cleaning up wifconfig", "wifconfig_id", wifConfigID)

	if _, err := h.Client.DeleteWifConfig(ctx, wifConfigID); err != nil {
		var httpErr *client.HTTPError
		if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusNotFound || httpErr.StatusCode == http.StatusConflict) {
			slog.Info("wifconfig cleanup skipped", "wifconfig_id", wifConfigID, "status", httpErr.StatusCode)
			return nil
		}
		return fmt.Errorf("delete wifconfig %s: %w", wifConfigID, err)
	}

	slog.Info("wifconfig cleaned up", "wifconfig_id", wifConfigID)
	return nil
}

// DeferChannelCleanup registers a DeferCleanup that will delete the channel after the test,
// regardless of pass/fail. If cleanup fails, it logs a warning.
func (h *Helper) DeferChannelCleanup(channelID string) {
	ginkgo.DeferCleanup(func(ctx context.Context) {
		if err := h.CleanupTestChannel(ctx, channelID); err != nil {
			ginkgo.GinkgoWriter.Printf("Warning: failed to cleanup channel %s: %v\n", channelID, err)
		}
	})
}

// DeferWifConfigCleanup registers a DeferCleanup that will delete the wifconfig after the test,
// regardless of pass/fail. If cleanup fails, it logs a warning.
func (h *Helper) DeferWifConfigCleanup(wifConfigID string) {
	ginkgo.DeferCleanup(func(ctx context.Context) {
		if err := h.CleanupTestWifConfig(ctx, wifConfigID); err != nil {
			ginkgo.GinkgoWriter.Printf("Warning: failed to cleanup wifconfig %s: %v\n", wifConfigID, err)
		}
	})
}

// ExpectedIdentity returns the configured expected audit identity value.
// Returns empty string if not configured, signalling callers to skip audit assertions.
func (h *Helper) ExpectedIdentity() string {
	return h.Cfg.Identity.ExpectedIdentity
}

// GetMaestroClient returns the Maestro client, initializing it lazily on first access
// This avoids the overhead of K8s service discovery for test suites that don't use Maestro
func (h *Helper) GetMaestroClient() *maestro.Client {
	if h.MaestroClient == nil {
		h.MaestroClient = maestro.NewClient("")
	}
	return h.MaestroClient
}
