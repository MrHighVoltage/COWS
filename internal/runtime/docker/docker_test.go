package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/runtime"
)

func TestNewRejectsEmptySocket(t *testing.T) {
	if _, err := New(" "); err == nil {
		t.Fatal("expected empty socket error")
	}
}

func TestStateMapping(t *testing.T) {
	tests := map[string]runtime.State{
		"created":    runtime.StateCreated,
		"running":    runtime.StateRunning,
		"restarting": runtime.StateRunning,
		"paused":     runtime.StateRunning,
		"exited":     runtime.StateExited,
		"dead":       runtime.StateExited,
		"unexpected": runtime.StateUnknown,
	}
	for input, expected := range tests {
		if actual := stateFromDocker(input); actual != expected {
			t.Errorf("stateFromDocker(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestAdapterWithoutDockerReturnsUnavailable(t *testing.T) {
	adapter, err := New(t.TempDir() + "/missing.sock")
	if err != nil {
		t.Fatalf("create adapter: %v", err)
	}
	_, err = adapter.Capabilities(context.Background())
	if !errors.Is(err, runtime.ErrUnavailable) {
		t.Fatalf("missing Docker socket error = %v", err)
	}
	if err := adapter.StopWorkspace(context.Background(), "abc", time.Second); !errors.Is(err, runtime.ErrUnavailable) {
		t.Fatalf("stop Docker error = %v", err)
	}
}

func TestListManagedUsesVersionedDockerAPI(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case "/version":
			body = `{"ApiVersion":"1.45"}`
		case "/v1.45/containers/json":
			body = `[{"Id":"abcdef0123456789","State":"running","Labels":{"cows.managed":"true","cows.workspace-id":"workspace-1"}}]`
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	workspaces, err := adapter.ListManaged(context.Background())
	if err != nil {
		t.Fatalf("list managed workspaces: %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].WorkspaceID != "workspace-1" || workspaces[0].State != runtime.StateRunning {
		t.Fatalf("unexpected managed workspaces: %+v", workspaces)
	}
}

func TestLifecycleUsesApprovedDockerRequests(t *testing.T) {
	var calls []string
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.Method+" "+request.URL.Path+"?"+request.URL.RawQuery)
		var body string
		switch request.URL.Path {
		case "/version":
			body = `{"ApiVersion":"1.45"}`
		case "/v1.45/info":
			body = `{"NCPU":8,"MemTotal":8589934592,"MemoryLimit":true,"PidsLimit":true,"CpuCfsQuota":true,"CpuCfsPeriod":true}`
		case "/v1.45/containers/create":
			body = `{"Id":"abcdef0123456789"}`
		case "/v1.45/containers/abcdef0123456789/start", "/v1.45/containers/abcdef0123456789/stop", "/v1.45/containers/abcdef0123456789":
			return &http.Response{StatusCode: http.StatusNoContent, Status: "204 No Content", Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusCreated, Status: "201 Created", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	handle, err := adapter.CreateWorkspace(context.Background(), runtime.WorkspaceSpec{WorkspaceID: "workspace-123", Image: runtime.Image{Reference: "registry.example/research:1"}, Limits: runtime.ResourceLimits{CPUMillis: 1000, MemoryBytes: 2 << 30}, Labels: runtime.ManagedLabels("workspace-123")})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if handle.RuntimeID != "abcdef0123456789" {
		t.Fatalf("runtime ID = %q", handle.RuntimeID)
	}
	if err := adapter.StartWorkspace(context.Background(), handle.RuntimeID); err != nil {
		t.Fatalf("start workspace: %v", err)
	}
	if err := adapter.StopWorkspace(context.Background(), handle.RuntimeID, time.Second); err != nil {
		t.Fatalf("stop workspace: %v", err)
	}
	if err := adapter.RemoveWorkspace(context.Background(), handle.RuntimeID); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	if len(calls) != 10 {
		t.Fatalf("Docker API calls = %d, want 10: %v", len(calls), calls)
	}
}

func TestCapabilitiesDetectRootlessPodmanAndMissingCPULimits(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case "/version":
			body = `{"ApiVersion":"1.44"}`
		case "/v1.44/info":
			body = `{"NCPU":8,"MemTotal":8589934592,"MemoryLimit":true,"PidsLimit":true,"CpuCfsQuota":false,"CpuCfsPeriod":false,"Rootless":true}`
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}}
	name, err := adapter.Name(context.Background())
	if err != nil || name != runtime.RuntimeNamePodman {
		t.Fatalf("runtime name = %q, err=%v, want rootless Podman", name, err)
	}
	capabilities, err := adapter.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if capabilities.SupportsCPUResourceLimits || !capabilities.SupportsMemoryResourceLimits || !capabilities.SupportsPIDLimits || capabilities.SupportsResourceLimits {
		t.Fatalf("unexpected rootless capabilities: %+v", capabilities)
	}
	_, err = adapter.CreateWorkspace(context.Background(), runtime.WorkspaceSpec{WorkspaceID: "workspace-123", Image: runtime.Image{Reference: "registry.example/research:1"}, Limits: runtime.ResourceLimits{CPUMillis: 1000, MemoryBytes: 2 << 30}})
	if !errors.Is(err, runtime.ErrNotSupported) {
		t.Fatalf("rootless create error = %v, want unsupported resource limits", err)
	}
}

func TestCapabilitiesDetectCgroupV2ResourceLimits(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case "/version":
			body = `{"ApiVersion":"1.44"}`
		case "/v1.44/info":
			body = `{"NCPU":8,"MemTotal":8589934592,"MemoryLimit":true,"PidsLimit":true,"CpuCfsQuota":false,"CpuCfsPeriod":false,"CgroupVersion":"2","Rootless":true}`
		default:
			return dockerResponse(http.StatusNotFound, `not found`), nil
		}
		return dockerResponse(http.StatusOK, body), nil
	})}}
	capabilities, err := adapter.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if !capabilities.SupportsCPUResourceLimits || !capabilities.SupportsResourceLimits {
		t.Fatalf("unexpected cgroup v2 capabilities: %+v", capabilities)
	}
}

