// Package apiaryclient is a thin REST client for Apiary's restshimd API
// (see the apiary repo's internal/restshim), mirroring
// terraform-provider-apiary's own internal/provider/client.go pattern -
// bearer-token auth, JSON in/out, a sentinel ErrNotFound for 404 - so
// this project's two external Apiary clients stay consistent with each
// other rather than each inventing their own conventions.
package apiaryclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// ErrNotFound is returned by Get*/Delete* methods when restshimd reports
// a 404 for the requested id.
var ErrNotFound = errors.New("apiaryclient: resource not found")

// Client is a thin REST client for restshimd's /v1 API. No retry/
// backoff of its own - the controller-runtime reconcile loop that calls
// it is the retry boundary, the same reasoning the Terraform provider's
// client already applies (Terraform's own apply/refresh cycle there).
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient returns a Client targeting baseURL (e.g.
// "http://10.50.0.14:8080"), authenticating with apiKey (may be empty,
// matching managerd's "zero keys means unauthenticated" behavior - see
// apiary's ADR-0023).
func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: httpClient}
}

// apiErrorBody mirrors restshim's own errorBody JSON shape.
type apiErrorBody struct {
	Error      string `json:"error"`
	LeaderHint string `json:"leader_hint,omitempty"`
}

func (e apiErrorBody) asError() error {
	if e.LeaderHint != "" {
		return fmt.Errorf("%s (leader hint: %s)", e.Error, e.LeaderHint)
	}
	return errors.New(e.Error)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("apiaryclient: encoding request body: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("apiaryclient: building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("apiaryclient: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr apiErrorBody
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if apiErr.Error == "" {
			return fmt.Errorf("apiaryclient: %s %s: unexpected status %d", method, path, resp.StatusCode)
		}
		return apiErr.asError()
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("apiaryclient: decoding response: %w", err)
		}
	}
	return nil
}

// VM mirrors internal/restshim's own vm JSON shape (apiary repo,
// internal/restshim/convert.go) - including the fields ADR-0031 added
// (iso_name/network_id/ip_address/mac_address/base_image_name) that
// this provider specifically needs.
type VM struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	VCPUs         uint32 `json:"vcpus,omitempty"`
	MemoryMB      uint64 `json:"memory_mb,omitempty"`
	NodeID        string `json:"node_id,omitempty"`
	DesiredState  string `json:"desired_state,omitempty"`
	ReplicaNodeID string `json:"replica_node_id,omitempty"`
	ISOName       string `json:"iso_name,omitempty"`
	NetworkID     string `json:"network_id,omitempty"`
	IPAddress     string `json:"ip_address,omitempty"`
	MACAddress    string `json:"mac_address,omitempty"`
	BaseImageName string `json:"base_image_name,omitempty"`
}

func (c *Client) CreateVM(ctx context.Context, v VM) (*VM, error) {
	var out VM
	if err := c.do(ctx, http.MethodPost, "/v1/vms", v, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetVM(ctx context.Context, id string) (*VM, error) {
	var out VM
	if err := c.do(ctx, http.MethodGet, "/v1/vms/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteVM is idempotent from the controller's perspective - a VM
// already gone (ErrNotFound) is treated as success, matching how a
// finalizer's teardown step should behave on a retry after a partial
// prior success.
func (c *Client) DeleteVM(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/v1/vms/"+id, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// UploadISOResult mirrors restshim's isoUploadResult response shape -
// just the name the caller then references as a VM's
// iso_name/base_image_name.
type UploadISOResult struct {
	Name string `json:"name"`
}

// UploadISO uploads data (already a complete file - a NoCloud seed ISO
// or a raw base disk image) under name, verifying it against
// expectedSHA256 exactly like restshim/isostore already require (see
// apiary's ADR-0017/ADR-0031). Sent as a multipart form POST against
// restshim's POST /v1/isos route - a thin REST relay in front of
// UploadISO's own client-streaming gRPC on ManagerService. The field
// order matters: expected_sha256 must be written before the file part,
// since restshim processes multipart parts strictly in the order the
// client sent them and needs the hash before the file's bytes start
// arriving.
func (c *Client) UploadISO(ctx context.Context, name, expectedSHA256 string, data io.Reader) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("expected_sha256", expectedSHA256); err != nil {
		return fmt.Errorf("apiaryclient: writing form field: %w", err)
	}
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		return fmt.Errorf("apiaryclient: creating form file: %w", err)
	}
	if _, err := io.Copy(fw, data); err != nil {
		return fmt.Errorf("apiaryclient: streaming upload body: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("apiaryclient: closing multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/isos", &buf)
	if err != nil {
		return fmt.Errorf("apiaryclient: building request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("apiaryclient: POST /v1/isos: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr apiErrorBody
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		if apiErr.Error == "" {
			return fmt.Errorf("apiaryclient: POST /v1/isos: unexpected status %d", resp.StatusCode)
		}
		return apiErr.asError()
	}
	return nil
}
