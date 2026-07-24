package podman

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
	client           *http.Client
	streamClient     *http.Client
	helperExecutable string
	podmanBinary     string
}

type imagePullMessage struct {
	Error       string `json:"error"`
	ErrorDetail struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
}

func New(socketPath string) (*Adapter, error) {
	if strings.TrimSpace(socketPath) == "" {
		return nil, errors.New("Podman socket path must not be empty")
	}
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: defaultRequestTimeout}).DialContext(ctx, "unix", socketPath)
		},
	}
	helperExecutable, _ := os.Executable()
	return &Adapter{
		client:           &http.Client{Transport: transport, Timeout: defaultRequestTimeout},
		streamClient:     &http.Client{Transport: transport},
		helperExecutable: helperExecutable,
		podmanBinary:     "podman",
	}, nil
}

func (a *Adapter) Name(ctx context.Context) (string, error) {
	info, err := a.hostInfo(ctx)
	if err != nil {
		return "", err
	}
	if !info.Rootless {
		return "", fmt.Errorf("%w: COWS requires rootless Podman", runtime.ErrNotSupported)
	}
	return runtimeName(info), nil
}

func (a *Adapter) Capabilities(ctx context.Context) (runtime.Capabilities, error) {
	info, err := a.hostInfo(ctx)
	if err != nil {
		return runtime.Capabilities{}, err
	}
	if !info.Rootless {
		return runtime.Capabilities{}, fmt.Errorf("%w: COWS requires rootless Podman", runtime.ErrNotSupported)
	}
	// Podman's compatibility API reports legacy CFS fields for cgroup v1. On
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
		SupportsPasswdEntry:           runtimeName(info) == runtime.RuntimeNamePodman,
	}, nil
}

func (a *Adapter) HostCapacity(ctx context.Context) (runtime.HostCapacity, error) {
	info, err := a.hostInfo(ctx)
	if err != nil {
		return runtime.HostCapacity{}, err
	}
	if info.NCPU <= 0 || info.MemTotal <= 0 {
		return runtime.HostCapacity{}, fmt.Errorf("%w: Podman returned invalid host capacity", runtime.ErrInvalidObservation)
	}
	if !info.Rootless {
		return runtime.HostCapacity{}, fmt.Errorf("%w: COWS requires rootless Podman", runtime.ErrNotSupported)
	}
	return runtime.HostCapacity{CPUMillis: int64(info.NCPU) * 1000, MemoryBytes: info.MemTotal}, nil
}