func TestOpenInternalServiceRequiresLoopbackPortMapping(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/version":
			return dockerResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		case "/v1.45/containers/abcdef0123456789/json":
			return dockerResponse(http.StatusOK, `{"Id":"abcdef0123456789","State":{"Status":"running"},"NetworkSettings":{"Ports":{"5900/tcp":[{"HostIp":"0.0.0.0","HostPort":"10000"}]}}}`), nil
		default:
			return dockerResponse(http.StatusNotFound, `not found`), nil
		}
	})}}
	_, err := adapter.OpenInternalService(context.Background(), "abcdef0123456789", 5900, 10000)
	if !errors.Is(err, runtime.ErrConflict) {
		t.Fatalf("internal service error = %v, want loopback mapping conflict", err)
	}
}

func TestOpenShellUsesDockerExecUpgradeAndResize(t *testing.T) {
	var calls []string
	stream := &testStream{Reader: strings.NewReader("shell> ")}
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.Method+" "+request.URL.Path+"?"+request.URL.RawQuery)
		switch request.URL.Path {
		case "/version":
			return dockerResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		case "/v1.45/containers/abcdef0123456789/exec":
			return dockerResponse(http.StatusCreated, `{"Id":"abc123"}`), nil
		case "/v1.45/exec/abc123/start":
			if request.Header.Get("Upgrade") != "tcp" || request.Header.Get("Connection") != "Upgrade" {
				t.Fatalf("exec start did not request an upgraded stream: %+v", request.Header)
			}
			return &http.Response{StatusCode: http.StatusSwitchingProtocols, Status: "101 Switching Protocols", Body: stream, Header: http.Header{"Connection": {"Upgrade"}, "Upgrade": {"tcp"}}}, nil
		case "/v1.45/exec/abc123/resize":
			if request.URL.Query().Get("w") != "120" || request.URL.Query().Get("h") != "40" {
				t.Fatalf("unexpected resize query: %s", request.URL.RawQuery)
			}
			return dockerResponse(http.StatusOK, ``), nil
		default:
			return dockerResponse(http.StatusNotFound, `not found`), nil
		}
	})}}
	terminal, err := adapter.OpenShell(context.Background(), "abcdef0123456789", []string{"/bin/sh", "-l"})
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	defer terminal.Close()
	buffer := make([]byte, 32)
	count, err := terminal.Read(buffer)
	if err != nil || string(buffer[:count]) != "shell> " {
		t.Fatalf("terminal read = %q, err=%v", string(buffer[:count]), err)
	}
	if _, err := terminal.Write([]byte("printf ok\n")); err != nil {
		t.Fatalf("terminal write: %v", err)
	}
	if err := terminal.Resize(context.Background(), 120, 40); err != nil {
		t.Fatalf("terminal resize: %v", err)
	}
	if got, want := string(stream.Written.Bytes()), "printf ok\n"; got != want {
		t.Fatalf("stream input = %q, want %q", got, want)
	}
	if len(calls) != 6 {
		t.Fatalf("Docker API calls = %d, want 6: %v", len(calls), calls)
	}
}

