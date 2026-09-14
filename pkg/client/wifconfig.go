package client

import (
	"context"
	"fmt"

	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/util"

	"log/slog"
)

func (c *HyperFleetClient) CreateWifConfig(ctx context.Context, req ResourceCreateRequest) (*Resource, error) {
	slog.Info("creating wifconfig", "name", req.Name)
	wifConfig, err := c.CreateResource(ctx, WifConfigsPath, req)
	if err != nil {
		return nil, fmt.Errorf("create wifconfig %q: %w", req.Name, err)
	}
	slog.Info("wifconfig created", "wifconfig_id", util.FromPtr(wifConfig.Id), "name", req.Name)
	return wifConfig, nil
}

func (c *HyperFleetClient) GetWifConfig(ctx context.Context, wifConfigID string) (*Resource, error) {
	return c.GetResource(ctx, WifConfigsPath+"/"+wifConfigID)
}

func (c *HyperFleetClient) ListWifConfigs(ctx context.Context, search string) (*ResourceList, error) {
	return c.ListResources(ctx, WifConfigsPath, search)
}

func (c *HyperFleetClient) DeleteWifConfig(ctx context.Context, wifConfigID string) (*Resource, error) {
	slog.Info("deleting wifconfig", "wifconfig_id", wifConfigID)
	wifConfig, err := c.DeleteResource(ctx, WifConfigsPath+"/"+wifConfigID)
	if err != nil {
		return nil, fmt.Errorf("delete wifconfig %q: %w", wifConfigID, err)
	}
	slog.Info("wifconfig deleted", "wifconfig_id", wifConfigID)
	return wifConfig, nil
}

func (c *HyperFleetClient) PatchWifConfig(ctx context.Context, wifConfigID string, req ResourcePatchRequest) (*Resource, error) {
	slog.Info("patching wifconfig", "wifconfig_id", wifConfigID)
	wifConfig, err := c.PatchResource(ctx, WifConfigsPath+"/"+wifConfigID, req)
	if err != nil {
		return nil, fmt.Errorf("patch wifconfig %q: %w", wifConfigID, err)
	}
	slog.Info("wifconfig patched", "wifconfig_id", wifConfigID, "generation", wifConfig.Generation)
	return wifConfig, nil
}

func (c *HyperFleetClient) CreateWifConfigFromPayload(ctx context.Context, payloadPath string) (*Resource, error) {
	return c.CreateResourceFromPayload(ctx, WifConfigsPath, payloadPath)
}
