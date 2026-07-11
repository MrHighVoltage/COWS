package docker

import (
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

func (a *Adapter) CreateWorkspace(context.Context, runtime.WorkspaceSpec) (runtime.WorkspaceHandle, error) {
	return runtime.WorkspaceHandle{}, runtime.ErrNotSupported
}

func (a *Adapter) StartWorkspace(context.Context, string) error { return runtime.ErrNotSupported }

func (a *Adapter) StopWorkspace(context.Context, string, time.Duration) error {
	return runtime.ErrNotSupported
}

func (a *Adapter) RemoveWorkspace(context.Context, string) error { return runtime.ErrNotSupported }

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
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return fmt.Errorf("create Docker request: %w", err)
	}
	response, err := a.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: Docker request: %v", runtime.ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		base := runtime.ErrUnavailable
		if response.StatusCode == http.StatusNotFound {
			base = runtime.ErrNotFound
		} else if response.StatusCode == http.StatusConflict {
			base = runtime.ErrConflict
		}
		return fmt.Errorf("%w: Docker API returned %s: %s", base, response.Status, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode Docker response: %w", err)
	}
	return nil
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
