package client

import (
	"context"
	"fmt"
	"net/url"

	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/util"

	"log/slog"
)

// CreateCluster creates a new cluster and returns the created resource.
func (c *HyperFleetClient) CreateCluster(ctx context.Context, req ResourceCreateRequest) (*Resource, error) {
	slog.Info("creating cluster", "name", req.Name)
	cluster, err := c.CreateResource(ctx, ClustersPath, req)
	if err != nil {
		return nil, fmt.Errorf("create cluster %q: %w", req.Name, err)
	}
	slog.Info("cluster created", "cluster_id", util.FromPtr(cluster.Id), "name", req.Name)
	return cluster, nil
}

// GetCluster retrieves a cluster by ID.
func (c *HyperFleetClient) GetCluster(ctx context.Context, clusterID string) (*Resource, error) {
	return c.GetResource(ctx, ClustersPath+"/"+clusterID)
}

// ListClusters retrieves all clusters.
func (c *HyperFleetClient) ListClusters(ctx context.Context) (*ResourceList, error) {
	return c.ListResources(ctx, ClustersPath, "")
}

// ListClustersWithParams retrieves clusters with query parameters (search, pagination, ordering).
func (c *HyperFleetClient) ListClustersWithParams(ctx context.Context, params url.Values) (*ResourceList, error) {
	return c.ListResourcesWithParams(ctx, ClustersPath, params)
}

// PatchCluster updates a cluster via PATCH.
func (c *HyperFleetClient) PatchCluster(ctx context.Context, clusterID string, req ResourcePatchRequest) (*Resource, error) {
	slog.Info("patching cluster", "cluster_id", clusterID)
	cluster, err := c.PatchResource(ctx, ClustersPath+"/"+clusterID, req)
	if err != nil {
		return nil, fmt.Errorf("patch cluster %s: %w", clusterID, err)
	}
	slog.Info("cluster patched", "cluster_id", clusterID, "generation", cluster.Generation)
	return cluster, nil
}

// PatchClusterFromPayload patches a cluster from a JSON payload file.
func (c *HyperFleetClient) PatchClusterFromPayload(ctx context.Context, clusterID, payloadPath string) (*Resource, error) {
	return c.PatchResourceFromPayload(ctx, ClustersPath+"/"+clusterID, payloadPath)
}

// DeleteCluster soft-deletes a cluster by ID (sets deleted_time, returns 202).
func (c *HyperFleetClient) DeleteCluster(ctx context.Context, clusterID string) (*Resource, error) {
	slog.Info("deleting cluster", "cluster_id", clusterID)
	cluster, err := c.DeleteResource(ctx, ClustersPath+"/"+clusterID)
	if err != nil {
		return nil, fmt.Errorf("delete cluster %s: %w", clusterID, err)
	}
	slog.Info("cluster deleted", "cluster_id", clusterID)
	return cluster, nil
}

// CreateClusterFromPayload creates a cluster from a JSON payload file.
func (c *HyperFleetClient) CreateClusterFromPayload(ctx context.Context, payloadPath string) (*Resource, error) {
	return c.CreateResourceFromPayload(ctx, ClustersPath, payloadPath)
}

// GetClusterStatuses retrieves all adapter statuses for a cluster.
func (c *HyperFleetClient) GetClusterStatuses(ctx context.Context, clusterID string) (*AdapterStatusList, error) {
	return c.GetResourceStatuses(ctx, ClustersPath+"/"+clusterID)
}

// ForceDeleteCluster permanently removes a cluster stuck in Finalizing from the database.
func (c *HyperFleetClient) ForceDeleteCluster(ctx context.Context, clusterID, reason string) error {
	slog.Info("force-deleting cluster", "cluster_id", clusterID, "reason", reason)
	if err := c.ForceDeleteResource(ctx, ClustersPath+"/"+clusterID, reason); err != nil {
		return fmt.Errorf("force-delete cluster %s: %w", clusterID, err)
	}
	slog.Info("cluster force-deleted", "cluster_id", clusterID)
	return nil
}
