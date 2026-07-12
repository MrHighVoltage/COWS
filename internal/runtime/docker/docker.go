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
	"strconv"
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
	client       *http.Client
	streamClient *http.Client
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
	return &Adapter{
		client:       &http.Client{Transport: transport, Timeout: defaultRequestTimeout},
		streamClient: &http.Client{Transport: transport},
	}, nil
}

func (a *Adapter) Name(ctx context.Context) (string, error) {
	info, err := a.hostInfo(ctx)
	if err != nil {
		return "", err
	}
	return runtimeName(info), nil
}

func (a *Adapter) Capabilities(ctx context.Context) (runtime.Capabilities, error) {
	info, err := a.hostInfo(ctx)
	if err != nil {
		return runtime.Capabilities{}, err
	}
	// Docker-compatible runtimes report legacy CFS fields for cgroup v1. On
	// cgroup v2, Podman reports those fields as false even when cpu.max is
	// enforced by the delegated systemd cgroup.
	cpu := (info.CPUCFSQuota && info.CPUCFSPeriod) || info.CgroupVersion == "2"
	memory := info.MemoryLimit
	pids := info.PidsLimit
	return runtime.Capabilities{
		RuntimeName:                   runtimeName(info),
		SupportsResourceLimits:        cpu && memory && pids,
		SupportsCPUResourceLimits:     cpu,
		SupportsMemoryResourceLimits:  memory,
		SupportsPIDLimits:             pids,
		SupportsStorageResourceLimits: false,
		SupportsPrivateNetwork:        true,
		SupportsManagedLabels:         true,
	}, nil
}

func (a *Adapter) HostCapacity(ctx context.Context) (runtime.HostCapacity, error) {
	info, err := a.hostInfo(ctx)
	if err != nil {
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
	capabilities, err := a.Capabilities(ctx)
	if err != nil {
		return runtime.WorkspaceHandle{}, err
	}
	if !capabilities.SupportsCPUResourceLimits || !capabilities.SupportsMemoryResourceLimits || !capabilities.SupportsPIDLimits {
		return runtime.WorkspaceHandle{}, runtime.ErrNotSupported
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

func (a *Adapter) OpenShell(ctx context.Context, runtimeID string, command []string) (runtime.Terminal, error) {
	if !validRuntimeID(runtimeID) || len(command) == 0 || len(command) > 8 {
		return nil, runtime.ErrConflict
	}
	for _, value := range command {
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, runtime.ErrConflict
		}
	}
	var created struct {
		ID string `json:"Id"`
	}
	body := struct {
		AttachStdin  bool     `json:"AttachStdin"`
		AttachStdout bool     `json:"AttachStdout"`
		AttachStderr bool     `json:"AttachStderr"`
		Tty          bool     `json:"Tty"`
		Cmd          []string `json:"Cmd"`
	}{AttachStdin: true, AttachStdout: true, AttachStderr: true, Tty: true, Cmd: command}
	if err := a.mutation(ctx, http.MethodPost, "/containers/"+url.PathEscape(runtimeID)+"/exec", &body, &created); err != nil {
		return nil, err
	}
	if !validRuntimeID(created.ID) {
		return nil, fmt.Errorf("%w: Docker returned invalid exec ID", runtime.ErrInvalidObservation)
	}
	version, err := a.version(ctx)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/v"+version+"/exec/"+url.PathEscape(created.ID)+"/start", strings.NewReader(`{"Detach":false,"Tty":true}`))
	if err != nil {
		return nil, fmt.Errorf("create Docker exec request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "tcp")
	client := a.streamClient
	if client == nil {
		client = a.client
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: Docker exec stream: %v", runtime.ErrUnavailable, err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		base := runtime.ErrUnavailable
		if response.StatusCode == http.StatusNotFound {
			base = runtime.ErrNotFound
		} else if response.StatusCode == http.StatusConflict {
			base = runtime.ErrConflict
		}
		return nil, fmt.Errorf("%w: Docker exec returned %s: %s", base, response.Status, strings.TrimSpace(string(body)))
	}
	stream, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		response.Body.Close()
		return nil, fmt.Errorf("%w: Docker did not return a writable exec stream", runtime.ErrUnavailable)
	}
	return &terminal{stream: stream, adapter: a, execID: created.ID}, nil
}

type terminal struct {
	stream  io.ReadWriteCloser
	adapter *Adapter
	execID  string
}

func (t *terminal) Read(value []byte) (int, error)  { return t.stream.Read(value) }
func (t *terminal) Write(value []byte) (int, error) { return t.stream.Write(value) }
func (t *terminal) Close() error                    { return t.stream.Close() }

func (t *terminal) Resize(ctx context.Context, cols, rows int) error {
	if cols < 1 || cols > 500 || rows < 1 || rows > 500 {
		return runtime.ErrConflict
	}
	path := "/exec/" + url.PathEscape(t.execID) + "/resize?h=" + strconv.Itoa(rows) + "&w=" + strconv.Itoa(cols)
	return t.adapter.mutation(ctx, http.MethodPost, path, nil, nil)
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

type hostInfo struct {
	NCPU            int      `json:"NCPU"`
	MemTotal        int64    `json:"MemTotal"`
	MemoryLimit     bool     `json:"MemoryLimit"`
	PidsLimit       bool     `json:"PidsLimit"`
	CPUCFSQuota     bool     `json:"CpuCfsQuota"`
	CPUCFSPeriod    bool     `json:"CpuCfsPeriod"`
	CgroupVersion   string   `json:"CgroupVersion"`
	Rootless        bool     `json:"Rootless"`
	SecurityOptions []string `json:"SecurityOptions"`
}

func (a *Adapter) hostInfo(ctx context.Context) (hostInfo, error) {
	var info hostInfo
	if err := a.get(ctx, "/info", &info); err != nil {
		return hostInfo{}, err
	}
	if info.NCPU <= 0 || info.MemTotal <= 0 {
		return hostInfo{}, fmt.Errorf("%w: runtime returned invalid host capacity", runtime.ErrInvalidObservation)
	}
	return info, nil
}

func runtimeName(info hostInfo) string {
	if info.Rootless {
		return runtime.RuntimeNamePodman
	}
	for _, option := range info.SecurityOptions {
		if option == "name=rootless" {
			return runtime.RuntimeNamePodman
		}
	}
	return runtime.RuntimeNameDocker
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
