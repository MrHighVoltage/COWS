package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cows-project/cows/internal/runtime"
)

const (
	defaultRequestTimeout = 10 * time.Second
	maxResponseBytes      = 1 << 20
)

var apiVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

type Adapter struct {
	client *http.Client
}

func New(socketPath string) (*Adapter, error) {
	if strings.TrimSpace(socketPath) == "" {
		return nil, errors.New("Docker socket path must not be empty")
	}
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: defaultRequestTimeout}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &Adapter{client: &http.Client{Transport: transport, Timeout: defaultRequestTimeout}}, nil
}

func (a *Adapter) Name(context.Context) (string, error) {
	return runtime.RuntimeNameDocker, nil
}

func (a *Adapter) Capabilities(ctx context.Context) (runtime.Capabilities, error) {
	if _, err := a.version(ctx); err != nil {
		return runtime.Capabilities{}, err
	}
	return runtime.Capabilities{
		RuntimeName:            runtime.RuntimeNameDocker,
		SupportsResourceLimits: true,
		SupportsPrivateNetwork: true,
		SupportsManagedLabels:  true,
	}, nil
}

func (a *Adapter) HostCapacity(ctx context.Context) (runtime.HostCapacity, error) {
	var info struct {
		NCPU     int   `json:"NCPU"`
		MemTotal int64 `json:"MemTotal"`
	}
	if err := a.get(ctx, "/info", &info); err != nil {
		return runtime.HostCapacity{}, err
	}
	if info.NCPU <= 0 || info.MemTotal <= 0 {
		return runtime.HostCapacity{}, fmt.Errorf("%w: Docker returned invalid host capacity", runtime.ErrInvalidObservation)
	}
	return runtime.HostCapacity{CPUMillis: int64(info.NCPU) * 1000, MemoryBytes: info.MemTotal}, nil
}

func (a *Adapter) ListManaged(ctx context.Context) ([]runtime.ObservedWorkspace, error) {
	filters, err := json.Marshal(map[string][]string{"label": {runtime.ManagedLabel + "=true"}})
	if err != nil {
		return nil, fmt.Errorf("encode Docker filters: %w", err)
	}
	path := "/containers/json?all=true&filters=" + url.QueryEscape(string(filters))
	var containers []containerSummary
	if err := a.get(ctx, path, &containers); err != nil {
		return nil, err
	}
	observed := make([]runtime.ObservedWorkspace, 0, len(containers))
	for _, container := range containers {
		observed = append(observed, runtime.ObservedWorkspace{
			RuntimeID:   container.ID,
			WorkspaceID: container.Labels[runtime.WorkspaceIDLabel],
			State:       stateFromDocker(container.State),
			ObservedAt:  time.Now().UTC(),
		})
	}
	return observed, nil
}

func (a *Adapter) InspectWorkspace(ctx context.Context, runtimeID string) (runtime.ObservedWorkspace, error) {
	if !validRuntimeID(runtimeID) {
		return runtime.ObservedWorkspace{}, runtime.ErrNotFound
	}
	var container containerInspect
	if err := a.get(ctx, "/containers/"+url.PathEscape(runtimeID)+"/json", &container); err != nil {
		return runtime.ObservedWorkspace{}, err
	}
	return runtime.ObservedWorkspace{
		RuntimeID:   container.ID,
		WorkspaceID: container.Config.Labels[runtime.WorkspaceIDLabel],
		State:       stateFromDocker(container.State.Status),
		ExitCode:    &container.State.ExitCode,
		ObservedAt:  time.Now().UTC(),
	}, nil
}

func (a *Adapter) CreateWorkspace(ctx context.Context, spec runtime.WorkspaceSpec) (runtime.WorkspaceHandle, error) {
	if !validWorkspaceID(spec.WorkspaceID) || !validImage(spec.Image) {
		return runtime.WorkspaceHandle{}, runtime.ErrConflict
	}
	imageReference := spec.Image.Reference
	if spec.Image.Digest != "" {
		if !validImageDigest(spec.Image.Digest) {
			return runtime.WorkspaceHandle{}, runtime.ErrConflict
		}
		imageReference += "@" + spec.Image.Digest
	}
	nanoCPUs := spec.Limits.CPUMillis * 1_000_000
	body := struct {
		Image      string            `json:"Image"`
		Labels     map[string]string `json:"Labels"`
		HostConfig struct {
			Memory      int64  `json:"Memory"`
			NanoCPUs    int64  `json:"NanoCpus"`
			NetworkMode string `json:"NetworkMode"`
			PidsLimit   *int64 `json:"PidsLimit,omitempty"`
		} `json:"HostConfig"`
	}{Image: imageReference, Labels: copyLabels(spec.Labels)}
	body.HostConfig.Memory = spec.Limits.MemoryBytes
	body.HostConfig.NanoCPUs = nanoCPUs
	body.HostConfig.NetworkMode = "none"
	pidsLimit := int64(512)
	body.HostConfig.PidsLimit = &pidsLimit
	var response struct {
		ID string `json:"Id"`
	}
	path := "/containers/create?name=" + url.QueryEscape("cows-"+spec.WorkspaceID)
	if err := a.mutation(ctx, http.MethodPost, path, &body, &response); err != nil {
		return runtime.WorkspaceHandle{}, err
	}
	if !validRuntimeID(response.ID) {
		return runtime.WorkspaceHandle{}, fmt.Errorf("%w: Docker returned invalid container ID", runtime.ErrInvalidObservation)
	}
	return runtime.WorkspaceHandle{RuntimeID: response.ID, WorkspaceID: spec.WorkspaceID}, nil
}