func (a *Adapter) ImageAvailable(ctx context.Context, image runtime.Image) (bool, error) {
	imageReference, err := imageReference(image)
	if err != nil {
		return false, runtime.ErrConflict
	}
	var details struct{}
	err = a.get(ctx, "/images/"+url.PathEscape(imageReference)+"/json", &details)
	if errors.Is(err, runtime.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *Adapter) PullImage(ctx context.Context, image runtime.Image) error {
	imageReference, err := imageReference(image)
	if err != nil {
		return runtime.ErrConflict
	}
	version, err := a.version(ctx)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://podman/v"+version+"/images/create?fromImage="+url.QueryEscape(imageReference), nil)
	if err != nil {
		return fmt.Errorf("create Podman image pull request: %w", err)
	}
	client := a.streamClient
	if client == nil {
		client = a.client
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: Podman image pull: %v", runtime.ErrUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		return fmt.Errorf("%w: Podman image pull returned %s: %s", runtime.ErrUnavailable, response.Status, strings.TrimSpace(string(body)))
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var message imagePullMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return fmt.Errorf("decode Podman image pull response: %w", err)
		}
		if message.Error != "" || message.ErrorDetail.Message != "" {
			failure := message.Error
			if failure == "" {
				failure = message.ErrorDetail.Message
			}
			return fmt.Errorf("%w: %s", runtime.ErrConflict, failure)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Podman image pull response: %w", err)
	}
	return nil
}

func (a *Adapter) ListManaged(ctx context.Context) ([]runtime.ObservedWorkspace, error) {
	filters, err := json.Marshal(map[string][]string{"label": {runtime.ManagedLabel + "=true"}})
	if err != nil {
		return nil, fmt.Errorf("encode Podman filters: %w", err)
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
			State:       stateFromPodman(container.State),
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
		State:       stateFromPodman(container.State.Status),
		ExitCode:    &container.State.ExitCode,
		ObservedAt:  time.Now().UTC(),
	}, nil
}

func (a *Adapter) WorkspaceResourceUsage(ctx context.Context, runtimeID string) (runtime.ResourceUsage, error) {
	if !validRuntimeID(runtimeID) {
		return runtime.ResourceUsage{}, runtime.ErrNotFound
	}
	var stats containerStats
	if err := a.get(ctx, "/containers/"+url.PathEscape(runtimeID)+"/stats?stream=false", &stats); err != nil {
		return runtime.ResourceUsage{}, err
	}
	if stats.Memory.Usage > uint64(^uint64(0)>>1) || stats.PIDs.Current > uint64(^uint64(0)>>1) {
		return runtime.ResourceUsage{}, fmt.Errorf("%w: Podman returned oversized resource usage", runtime.ErrInvalidObservation)
	}
	usage := runtime.ResourceUsage{MemoryBytes: int64(stats.Memory.Usage), PIDs: int64(stats.PIDs.Current), ObservedAt: time.Now().UTC()}
	if stats.CPU.Usage.Total < stats.PreCPU.Usage.Total || stats.CPU.System < stats.PreCPU.System {
		return runtime.ResourceUsage{}, fmt.Errorf("%w: Podman returned decreasing CPU counters", runtime.ErrInvalidObservation)
	}
	systemDelta := stats.CPU.System - stats.PreCPU.System
	if systemDelta > 0 {
		onlineCPUs := stats.CPU.OnlineCPUs
		if onlineCPUs == 0 {
			onlineCPUs = 1
		}
		percentMilli := float64(stats.CPU.Usage.Total-stats.PreCPU.Usage.Total) / float64(systemDelta) * float64(onlineCPUs) * 100000
		if percentMilli < 0 || percentMilli > float64(^uint64(0)>>1) {
			return runtime.ResourceUsage{}, fmt.Errorf("%w: Podman returned invalid CPU usage", runtime.ErrInvalidObservation)
		}
		usage.CPUPercentMilli = int64(percentMilli)
	}
	return usage, nil
}

func (a *Adapter) CreateWorkspace(ctx context.Context, spec runtime.WorkspaceSpec) (runtime.WorkspaceHandle, error) {
	if !validWorkspaceID(spec.WorkspaceID) || !validImage(spec.Image) {
		return runtime.WorkspaceHandle{}, runtime.ErrConflict
	}
	if !validRuntimeConfiguration(spec) {
		return runtime.WorkspaceHandle{}, runtime.ErrConflict
	}
	capabilities, err := a.Capabilities(ctx)
	if err != nil {
		return runtime.WorkspaceHandle{}, err
	}
	if !capabilities.SupportsCPUResourceLimits || !capabilities.SupportsMemoryResourceLimits || !capabilities.SupportsPIDLimits {
		return runtime.WorkspaceHandle{}, runtime.ErrNotSupported
	}
	imageReference, err := imageReference(spec.Image)
	if err != nil {
		return runtime.WorkspaceHandle{}, err
	}
	networkMode := spec.NetworkMode
	if networkMode == "" {
		networkMode = "none"
	}
	if spec.User != nil && spec.User.PasswdEntry != "" && !capabilities.SupportsPasswdEntry {
		return runtime.WorkspaceHandle{}, fmt.Errorf("%w: passwd entries require the Podman Libpod API", runtime.ErrNotSupported)
	}
	if spec.User != nil && spec.User.PasswdEntry != "" {
		return a.createPodmanWorkspace(ctx, spec, imageReference, networkMode)
	}
	nanoCPUs := spec.Limits.CPUMillis * 1_000_000
	environment := make([]string, 0, len(spec.Environment))
	for _, value := range spec.Environment {
		environment = append(environment, value.Name+"="+value.Value)
	}
	mounts := make([]struct {
		Type     string `json:"Type"`
		Source   string `json:"Source"`
		Target   string `json:"Target"`
		ReadOnly bool   `json:"ReadOnly"`
	}, 0, len(spec.Mounts))
	for _, value := range spec.Mounts {
		source := value.Source
		if source == "" {
			source = "cows-" + spec.WorkspaceID + "-" + value.Name
		}
		mountType := value.Type
		if mountType == "" {
			mountType = "volume"
		}
		mounts = append(mounts, struct {
			Type     string `json:"Type"`
			Source   string `json:"Source"`
			Target   string `json:"Target"`
			ReadOnly bool   `json:"ReadOnly"`
		}{Type: mountType, Source: source, Target: value.ContainerPath, ReadOnly: value.ReadOnly})
	}
	portBindings := make(map[string][]struct {
		HostIP   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}, len(spec.Ports))
	exposedPorts := make(map[string]struct{}, len(spec.Ports))
	for _, value := range spec.Ports {
		key := strconv.Itoa(value.ContainerPort) + "/" + value.Protocol
		exposedPorts[key] = struct{}{}
		portBindings[key] = []struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		}{{HostIP: value.HostIP, HostPort: strconv.Itoa(value.HostPort)}}
	}
	body := struct {
		Image        string              `json:"Image"`
		Labels       map[string]string   `json:"Labels"`
		Cmd          []string            `json:"Cmd,omitempty"`
		Env          []string            `json:"Env,omitempty"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
		HostConfig   struct {
			Memory       int64  `json:"Memory"`
			NanoCPUs     int64  `json:"NanoCpus"`
			NetworkMode  string `json:"NetworkMode"`
			PidsLimit    *int64 `json:"PidsLimit,omitempty"`
			Mounts       any    `json:"Mounts,omitempty"`
			PortBindings any    `json:"PortBindings,omitempty"`
		} `json:"HostConfig"`
		User string `json:"User,omitempty"`
	}{Image: imageReference, Labels: copyLabels(spec.Labels), Cmd: append([]string(nil), spec.Command...), Env: environment, ExposedPorts: exposedPorts}
	if spec.User != nil {
		body.User = strconv.FormatInt(spec.User.UID, 10) + ":" + strconv.FormatInt(spec.User.GID, 10)
	}
	body.HostConfig.Memory = spec.Limits.MemoryBytes
	body.HostConfig.NanoCPUs = nanoCPUs
	body.HostConfig.NetworkMode = networkMode
	pidsLimit := int64(512)
	body.HostConfig.PidsLimit = &pidsLimit
	body.HostConfig.Mounts = mounts
	body.HostConfig.PortBindings = portBindings
	var response struct {
		ID string `json:"Id"`
	}
	path := "/containers/create?name=" + url.QueryEscape("cows-"+spec.WorkspaceID)
	if err := a.mutation(ctx, http.MethodPost, path, &body, &response); err != nil {
		return runtime.WorkspaceHandle{}, err
	}
	if !validRuntimeID(response.ID) {
		return runtime.WorkspaceHandle{}, fmt.Errorf("%w: Podman returned invalid container ID", runtime.ErrInvalidObservation)
	}
	return runtime.WorkspaceHandle{RuntimeID: response.ID, WorkspaceID: spec.WorkspaceID}, nil
}

func (a *Adapter) createPodmanWorkspace(ctx context.Context, spec runtime.WorkspaceSpec, imageReference, networkMode string) (runtime.WorkspaceHandle, error) {
	environment := make(map[string]string, len(spec.Environment))
	for _, value := range spec.Environment {
		environment[value.Name] = value.Value
	}
	type podmanMount struct {
		Type        string   `json:"type"`
		Source      string   `json:"source"`
		Destination string   `json:"destination"`
		Options     []string `json:"options,omitempty"`
	}
	type portMapping struct {
		ContainerPort int    `json:"container_port"`
		HostPort      int    `json:"host_port"`
		HostIP        string `json:"host_ip"`
		Protocol      string `json:"protocol"`
	}
	type userNamespace struct {
		UIDMappings []idMapping `json:"uidmapping,omitempty"`
		GIDMappings []idMapping `json:"gidmapping,omitempty"`
	}
	body := struct {
		Name           string            `json:"name,omitempty"`
		Image          string            `json:"image"`
		Command        []string          `json:"command,omitempty"`
		Env            map[string]string `json:"env,omitempty"`
		Labels         map[string]string `json:"labels,omitempty"`
		User           string            `json:"user,omitempty"`
		PasswdEntry    string            `json:"passwd_entry,omitempty"`
		UserNS         *userNamespace    `json:"userns,omitempty"`
		NetNS          map[string]string `json:"netns,omitempty"`
		Mounts         []podmanMount     `json:"mounts,omitempty"`
		PortMappings   []portMapping     `json:"portmappings,omitempty"`
		ResourceLimits struct {
			CPU struct {
				Quota  int64  `json:"quota,omitempty"`
				Period uint64 `json:"period,omitempty"`
			} `json:"cpu,omitempty"`
			Memory struct {
				Limit int64 `json:"limit,omitempty"`
			} `json:"memory,omitempty"`
			PIDs struct {
				Limit int64 `json:"limit,omitempty"`
			} `json:"pids,omitempty"`
		} `json:"resource_limits,omitempty"`
	}{Image: imageReference, Command: append([]string(nil), spec.Command...), Env: environment, Labels: copyLabels(spec.Labels), PasswdEntry: spec.User.PasswdEntry}
	body.Name = "cows-" + spec.WorkspaceID
	body.User = strconv.FormatInt(spec.User.UID, 10) + ":" + strconv.FormatInt(spec.User.GID, 10)
	body.NetNS = map[string]string{"nsmode": networkMode}
	info, err := a.hostInfo(ctx)
	if err != nil {
		return runtime.WorkspaceHandle{}, err
	}
	if info.Rootless {
		mappings, err := a.podmanIDMappings(ctx)
		if err != nil {
			return runtime.WorkspaceHandle{}, err
		}
		uidMappings, err := explicitRootlessMapping(mappings.UIDMap, spec.User.UID)
		if err != nil {
			return runtime.WorkspaceHandle{}, err
		}
		gidMappings, err := explicitRootlessMapping(mappings.GIDMap, spec.User.GID)
		if err != nil {
			return runtime.WorkspaceHandle{}, err
		}
		body.UserNS = &userNamespace{UIDMappings: uidMappings, GIDMappings: gidMappings}
	}
	body.ResourceLimits.CPU.Quota = spec.Limits.CPUMillis * 100
	body.ResourceLimits.CPU.Period = 100000
	body.ResourceLimits.Memory.Limit = spec.Limits.MemoryBytes
	body.ResourceLimits.PIDs.Limit = 512
	for _, value := range spec.Mounts {
		source := value.Source
		if source == "" {
			source = "cows-" + spec.WorkspaceID + "-" + value.Name
		}
		mountType := value.Type
		if mountType == "" {
			mountType = "volume"
		}
		body.Mounts = append(body.Mounts, podmanMount{Type: mountType, Source: source, Destination: value.ContainerPath, Options: readOnlyOptions(value.ReadOnly, value.RemapOwnership)})
	}
	for _, value := range spec.Ports {
		body.PortMappings = append(body.PortMappings, portMapping{ContainerPort: value.ContainerPort, HostPort: value.HostPort, HostIP: value.HostIP, Protocol: value.Protocol})
	}
	var response struct {
		ID string `json:"Id"`
	}
	path := "/libpod/containers/create?name=" + url.QueryEscape(body.Name)
	if err := a.mutation(ctx, http.MethodPost, path, &body, &response); err != nil {
		return runtime.WorkspaceHandle{}, err
	}
	if !validRuntimeID(response.ID) {
		return runtime.WorkspaceHandle{}, fmt.Errorf("%w: Podman returned invalid container ID", runtime.ErrInvalidObservation)
	}
	return runtime.WorkspaceHandle{RuntimeID: response.ID, WorkspaceID: spec.WorkspaceID}, nil
}

func readOnlyOptions(readOnly, remapOwnership bool) []string {
	options := make([]string, 0, 2)
	if readOnly {
		options = append(options, "ro")
	}
	if remapOwnership {
		options = append(options, "U")
	}
	return options
}

func explicitRootlessMapping(mappings []idMapping, containerID int64) ([]idMapping, error) {
	if containerID < 0 {
		return nil, fmt.Errorf("%w: container identity must not be negative", runtime.ErrNotSupported)
	}
	var total int64
	for _, mapping := range mappings {
		if mapping.Size <= 0 || mapping.ContainerID < 0 || mapping.HostID < 0 {
			return nil, fmt.Errorf("%w: invalid rootless ID mapping", runtime.ErrNotSupported)
		}
		total += mapping.Size
	}
	// Podman's rootless mapping reserves intermediate ID 0 for the invoking
	// host account. Mapping the remaining range explicitly keeps that account
	// outside the container while still making every normal container ID usable.
	if total <= 1 || containerID >= total-1 {
		return nil, fmt.Errorf("%w: container identity %d is outside the available subordinate ID range", runtime.ErrNotSupported, containerID)
	}
	return []idMapping{{ContainerID: 0, HostID: 1, Size: total - 1}}, nil
}

func validRuntimeConfiguration(spec runtime.WorkspaceSpec) bool {
	seenEnvironment := make(map[string]struct{}, len(spec.Environment))
	for _, value := range spec.Environment {
		if !validEnvironmentName(value.Name) || strings.ContainsAny(value.Value, "\x00\r\n") {
			return false
		}
		if _, ok := seenEnvironment[value.Name]; ok {
			return false
		}
		seenEnvironment[value.Name] = struct{}{}
	}
	seenMounts := make(map[string]struct{}, len(spec.Mounts))
	for _, value := range spec.Mounts {
		mountType := value.Type
		if mountType == "" {
			mountType = "volume"
		}
		if !validWorkspaceID(value.Name) || (mountType != "volume" && mountType != "bind") || value.ContainerPath == "" || value.ContainerPath[0] != '/' || strings.Contains(value.ContainerPath, "..") || strings.ContainsAny(value.ContainerPath, "\x00\r\n") || (mountType == "bind" && value.Source == "") || strings.ContainsAny(value.Source, "\x00\r\n") {
			return false
		}
		if mountType == "bind" && !filepath.IsAbs(value.Source) {
			return false
		}
		if mountType == "volume" && (filepath.IsAbs(value.Source) || strings.ContainsAny(value.Source, " \t")) {
			return false
		}
		if _, ok := seenMounts[value.Name]; ok {
			return false
		}
		seenMounts[value.Name] = struct{}{}
	}
	for _, value := range spec.Ports {
		if value.Protocol != "tcp" && value.Protocol != "udp" || value.ContainerPort < 1 || value.ContainerPort > 65535 || value.HostPort < 1024 || value.HostPort > 65535 || value.HostIP != "127.0.0.1" {
			return false
		}
	}
	if spec.User != nil {
		if spec.User.Username == "" || spec.User.Name == "" || spec.User.Home == "" || spec.User.Shell == "" || spec.User.UID < 0 || spec.User.UID > 2147483647 || spec.User.GID < 0 || spec.User.GID > 2147483647 || strings.ContainsAny(spec.User.PasswdEntry, "\x00\r\n") {
			return false
		}
		if !validRuntimeUserField(spec.User.Username) || !validRuntimeUserField(spec.User.Name) || !validRuntimeUserField(spec.User.Home) || !validRuntimeUserField(spec.User.Shell) {
			return false
		}
	}
	return true
}

func validRuntimeUserField(value string) bool {
	return value != "" && len(value) <= 256 && !strings.Contains(value, ":")
}

func validEnvironmentName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, char := range value {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9' && index > 0) || (char == '_' && index > 0) {
			continue
		}
		return false
	}
	return true
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

func (a *Adapter) CreateWorkspaceNetwork(ctx context.Context, name string) error {
	if !validWorkspaceNetworkName(name) {
		return runtime.ErrConflict
	}
	if a.podmanBinary == "" {
		return fmt.Errorf("%w: Podman executable is unavailable", runtime.ErrNotSupported)
	}
	if err := exec.CommandContext(ctx, a.podmanBinary, "network", "inspect", name).Run(); err == nil {
		return nil
	}
	command := exec.CommandContext(ctx, a.podmanBinary, "network", "create", "--internal", "--disable-dns", name)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: create private Podman network: %s", runtime.ErrUnavailable, strings.TrimSpace(string(output)))
	}
	return nil
}

func (a *Adapter) RemoveWorkspaceNetwork(ctx context.Context, name string) error {
	if !validWorkspaceNetworkName(name) {
		return runtime.ErrConflict
	}
	if a.podmanBinary == "" {
		return fmt.Errorf("%w: Podman executable is unavailable", runtime.ErrNotSupported)
	}
	command := exec.CommandContext(ctx, a.podmanBinary, "network", "rm", name)
	output, err := command.CombinedOutput()
	if err != nil && strings.TrimSpace(string(output)) != "" {
		return fmt.Errorf("%w: remove private Podman network: %s", runtime.ErrUnavailable, strings.TrimSpace(string(output)))
	}
	return nil
}

func (a *Adapter) VolumeExists(ctx context.Context, name string) (bool, error) {
	if !validWorkspaceID(name) {
		return false, runtime.ErrConflict
	}
	_, err := a.volumePath(ctx, name)
	if errors.Is(err, runtime.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *Adapter) RemoveVolume(ctx context.Context, name string) error {
	if !validWorkspaceID(name) {
		return runtime.ErrConflict
	}
	return a.mutation(ctx, http.MethodDelete, "/libpod/volumes/"+url.PathEscape(name), nil, nil)
}

func validWorkspaceNetworkName(value string) bool {
	if len(value) < len("cows-net-")+1 || len(value) > 63 || !strings.HasPrefix(value, "cows-net-") {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func (a *Adapter) OpenShell(ctx context.Context, runtimeID string, command []string) (runtime.Terminal, error) {
	return a.openShell(ctx, runtimeID, "", command)
}

func (a *Adapter) OpenShellAs(ctx context.Context, runtimeID string, uid int64, command []string) (runtime.Terminal, error) {
	if uid < 0 || uid > 2147483647 {
		return nil, runtime.ErrConflict
	}
	return a.openShell(ctx, runtimeID, strconv.FormatInt(uid, 10), command)
}

func (a *Adapter) openShell(ctx context.Context, runtimeID, user string, command []string) (runtime.Terminal, error) {
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
		User         string   `json:"User,omitempty"`
	}{AttachStdin: true, AttachStdout: true, AttachStderr: true, Tty: true, Cmd: command, User: user}
	if err := a.mutation(ctx, http.MethodPost, "/containers/"+url.PathEscape(runtimeID)+"/exec", &body, &created); err != nil {
		return nil, err
	}
	if !validRuntimeID(created.ID) {
		return nil, fmt.Errorf("%w: Podman returned invalid exec ID", runtime.ErrInvalidObservation)
	}
	version, err := a.version(ctx)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://podman/v"+version+"/exec/"+url.PathEscape(created.ID)+"/start", strings.NewReader(`{"Detach":false,"Tty":true}`))
	if err != nil {
		return nil, fmt.Errorf("create Podman exec request: %w", err)
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
		return nil, fmt.Errorf("%w: Podman exec stream: %v", runtime.ErrUnavailable, err)
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
		return nil, fmt.Errorf("%w: Podman exec returned %s: %s", base, response.Status, strings.TrimSpace(string(body)))
	}
	stream, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		response.Body.Close()
		return nil, fmt.Errorf("%w: Podman did not return a writable exec stream", runtime.ErrUnavailable)
	}
	return &terminal{stream: stream, adapter: a, execID: created.ID}, nil
}

func (a *Adapter) OpenInternalService(ctx context.Context, runtimeID string, containerPort, hostPort int) (io.ReadWriteCloser, error) {
	if !validRuntimeID(runtimeID) || containerPort < 1 || containerPort > 65535 || hostPort < 1024 || hostPort > 65535 {
		return nil, runtime.ErrConflict
	}
	var container containerInspect
	if err := a.get(ctx, "/containers/"+url.PathEscape(runtimeID)+"/json", &container); err != nil {
		return nil, err
	}
	if stateFromPodman(container.State.Status) != runtime.StateRunning {
		return nil, runtime.ErrConflict
	}
	bindings, ok := container.NetworkSettings.Ports[strconv.Itoa(containerPort)+"/tcp"]
	if !ok {
		return nil, runtime.ErrConflict
	}
	expectedHostPort := strconv.Itoa(hostPort)
	for _, binding := range bindings {
		if binding.HostPort == expectedHostPort && binding.HostIP == "127.0.0.1" {
			connection, err := (&net.Dialer{Timeout: defaultRequestTimeout}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", expectedHostPort))
			if err != nil {
				return nil, fmt.Errorf("%w: connect internal service: %v", runtime.ErrUnavailable, err)
			}
			return connection, nil
		}
	}
	return nil, runtime.ErrConflict
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
	SizeRw int64  `json:"SizeRw"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status   string `json:"Status"`
		ExitCode int    `json:"ExitCode"`
	} `json:"State"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

type containerStats struct {
	CPU struct {
		Usage struct {
			Total uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System     uint64 `json:"system_cpu_usage"`
		OnlineCPUs uint64 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPU struct {
		Usage struct {
			Total uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	Memory struct {
		Usage uint64 `json:"usage"`
	} `json:"memory_stats"`
	PIDs struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

func (a *Adapter) WorkspaceStorageUsage(ctx context.Context, spec runtime.StorageUsageSpec) (int64, error) {
	var total int64
	if spec.RuntimeID != "" && !validRuntimeID(spec.RuntimeID) {
		return 0, runtime.ErrConflict
	}
	if spec.RuntimeID != "" {
		var container containerInspect
		if err := a.get(ctx, "/containers/"+url.PathEscape(spec.RuntimeID)+"/json?size=true", &container); err != nil {
			return 0, err
		}
		if container.SizeRw < 0 {
			return 0, fmt.Errorf("%w: Podman returned a negative writable-layer size", runtime.ErrInvalidObservation)
		}
		total = container.SizeRw
	}
	for _, mount := range spec.Mounts {
		access, err := a.OpenFileAccess(ctx, mount)
		if err != nil {
			return 0, err
		}
		usage, usageErr := access.Usage(ctx)
		closeErr := access.Close()
		if usageErr != nil {
			return 0, usageErr
		}
		if closeErr != nil {
			return 0, closeErr
		}
		if usage > (1<<62)-total {
			return 0, fmt.Errorf("%w: workspace storage usage is too large", runtime.ErrInvalidObservation)
		}
		total += usage
	}
	return total, nil
}

type idMapping struct {
	ContainerID int64 `json:"container_id"`
	HostID      int64 `json:"host_id"`
	Size        int64 `json:"size"`
}

type idMappings struct {
	UIDMap []idMapping `json:"uidmap"`
	GIDMap []idMapping `json:"gidmap"`
}

type podmanInfo struct {
	Host struct {
		IDMappings idMappings `json:"idMappings"`
	} `json:"host"`
}

func (a *Adapter) podmanIDMappings(ctx context.Context) (idMappings, error) {
	var info podmanInfo
	if err := a.get(ctx, "/libpod/info", &info); err != nil {
		return idMappings{}, err
	}
	if len(info.Host.IDMappings.UIDMap) == 0 || len(info.Host.IDMappings.GIDMap) == 0 {
		return idMappings{}, fmt.Errorf("%w: Podman did not report rootless UID/GID mappings", runtime.ErrNotSupported)
	}
	return info.Host.IDMappings, nil
}

func (a *Adapter) version(ctx context.Context) (string, error) {
	var response struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := a.request(ctx, "/version", &response); err != nil {
		return "", err
	}
	if !apiVersionPattern.MatchString(response.APIVersion) {
		return "", fmt.Errorf("%w: invalid Podman API version %q", runtime.ErrInvalidObservation, response.APIVersion)
	}
	return response.APIVersion, nil
}

type hostInfo struct {
	NCPU            int        `json:"NCPU"`
	MemTotal        int64      `json:"MemTotal"`
	MemoryLimit     bool       `json:"MemoryLimit"`
	PidsLimit       bool       `json:"PidsLimit"`
	CPUCFSQuota     bool       `json:"CpuCfsQuota"`
	CPUCFSPeriod    bool       `json:"CpuCfsPeriod"`
	CgroupVersion   string     `json:"CgroupVersion"`
	Rootless        bool       `json:"Rootless"`
	SecurityOptions []string   `json:"SecurityOptions"`
	IDMappings      idMappings `json:"IDMappings"`
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
	return runtime.RuntimeNamePodman
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
			return fmt.Errorf("encode Podman request: %w", encodeErr)
		}
		request, err = http.NewRequestWithContext(ctx, method, "http://podman"+path, bytes.NewReader(encoded))
	} else {
		request, err = http.NewRequestWithContext(ctx, method, "http://podman"+path, nil)
	}
	if err != nil {
		return fmt.Errorf("create Podman request: %w", err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := a.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: Podman request: %v", runtime.ErrUnavailable, err)
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
		return fmt.Errorf("%w: Podman API returned %s: %s", base, response.Status, strings.TrimSpace(string(body)))
	}
	if output == nil || response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotModified {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode Podman response: %w", err)
	}
	return nil
}

func validImage(image runtime.Image) bool {
	return image.Reference != "" && len(image.Reference) <= 255 && !strings.ContainsAny(image.Reference, " \t\r\n")
}

func imageReference(image runtime.Image) (string, error) {
	if !validImage(image) {
		return "", runtime.ErrConflict
	}
	if image.Digest != "" && !validImageDigest(image.Digest) {
		return "", runtime.ErrConflict
	}
	if image.Digest == "" {
		return image.Reference, nil
	}
	return image.Reference + "@" + image.Digest, nil
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

func stateFromPodman(value string) runtime.State {
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
var _ runtime.InternalServiceRuntime = (*Adapter)(nil)
