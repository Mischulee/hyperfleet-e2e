package client

import (
	"context"
	"fmt"

	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/util"

	"log/slog"
)

// CreateNodePool creates a new nodepool for the specified cluster.
func (c *HyperFleetClient) CreateNodePool(ctx context.Context, clusterID string, req ResourceCreateRequest) (*Resource, error) {
	slog.Info("creating nodepool", "cluster_id", clusterID, "name", req.Name)
	np, err := c.CreateResource(ctx, ClustersPath+"/"+clusterID+"/"+NodepoolsPath, req)
	if err != nil {
		return nil, fmt.Errorf("create nodepool %q in cluster %s: %w", req.Name, clusterID, err)
	}
	slog.Info("nodepool created", "cluster_id", clusterID, "nodepool_id", util.FromPtr(np.Id), "name", req.Name)
	return np, nil
}

// GetNodePool retrieves a nodepool by ID.
func (c *HyperFleetClient) GetNodePool(ctx context.Context, clusterID, nodepoolID string) (*Resource, error) {
	return c.GetResource(ctx, ClustersPath+"/"+clusterID+"/"+NodepoolsPath+"/"+nodepoolID)
}

// ListNodePools retrieves all nodepools for a cluster.
func (c *HyperFleetClient) ListNodePools(ctx context.Context, clusterID string) (*ResourceList, error) {
	return c.ListResources(ctx, ClustersPath+"/"+clusterID+"/"+NodepoolsPath, "")
}

// PatchNodePool updates a nodepool via PATCH.
func (c *HyperFleetClient) PatchNodePool(ctx context.Context, clusterID, nodepoolID string, req ResourcePatchRequest) (*Resource, error) {
	slog.Info("patching nodepool", "cluster_id", clusterID, "nodepool_id", nodepoolID)
	np, err := c.PatchResource(ctx, ClustersPath+"/"+clusterID+"/"+NodepoolsPath+"/"+nodepoolID, req)
	if err != nil {
		return nil, fmt.Errorf("patch nodepool %s in cluster %s: %w", nodepoolID, clusterID, err)
	}
	slog.Info("nodepool patched", "cluster_id", clusterID, "nodepool_id", nodepoolID, "generation", np.Generation)
	return np, nil
}

// PatchNodePoolFromPayload patches a nodepool from a JSON payload file.
func (c *HyperFleetClient) PatchNodePoolFromPayload(ctx context.Context, clusterID, nodepoolID, payloadPath string) (*Resource, error) {
	return c.PatchResourceFromPayload(ctx, ClustersPath+"/"+clusterID+"/"+NodepoolsPath+"/"+nodepoolID, payloadPath)
}

// DeleteNodePool soft-deletes a nodepool by ID (sets deleted_time, returns 202).
func (c *HyperFleetClient) DeleteNodePool(ctx context.Context, clusterID, nodepoolID string) (*Resource, error) {
	slog.Info("deleting nodepool", "cluster_id", clusterID, "nodepool_id", nodepoolID)
	np, err := c.DeleteResource(ctx, ClustersPath+"/"+clusterID+"/"+NodepoolsPath+"/"+nodepoolID)
	if err != nil {
		return nil, fmt.Errorf("delete nodepool %s in cluster %s: %w", nodepoolID, clusterID, err)
	}
	slog.Info("nodepool deleted", "cluster_id", clusterID, "nodepool_id", nodepoolID)
	return np, nil
}

// CreateNodePoolFromPayload creates a nodepool from a JSON payload file.
func (c *HyperFleetClient) CreateNodePoolFromPayload(ctx context.Context, clusterID, payloadPath string) (*Resource, error) {
	return c.CreateResourceFromPayload(ctx, ClustersPath+"/"+clusterID+"/"+NodepoolsPath, payloadPath)
}

// GetNodePoolStatuses retrieves all adapter statuses for a nodepool.
func (c *HyperFleetClient) GetNodePoolStatuses(ctx context.Context, clusterID, nodepoolID string) (*AdapterStatusList, error) {
	return c.GetResourceStatuses(ctx, ClustersPath+"/"+clusterID+"/"+NodepoolsPath+"/"+nodepoolID)
}

// ForceDeleteNodePool permanently removes a nodepool stuck in Finalizing from the database.
func (c *HyperFleetClient) ForceDeleteNodePool(ctx context.Context, clusterID, nodepoolID, reason string) error {
	slog.Info("force-deleting nodepool", "cluster_id", clusterID, "nodepool_id", nodepoolID, "reason", reason)
	if err := c.ForceDeleteResource(ctx, ClustersPath+"/"+clusterID+"/"+NodepoolsPath+"/"+nodepoolID, reason); err != nil {
		return fmt.Errorf("force-delete nodepool %s in cluster %s: %w", nodepoolID, clusterID, err)
	}
	slog.Info("nodepool force-deleted", "cluster_id", clusterID, "nodepool_id", nodepoolID)
	return nil
}