func (a *Adapter) StartWorkspace(ctx context.Context, runtimeID string) error {
	if !validRuntimeID(runtimeID) {
		return runtime.ErrNotFound
	}
	return a.mutation(ctx, http.MethodPost, "/containers/"+url.PathEscape(runtimeID)+"/start", nil, nil)
}

func (a *Adapter) StopWorkspace(ctx context.Context, runtimeID string, timeout time.Duration) error {
	if !validRuntimeID(runtimeID) {
		return runtime.ErrNotFound
	}
	seconds := int64(timeout / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	path := "/containers/" + url.PathEscape(runtimeID) + "/stop?t=" + fmt.Sprintf("%d", seconds)
	return a.mutation(ctx, http.MethodPost, path, nil, nil)
}

func (a *Adapter) RemoveWorkspace(ctx context.Context, runtimeID string) error {
	if !validRuntimeID(runtimeID) {
		return runtime.ErrNotFound
	}
	return a.mutation(ctx, http.MethodDelete, "/containers/"+url.PathEscape(runtimeID), nil, nil)
}

type containerSummary struct {
	ID     string            `json:"Id"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

type containerInspect struct {
	ID     string `json:"Id"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status   string `json:"Status"`
		ExitCode int    `json:"ExitCode"`
	} `json:"State"`
}

func (a *Adapter) version(ctx context.Context) (string, error) {
	var response struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := a.request(ctx, "/version", &response); err != nil {
		return "", err
	}
	if !apiVersionPattern.MatchString(response.APIVersion) {
		return "", fmt.Errorf("%w: invalid Docker API version %q", runtime.ErrInvalidObservation, response.APIVersion)
	}
	return response.APIVersion, nil
}

func (a *Adapter) get(ctx context.Context, path string, output any) error {
	version, err := a.version(ctx)
	if err != nil {
		return err
	}
	return a.request(ctx, "/v"+version+path, output)
}

func (a *Adapter) request(ctx context.Context, path string, output any) error {
	return a.requestMethod(ctx, http.MethodGet, path, nil, output)
}

func (a *Adapter) mutation(ctx context.Context, method, path string, input, output any) error {
	version, err := a.version(ctx)
	if err != nil {
		return err
	}
	return a.requestMethod(ctx, method, "/v"+version+path, input, output)
}

func (a *Adapter) requestMethod(ctx context.Context, method, path string, input, output any) error {
	var request *http.Request
	var err error
	if input != nil {
		encoded, encodeErr := json.Marshal(input)
		if encodeErr != nil {
			return fmt.Errorf("encode Docker request: %w", encodeErr)
		}
		request, err = http.NewRequestWithContext(ctx, method, "http://docker"+path, bytes.NewReader(encoded))
	} else {
		request, err = http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
	}
	if err != nil {
		return fmt.Errorf("create Docker request: %w", err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := a.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: Docker request: %v", runtime.ErrUnavailable, err)
	}
	defer response.Body.Close()
	if (response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices) && response.StatusCode != http.StatusNotModified {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		base := runtime.ErrUnavailable
		if response.StatusCode == http.StatusNotFound {
			base = runtime.ErrNotFound
		} else if response.StatusCode == http.StatusConflict {
			base = runtime.ErrConflict
		}
		return fmt.Errorf("%w: Docker API returned %s: %s", base, response.Status, strings.TrimSpace(string(body)))
	}
	if output == nil || response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotModified {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode Docker response: %w", err)
	}
	return nil
}

func validImage(image runtime.Image) bool {
	return image.Reference != "" && len(image.Reference) <= 255 && !strings.ContainsAny(image.Reference, " \t\r\n")
}

func validImageDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') {
			continue
		}
		return false
	}
	return true
}

func validWorkspaceID(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func copyLabels(labels map[string]string) map[string]string {
	copy := make(map[string]string, len(labels))
	for key, value := range labels {
		copy[key] = value
	}
	return copy
}

func stateFromDocker(value string) runtime.State {
	switch strings.ToLower(value) {
	case "created":
		return runtime.StateCreated
	case "running", "restarting", "paused":
		return runtime.StateRunning
	case "exited", "dead":
		return runtime.StateExited
	default:
		return runtime.StateUnknown
	}
}

func validRuntimeID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F') || (char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

var _ runtime.Runtime = (*Adapter)(nil)
var _ runtime.CapacityProvider = (*Adapter)(nil)
