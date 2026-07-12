package docker

import (
	"context"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
