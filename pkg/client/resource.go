package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"time"

	"log/slog"
)

const apiPrefix = "/api/hyperfleet/v1/"

// Resource represents a generic HyperFleet API resource.
type Resource struct {
	Id              *string           `json:"id,omitempty"`
	Kind            string            `json:"kind"`
	Name            string            `json:"name"`
	Href            *string           `json:"href,omitempty"`
	Spec            map[string]any    `json:"spec,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Generation      int32             `json:"generation"`
	Status          ResourceStatus    `json:"status"`
	OwnerReferences *ObjectReference  `json:"owner_references,omitempty"`
	CreatedTime     *time.Time        `json:"created_time,omitempty"`
	UpdatedTime     *time.Time        `json:"updated_time,omitempty"`
	DeletedTime     *time.Time        `json:"deleted_time,omitempty"`
	CreatedBy       *string           `json:"created_by,omitempty"`
	UpdatedBy       *string           `json:"updated_by,omitempty"`
	DeletedBy       *string           `json:"deleted_by,omitempty"`
	ResourceVersion *string           `json:"resource_version,omitempty"`
}

// ResourceList is a paginated list of resources.
type ResourceList struct {
	Items []Resource `json:"items"`
	Total int32      `json:"total"`
	Size  int32      `json:"size"`
	Page  int32      `json:"page"`
}

// ResourceCreateRequest is the payload for creating a resource.
type ResourceCreateRequest struct {
	Kind   string            `json:"kind"`
	Name   string            `json:"name"`
	Spec   map[string]any    `json:"spec"`
	Labels map[string]string `json:"labels,omitempty"`
}

// ResourcePatchRequest is the payload for patching a resource.
type ResourcePatchRequest struct {
	Spec   map[string]any    `json:"spec,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// ResourceStatus holds the status conditions for a resource.
type ResourceStatus struct {
	Conditions []ResourceCondition `json:"conditions"`
}

// ResourceCondition represents a single status condition on a resource.
type ResourceCondition struct {
	Type               string                  `json:"type"`
	Status             ResourceConditionStatus `json:"status"`
	ObservedGeneration int32                   `json:"observed_generation"`
	Reason             *string                 `json:"reason,omitempty"`
	Message            *string                 `json:"message,omitempty"`
	CreatedTime        time.Time               `json:"created_time"`
	LastTransitionTime time.Time               `json:"last_transition_time"`
	LastUpdatedTime    time.Time               `json:"last_updated_time"`
}

// ResourceConditionStatus is the status value for resource conditions.
type ResourceConditionStatus string

const (
	ResourceConditionStatusTrue  ResourceConditionStatus = "True"
	ResourceConditionStatusFalse ResourceConditionStatus = "False"
)

// AdapterCondition represents a single status condition reported by an adapter.
type AdapterCondition struct {
	Type               string                 `json:"type"`
	Status             AdapterConditionStatus `json:"status"`
	Reason             *string                `json:"reason,omitempty"`
	Message            *string                `json:"message,omitempty"`
	LastTransitionTime time.Time              `json:"last_transition_time"`
}

// AdapterConditionStatus is the status value for adapter conditions.
type AdapterConditionStatus string

const (
	AdapterConditionStatusTrue  AdapterConditionStatus = "True"
	AdapterConditionStatusFalse AdapterConditionStatus = "False"
)

// AdapterStatus represents the complete status report from an adapter.
type AdapterStatus struct {
	Adapter            string             `json:"adapter"`
	Conditions         []AdapterCondition `json:"conditions"`
	ObservedGeneration int32              `json:"observed_generation"`
	CreatedTime        time.Time          `json:"created_time"`
	LastReportTime     time.Time          `json:"last_report_time"`
	Data               map[string]any     `json:"data,omitempty"`
	Metadata           *AdapterMetadata   `json:"metadata,omitempty"`
}

// AdapterMetadata holds job execution metadata for an adapter status.
type AdapterMetadata struct {
	Attempt       *int32     `json:"attempt,omitempty"`
	CompletedTime *time.Time `json:"completed_time,omitempty"`
	Duration      *string    `json:"duration,omitempty"`
	JobName       *string    `json:"job_name,omitempty"`
	JobNamespace  *string    `json:"job_namespace,omitempty"`
	StartedTime   *time.Time `json:"started_time,omitempty"`
}

// AdapterStatusList is a paginated list of adapter statuses.
type AdapterStatusList struct {
	Items []AdapterStatus `json:"items"`
	Page  int32           `json:"page"`
	Size  int32           `json:"size"`
	Total int32           `json:"total"`
}

// ForceDeleteRequest is the payload for force-deleting a resource.
type ForceDeleteRequest struct {
	Reason string `json:"reason"`
}

// ObjectReference identifies a related resource.
type ObjectReference struct {
	Href *string `json:"href,omitempty"`
	Id   *string `json:"id,omitempty"`
	Kind string  `json:"kind"`
}

// --- Generic CRUD methods ---

func (c *HyperFleetClient) CreateResource(ctx context.Context, path string, body any) (*Resource, error) {
	resp, err := c.doJSON(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, fmt.Errorf("create resource at %s: %w", path, err)
	}
	return handleHTTPResponse[Resource](resp, http.StatusCreated, "create resource at "+path)
}

func (c *HyperFleetClient) GetResource(ctx context.Context, path string) (*Resource, error) {
	resp, err := c.doJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("get resource at %s: %w", path, err)
	}
	return handleHTTPResponse[Resource](resp, http.StatusOK, "get resource at "+path)
}

