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
	if err := adapter.StopWorkspace(context.Background(), "abc", time.Second); !errors.Is(err, runtime.ErrNotSupported) {
		t.Fatalf("stop support error = %v", err)
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