func TestCreateWorkspaceMapsTypedConfiguration(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/version":
			return dockerResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		case "/v1.45/info":
			return dockerResponse(http.StatusOK, `{"NCPU":8,"MemTotal":8589934592,"MemoryLimit":true,"PidsLimit":true,"CpuCfsQuota":true,"CpuCfsPeriod":true}`), nil
		case "/v1.45/containers/create":
			var body struct {
				Cmd        []string `json:"Cmd"`
				Env        []string `json:"Env"`
				User       string   `json:"User"`
				HostConfig struct {
					NetworkMode  string                                         `json:"NetworkMode"`
					Mounts       []struct{ Source, Target string }              `json:"Mounts"`
					PortBindings map[string][]struct{ HostIP, HostPort string } `json:"PortBindings"`
				} `json:"HostConfig"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if len(body.Cmd) != 2 || body.Cmd[0] != "/bin/sh" || len(body.Env) != 1 || body.Env[0] != "DESKTOP_PORT=10000" || body.User != "" || body.HostConfig.NetworkMode != "bridge" {
				t.Fatalf("unexpected typed configuration: %+v", body)
			}
			if len(body.HostConfig.Mounts) != 1 || body.HostConfig.Mounts[0].Source != "cows-workspace-123-workspace-data" || body.HostConfig.Mounts[0].Target != "/workspace" {
				t.Fatalf("unexpected mounts: %+v", body.HostConfig.Mounts)
			}
			binding := body.HostConfig.PortBindings["5900/tcp"]
			if len(binding) != 1 || binding[0].HostIP != "127.0.0.1" || binding[0].HostPort != "10000" {
				t.Fatalf("unexpected port binding: %+v", body.HostConfig.PortBindings)
			}
			return dockerResponse(http.StatusCreated, `{"Id":"abcdef0123456789"}`), nil
		default:
			return dockerResponse(http.StatusNotFound, `not found`), nil
		}
	})}}
	_, err := adapter.CreateWorkspace(context.Background(), runtime.WorkspaceSpec{
		WorkspaceID: "workspace-123", Image: runtime.Image{Reference: "registry.example/research:1"},
		Limits: runtime.ResourceLimits{CPUMillis: 1000, MemoryBytes: 2 << 30}, Labels: runtime.ManagedLabels("workspace-123"),
		Command: []string{"/bin/sh", "-l"}, Environment: []runtime.EnvironmentVariable{{Name: "DESKTOP_PORT", Value: "10000"}},
		Mounts: []runtime.Mount{{Name: "workspace-data", ContainerPath: "/workspace"}},
		Ports:  []runtime.PortBinding{{ServiceName: "desktop", Protocol: "tcp", ContainerPort: 5900, HostPort: 10000, HostIP: "127.0.0.1"}}, NetworkMode: "bridge",
	})
	if err != nil {
		t.Fatalf("create configured workspace: %v", err)
	}
}

func TestCreateWorkspaceUsesPodmanLibpodForPasswdEntry(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/version":
			return dockerResponse(http.StatusOK, `{"ApiVersion":"4.9"}`), nil
		case "/v4.9/info":
			return dockerResponse(http.StatusOK, `{"NCPU":8,"MemTotal":8589934592,"MemoryLimit":true,"PidsLimit":true,"CgroupVersion":"2","Rootless":true,"IDMappings":{"UIDMap":[{"ContainerID":0,"HostID":100000,"Size":65536}],"GIDMap":[{"ContainerID":0,"HostID":100000,"Size":65536}]}}`), nil
		case "/v4.9/libpod/containers/create":
			var body struct {
				User        string `json:"user"`
				PasswdEntry string `json:"passwd_entry"`
				UserNS      struct {
					UIDMappings []struct {
						ContainerID int64 `json:"container_id"`
						HostID      int64 `json:"host_id"`
						Size        int64 `json:"size"`
					} `json:"uidmapping"`
					GIDMappings []struct {
						ContainerID int64 `json:"container_id"`
						HostID      int64 `json:"host_id"`
						Size        int64 `json:"size"`
					} `json:"gidmapping"`
				} `json:"userns"`
				NetNS struct {
					Mode string `json:"nsmode"`
				} `json:"netns"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode Podman create request: %v", err)
			}
			if body.User != "1000:1001" || body.PasswdEntry != "alice:x:1000:1001:Alice:/home/alice:/bin/bash" || len(body.UserNS.UIDMappings) != 1 || len(body.UserNS.GIDMappings) != 1 || body.UserNS.UIDMappings[0].ContainerID != 0 || body.UserNS.UIDMappings[0].HostID != 1 || body.UserNS.UIDMappings[0].Size != 65535 || body.UserNS.GIDMappings[0].HostID != 1 || body.NetNS.Mode != "none" {
				t.Fatalf("unexpected Podman identity configuration: %+v", body)
			}
			return dockerResponse(http.StatusCreated, `{"Id":"abcdef0123456789"}`), nil
		default:
			return dockerResponse(http.StatusNotFound, `not found`), nil
		}
	})}}
	_, err := adapter.CreateWorkspace(context.Background(), runtime.WorkspaceSpec{
		WorkspaceID: "workspace-123",
		Image:       runtime.Image{Reference: "registry.example/research:1"},
		Limits:      runtime.ResourceLimits{CPUMillis: 1000, MemoryBytes: 2 << 30},
		User:        &runtime.ContainerUser{Username: "alice", UID: 1000, GID: 1001, Name: "Alice", Home: "/home/alice", Shell: "/bin/bash", PasswdEntry: "alice:x:1000:1001:Alice:/home/alice:/bin/bash"},
	})
	if err != nil {
		t.Fatalf("create Podman workspace: %v", err)
	}
}

func TestExplicitRootlessMappingLeavesInvokingUserUnmapped(t *testing.T) {
	mappings, err := explicitRootlessMapping([]idMapping{{ContainerID: 0, HostID: 100000, Size: 65536}}, 1000)
	if err != nil {
		t.Fatalf("build explicit mapping: %v", err)
	}
	if len(mappings) != 1 || mappings[0] != (idMapping{ContainerID: 0, HostID: 1, Size: 65535}) {
		t.Fatalf("unexpected mapping: %+v", mappings)
	}
	if _, err := explicitRootlessMapping([]idMapping{{ContainerID: 0, HostID: 100000, Size: 1000}}, 999); !errors.Is(err, runtime.ErrNotSupported) {
		t.Fatalf("expected out-of-range identity to be rejected, got %v", err)
	}
}

func dockerResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type testStream struct {
	*strings.Reader
	Written bytes.Buffer
}

func (s *testStream) Write(value []byte) (int, error) { return s.Written.Write(value) }
func (s *testStream) Close() error                    { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