func (c *HyperFleetClient) ListResources(ctx context.Context, path string, search string) (*ResourceList, error) {
	if search != "" {
		path += "?search=" + url.QueryEscape(search)
	}
	resp, err := c.doJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("list resources at %s: %w", path, err)
	}
	return handleHTTPResponse[ResourceList](resp, http.StatusOK, "list resources at "+path)
}

func (c *HyperFleetClient) ListResourcesWithParams(ctx context.Context, path string, params url.Values) (*ResourceList, error) {
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	resp, err := c.doJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("list resources at %s: %w", path, err)
	}
	return handleHTTPResponse[ResourceList](resp, http.StatusOK, "list resources at "+path)
}

func (c *HyperFleetClient) DeleteResource(ctx context.Context, path string) (*Resource, error) {
	resp, err := c.doJSON(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return nil, fmt.Errorf("delete resource at %s: %w", path, err)
	}
	return handleHTTPResponse[Resource](resp, http.StatusAccepted, "delete resource at "+path)
}

func (c *HyperFleetClient) PatchResource(ctx context.Context, path string, body any) (*Resource, error) {
	resp, err := c.doJSON(ctx, http.MethodPatch, path, body)
	if err != nil {
		return nil, fmt.Errorf("patch resource at %s: %w", path, err)
	}
	return handleHTTPResponse[Resource](resp, http.StatusOK, "patch resource at "+path)
}

func (c *HyperFleetClient) CreateResourceFromPayload(ctx context.Context, path string, payloadPath string) (*Resource, error) {
	slog.Debug("loading resource payload", "path", path, "payload_path", payloadPath)

	payload, err := loadPayloadFromFile[map[string]any](payloadPath)
	if err != nil {
		return nil, fmt.Errorf("load resource payload %s: %w", payloadPath, err)
	}

	return c.CreateResource(ctx, path, payload)
}

func (c *HyperFleetClient) CreateResourceFromPayloadWith(ctx context.Context, path string, payloadPath string, overrides map[string]any) (*Resource, error) {
	slog.Debug("loading resource payload", "path", path, "payload_path", payloadPath)

	payload, err := loadPayloadFromFile[map[string]any](payloadPath)
	if err != nil {
		return nil, fmt.Errorf("load resource payload %s: %w", payloadPath, err)
	}
	maps.Copy((*payload), overrides)

	return c.CreateResource(ctx, path, payload)
}

func (c *HyperFleetClient) PatchResourceFromPayload(ctx context.Context, path string, payloadPath string) (*Resource, error) {
	slog.Debug("loading resource patch payload", "path", path, "payload_path", payloadPath)

	payload, err := loadPayloadFromFile[map[string]any](payloadPath)
	if err != nil {
		return nil, fmt.Errorf("load resource patch payload %s: %w", payloadPath, err)
	}

	return c.PatchResource(ctx, path, payload)
}

func (c *HyperFleetClient) GetResourceStatuses(ctx context.Context, path string) (*AdapterStatusList, error) {
	resp, err := c.doJSON(ctx, http.MethodGet, path+"/statuses", nil)
	if err != nil {
		return nil, fmt.Errorf("get resource statuses at %s: %w", path, err)
	}
	return handleHTTPResponse[AdapterStatusList](resp, http.StatusOK, "get resource statuses at "+path)
}

func (c *HyperFleetClient) ForceDeleteResource(ctx context.Context, path string, reason string) error {
	resp, err := c.doJSON(ctx, http.MethodPost, path+"/force-delete", ForceDeleteRequest{Reason: reason})
	if err != nil {
		return fmt.Errorf("force-delete resource at %s: %w", path, err)
	}
	return handleHTTPNoBodyResponse(resp, http.StatusNoContent, "force-delete resource at "+path)
}

func (c *HyperFleetClient) GetResourceHTTPStatus(ctx context.Context, path string) (int, error) {
	resp, err := c.doJSON(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, fmt.Errorf("get resource HTTP status at %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return resp.StatusCode, fmt.Errorf("drain response body at %s: %w", path, err)
	}
	return resp.StatusCode, nil
}

func (c *HyperFleetClient) doJSON(ctx context.Context, method, path string, body any) (*http.Response, error) {
	fullURL := c.baseURL + apiPrefix + path

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, fullURL, err)
	}
	return resp, nil
}
