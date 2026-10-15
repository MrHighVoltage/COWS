package podman

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
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
		if actual := stateFromPodman(input); actual != expected {
			t.Errorf("stateFromPodman(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestAdapterWithoutPodmanReturnsUnavailable(t *testing.T) {
	adapter, err := New(t.TempDir() + "/missing.sock")
	if err != nil {
		t.Fatalf("create adapter: %v", err)
	}
	_, err = adapter.Capabilities(context.Background())
	if !errors.Is(err, runtime.ErrUnavailable) {
		t.Fatalf("missing Podman socket error = %v", err)
	}
	if err := adapter.StopWorkspace(context.Background(), "abc", time.Second); !errors.Is(err, runtime.ErrUnavailable) {
		t.Fatalf("stop Podman error = %v", err)
	}
}

func TestListManagedUsesVersionedPodmanAPI(t *testing.T) {
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

func TestImageAvailableUsesImageInspection(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/version" {
			return podmanResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		}
		if strings.HasSuffix(request.URL.Path, "/images/registry.example/research:1/json") {
			return podmanResponse(http.StatusOK, `{}`), nil
		}
		return podmanResponse(http.StatusNotFound, `not found`), nil
	})}}
	available, err := adapter.ImageAvailable(context.Background(), runtime.Image{Reference: "registry.example/research:1"})
	if err != nil || !available {
		t.Fatalf("available image = %v, err=%v; want true", available, err)
	}
	available, err = adapter.ImageAvailable(context.Background(), runtime.Image{Reference: "registry.example/missing:1"})
	if err != nil || available {
		t.Fatalf("missing image = %v, err=%v; want false", available, err)
	}
}

func TestPullImageConsumesStreamingResponse(t *testing.T) {
	adapter := &Adapter{}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/version":
			return podmanResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		case "/v1.45/images/create":
			return podmanResponse(http.StatusOK, "{\"status\":\"Downloading\",\"progressDetail\":{\"current\":10,\"total\":100}}\n{\"status\":\"Download complete\"}\n"), nil
		default:
			return podmanResponse(http.StatusNotFound, `not found`), nil
		}
	})
	adapter.client = &http.Client{Transport: transport}
	adapter.streamClient = &http.Client{Transport: transport}
	if err := adapter.PullImage(context.Background(), runtime.Image{Reference: "registry.example/research:1"}); err != nil {
		t.Fatalf("pull image: %v", err)
	}
}

func TestLifecycleUsesApprovedPodmanRequests(t *testing.T) {
	var calls []string
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.Method+" "+request.URL.Path+"?"+request.URL.RawQuery)
		var body string
		switch request.URL.Path {
		case "/version":
			body = `{"ApiVersion":"1.45"}`
		case "/v1.45/info":
			body = `{"NCPU":8,"MemTotal":8589934592,"ServerVersion":"4.9.3","MemoryLimit":true,"PidsLimit":true,"CpuCfsQuota":true,"CpuCfsPeriod":true,"Rootless":true}`
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
		t.Fatalf("Podman API calls = %d, want 10: %v", len(calls), calls)
	}
}

func TestCapabilitiesDetectRootlessPodmanAndMissingCPULimits(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case "/version":
			body = `{"ApiVersion":"1.44"}`
		case "/v1.44/info":
			body = `{"NCPU":8,"MemTotal":8589934592,"ServerVersion":"4.9.3","MemoryLimit":true,"PidsLimit":true,"CpuCfsQuota":false,"CpuCfsPeriod":false,"Rootless":true}`
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
			body = `{"NCPU":8,"MemTotal":8589934592,"ServerVersion":"4.9.3","MemoryLimit":true,"PidsLimit":true,"CpuCfsQuota":false,"CpuCfsPeriod":false,"CgroupVersion":"2","Rootless":true}`
		default:
			return podmanResponse(http.StatusNotFound, `not found`), nil
		}
		return podmanResponse(http.StatusOK, body), nil
	})}}
	capabilities, err := adapter.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if !capabilities.SupportsCPUResourceLimits || !capabilities.SupportsResourceLimits {
		t.Fatalf("unexpected cgroup v2 capabilities: %+v", capabilities)
	}
}

func TestPodmanVersionGateFailsClosedBelowMinimum(t *testing.T) {
	tests := []struct {
		version   string
		supported bool
	}{
		{version: "3.4.4"},
		{version: "4.0.3"},
		{version: ""},
		{version: "unknown"},
		{version: "4"},
		{version: "4.1.0", supported: true},
		{version: "4.1.0-dev", supported: true},
		{version: "4.9.3", supported: true},
		{version: "5.0.0-rc1", supported: true},
		{version: "6.1.2", supported: true},
		{version: "10.0", supported: true},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			info, err := json.Marshal(map[string]any{"NCPU": 8, "MemTotal": 8589934592, "ServerVersion": test.version, "MemoryLimit": true, "PidsLimit": true, "CgroupVersion": "2", "Rootless": true})
			if err != nil {
				t.Fatalf("encode info: %v", err)
			}
			created := false
			adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/version":
					return podmanResponse(http.StatusOK, `{"ApiVersion":"1.41"}`), nil
				case "/v1.41/info":
					return podmanResponse(http.StatusOK, string(info)), nil
				case "/v1.41/containers/create":
					created = true
					return podmanResponse(http.StatusCreated, `{"Id":"abcdef0123456789"}`), nil
				default:
					return podmanResponse(http.StatusNotFound, `not found`), nil
				}
			})}}
			_, nameErr := adapter.Name(context.Background())
			_, capabilitiesErr := adapter.Capabilities(context.Background())
			_, createErr := adapter.CreateWorkspace(context.Background(), runtime.WorkspaceSpec{WorkspaceID: "workspace-123", Image: runtime.Image{Reference: "registry.example/research:1"}, Limits: runtime.ResourceLimits{CPUMillis: 1000, MemoryBytes: 2 << 30}})
			if test.supported {
				if nameErr != nil || capabilitiesErr != nil || createErr != nil || !created {
					t.Fatalf("Podman %q rejected: name=%v capabilities=%v create=%v created=%v", test.version, nameErr, capabilitiesErr, createErr, created)
				}
				return
			}
			for _, err := range []error{nameErr, capabilitiesErr, createErr} {
				if !errors.Is(err, runtime.ErrNotSupported) || !strings.Contains(err.Error(), "Podman 4.1.0 or newer") {
					t.Fatalf("Podman %q error = %v, want unsupported version", test.version, err)
				}
			}
			if created {
				t.Fatalf("Podman %q created a container despite the version gate", test.version)
			}
		})
	}
}

func TestOpenInternalServiceRequiresLoopbackPortMapping(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/version":
			return podmanResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		case "/v1.45/containers/abcdef0123456789/json":
			return podmanResponse(http.StatusOK, `{"Id":"abcdef0123456789","State":{"Status":"running"},"NetworkSettings":{"Ports":{"5900/tcp":[{"HostIp":"0.0.0.0","HostPort":"10000"}]}}}`), nil
		default:
			return podmanResponse(http.StatusNotFound, `not found`), nil
		}
	})}}
	_, err := adapter.OpenInternalService(context.Background(), "abcdef0123456789", 5900, 10000)
	if !errors.Is(err, runtime.ErrConflict) {
		t.Fatalf("internal service error = %v, want loopback mapping conflict", err)
	}
}

func TestOpenShellUsesPodmanExecUpgradeAndResize(t *testing.T) {
	var calls []string
	stream := &testStream{Reader: strings.NewReader("shell> ")}
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.Method+" "+request.URL.Path+"?"+request.URL.RawQuery)
		switch request.URL.Path {
		case "/version":
			return podmanResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		case "/v1.45/containers/abcdef0123456789/exec":
			return podmanResponse(http.StatusCreated, `{"Id":"abc123"}`), nil
		case "/v1.45/exec/abc123/start":
			if request.Header.Get("Upgrade") != "tcp" || request.Header.Get("Connection") != "Upgrade" {
				t.Fatalf("exec start did not request an upgraded stream: %+v", request.Header)
			}
			return &http.Response{StatusCode: http.StatusSwitchingProtocols, Status: "101 Switching Protocols", Body: stream, Header: http.Header{"Connection": {"Upgrade"}, "Upgrade": {"tcp"}}}, nil
		case "/v1.45/exec/abc123/resize":
			if request.URL.Query().Get("w") != "120" || request.URL.Query().Get("h") != "40" {
				t.Fatalf("unexpected resize query: %s", request.URL.RawQuery)
			}
			return podmanResponse(http.StatusOK, ``), nil
		default:
			return podmanResponse(http.StatusNotFound, `not found`), nil
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
		t.Fatalf("Podman API calls = %d, want 6: %v", len(calls), calls)
	}
}

func TestTerminalCloseTerminatesExec(t *testing.T) {
	cleanupStarted := false
	cleanupUser := ""
	var cleanupCommand []string
	var events []string
	stream := &testStream{Reader: strings.NewReader("shell> "), onClose: func() { events = append(events, "stream-close") }}
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/version":
			return podmanResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		case "/v1.45/containers/abcdef0123456789/exec":
			var body struct {
				Cmd  []string `json:"Cmd"`
				User string   `json:"User"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				return nil, err
			}
			if body.User == "0" {
				events = append(events, "cleanup-create")
				cleanupUser = body.User
				cleanupCommand = body.Cmd
				return podmanResponse(http.StatusCreated, `{"Id":"c1ea12345678"}`), nil
			}
			return podmanResponse(http.StatusCreated, `{"Id":"abc123"}`), nil
		case "/v1.45/exec/abc123/start":
			return &http.Response{StatusCode: http.StatusSwitchingProtocols, Status: "101 Switching Protocols", Body: stream, Header: http.Header{"Connection": {"Upgrade"}, "Upgrade": {"tcp"}}}, nil
		case "/v1.45/exec/abc123/json":
			events = append(events, "inspect")
			return podmanResponse(http.StatusOK, `{"Running":true,"Pid":`+strconv.Itoa(os.Getpid())+`}`), nil
		case "/v1.45/exec/c1ea12345678/start":
			events = append(events, "cleanup-start")
			var body struct {
				Detach bool `json:"Detach"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				return nil, err
			}
			if !body.Detach {
				t.Fatal("terminal cleanup exec must be detached")
			}
			cleanupStarted = true
			return podmanResponse(http.StatusOK, ``), nil
		case "/v1.45/exec/c1ea12345678/json":
			return podmanResponse(http.StatusOK, `{"Running":false}`), nil
		default:
			return podmanResponse(http.StatusNotFound, `not found`), nil
		}
	})}}
	terminal, err := adapter.OpenShell(context.Background(), "abcdef0123456789", []string{"/bin/sh", "-l"})
	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	if err := terminal.Close(); err != nil {
		t.Fatalf("close terminal: %v", err)
	}
	if !cleanupStarted || cleanupUser != "0" || len(cleanupCommand) != 3 || cleanupCommand[0] != "/bin/sh" || cleanupCommand[1] != "-c" || !strings.Contains(cleanupCommand[2], "kill -TERM -") {
		t.Fatalf("unexpected terminal cleanup: started=%v user=%q command=%q", cleanupStarted, cleanupUser, cleanupCommand)
	}
	if got, want := strings.Join(events, ","), "inspect,cleanup-create,cleanup-start,stream-close"; got != want {
		t.Fatalf("terminal close order = %q, want %q", got, want)
	}
}

func TestOpenShellAsPassesContainerUID(t *testing.T) {
	var user string
	stream := &testStream{Reader: strings.NewReader("shell> ")}
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/version":
			return podmanResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		case "/v1.45/containers/abcdef0123456789/exec":
			var body struct {
				User string `json:"User"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				return nil, err
			}
			user = body.User
			return podmanResponse(http.StatusCreated, `{"Id":"abc123"}`), nil
		case "/v1.45/exec/abc123/start":
			return &http.Response{StatusCode: http.StatusSwitchingProtocols, Status: "101 Switching Protocols", Body: stream, Header: http.Header{"Connection": {"Upgrade"}, "Upgrade": {"tcp"}}}, nil
		default:
			return podmanResponse(http.StatusNotFound, `not found`), nil
		}
	})}}
	terminal, err := adapter.OpenShellAs(context.Background(), "abcdef0123456789", 0, []string{"/bin/bash", "-l"})
	if err != nil {
		t.Fatalf("open shell as UID: %v", err)
	}
	defer terminal.Close()
	if user != "0" {
		t.Fatalf("Podman exec user = %q, want %q", user, "0")
	}
}

func TestWorkspaceResourceUsageReadsNonStreamingStats(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/version" {
			return podmanResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		}
		if request.URL.Path != "/v1.45/containers/abcdef0123456789/stats" || request.URL.Query().Get("stream") != "false" {
			t.Fatalf("unexpected stats request: %s", request.URL.String())
		}
		return podmanResponse(http.StatusOK, `{"cpu_stats":{"cpu_usage":{"total_usage":300},"system_cpu_usage":1200,"online_cpus":2},"precpu_stats":{"cpu_usage":{"total_usage":100},"system_cpu_usage":1000},"memory_stats":{"usage":4096},"pids_stats":{"current":7}}`), nil
	})}}
	usage, err := adapter.WorkspaceResourceUsage(context.Background(), "abcdef0123456789")
	if err != nil {
		t.Fatalf("workspace resource usage: %v", err)
	}
	if usage.CPUPercentMilli != 200000 || usage.MemoryBytes != 4096 || usage.PIDs != 7 || usage.ObservedAt.IsZero() {
		t.Fatalf("unexpected resource usage: %+v", usage)
	}
}

func TestWorkspaceLogsDemuxesFramedStdoutAndStderr(t *testing.T) {
	frame := func(stream byte, text string) []byte {
		header := make([]byte, 8)
		header[0] = stream
		binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
		return append(header, []byte(text)...)
	}
	var body []byte
	body = append(body, frame(1, "2026-08-17T10:00:00.000000000Z booting up\n")...)
	body = append(body, frame(2, "2026-08-17T10:00:01.500000000Z a warning on stderr\n")...)
	body = append(body, frame(1, "2026-08-17T10:00:02.000000000Z line one\nline two\n")...)

	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/version" {
			return podmanResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		}
		if request.URL.Path != "/v1.45/containers/abcdef0123456789/logs" {
			t.Fatalf("unexpected logs request path: %s", request.URL.String())
		}
		if request.URL.Query().Get("tail") != "50" || request.URL.Query().Get("timestamps") != "true" {
			t.Fatalf("unexpected logs query: %s", request.URL.RawQuery)
		}
		return podmanResponse(http.StatusOK, string(body)), nil
	})}}

	lines, err := adapter.WorkspaceLogs(context.Background(), "abcdef0123456789", 50)
	if err != nil {
		t.Fatalf("workspace logs: %v", err)
	}
	want := []string{"booting up", "a warning on stderr", "line one", "line two"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(lines), len(want), lines)
	}
	for index, line := range lines {
		if line.Text != want[index] {
			t.Fatalf("line %d text = %q, want %q", index, line.Text, want[index])
		}
		if line.Timestamp.IsZero() {
			t.Fatalf("line %d timestamp should be parsed, got zero", index)
		}
	}
	if !lines[0].Timestamp.Before(lines[1].Timestamp) {
		t.Fatalf("timestamps should be in order: %v then %v", lines[0].Timestamp, lines[1].Timestamp)
	}
}

func TestCreateWorkspaceMapsTypedConfiguration(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/version":
			return podmanResponse(http.StatusOK, `{"ApiVersion":"1.45"}`), nil
		case "/v1.45/info":
			return podmanResponse(http.StatusOK, `{"NCPU":8,"MemTotal":8589934592,"ServerVersion":"4.9.3","MemoryLimit":true,"PidsLimit":true,"CpuCfsQuota":true,"CpuCfsPeriod":true,"Rootless":true}`), nil
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
			return podmanResponse(http.StatusCreated, `{"Id":"abcdef0123456789"}`), nil
		default:
			return podmanResponse(http.StatusNotFound, `not found`), nil
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
			return podmanResponse(http.StatusOK, `{"ApiVersion":"4.9"}`), nil
		case "/v4.9/info":
			return podmanResponse(http.StatusOK, `{"NCPU":8,"MemTotal":8589934592,"ServerVersion":"4.9.3","MemoryLimit":true,"PidsLimit":true,"CgroupVersion":"2","Rootless":true,"IDMappings":{"UIDMap":[{"ContainerID":0,"HostID":100000,"Size":65536}],"GIDMap":[{"ContainerID":0,"HostID":100000,"Size":65536}]}}`), nil
		case "/v4.9/libpod/info":
			return podmanResponse(http.StatusOK, `{"host":{"idMappings":{"uidmap":[{"container_id":0,"host_id":1000,"size":1},{"container_id":1,"host_id":100000,"size":65536}],"gidmap":[{"container_id":0,"host_id":1000,"size":1},{"container_id":1,"host_id":100000,"size":65536}]}}}`), nil
		case "/v4.9/libpod/containers/create":
			var body struct {
				User        string            `json:"user"`
				PasswdEntry string            `json:"passwd_entry"`
				UserNS      map[string]string `json:"userns"`
				IDMappings  struct {
					UIDMap []idMapping `json:"UIDMap"`
					GIDMap []idMapping `json:"GIDMap"`
				} `json:"idmappings"`
				NetNS struct {
					Mode string `json:"nsmode"`
				} `json:"netns"`
				Networks map[string]struct{} `json:"newNetworks"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode Podman create request: %v", err)
			}
			if body.User != "1000:1001" || body.PasswdEntry != "alice:x:1000:1001:Alice:/home/alice:/bin/bash" || body.NetNS.Mode != "none" {
				t.Fatalf("unexpected Podman identity configuration: %+v", body)
			}
			// Libpod ignores idmappings unless the user namespace is private,
			// and ignores mapping lists nested inside userns entirely.
			wantMapping := []idMapping{{ContainerID: 0, HostID: 1, Size: 65536}}
			if !reflect.DeepEqual(body.UserNS, map[string]string{"nsmode": "private"}) || !reflect.DeepEqual(body.IDMappings.UIDMap, wantMapping) || !reflect.DeepEqual(body.IDMappings.GIDMap, wantMapping) {
				t.Fatalf("user namespace = %#v, idmappings = %+v, want private namespace with %+v", body.UserNS, body.IDMappings, wantMapping)
			}
			return podmanResponse(http.StatusCreated, `{"Id":"abcdef0123456789"}`), nil
		default:
			return podmanResponse(http.StatusNotFound, `not found`), nil
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

func TestCreateWorkspaceUsesNamedNetworkForPodmanLibpod(t *testing.T) {
	adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/version":
			return podmanResponse(http.StatusOK, `{"ApiVersion":"4.9"}`), nil
		case "/v4.9/info":
			return podmanResponse(http.StatusOK, `{"NCPU":8,"MemTotal":8589934592,"ServerVersion":"4.9.3","MemoryLimit":true,"PidsLimit":true,"CgroupVersion":"2","Rootless":true,"IDMappings":{"UIDMap":[{"ContainerID":0,"HostID":100000,"Size":65536}],"GIDMap":[{"ContainerID":0,"HostID":100000,"Size":65536}]}}`), nil
		case "/v4.9/libpod/info":
			return podmanResponse(http.StatusOK, `{"host":{"idMappings":{"uidmap":[{"container_id":0,"host_id":1000,"size":1},{"container_id":1,"host_id":100000,"size":65536}],"gidmap":[{"container_id":0,"host_id":1000,"size":1},{"container_id":1,"host_id":100000,"size":65536}]}}}`), nil
		case "/v4.9/libpod/containers/create":
			// Decode the raw keys: encoding/json matches field names
			// case-insensitively, but Libpod silently ignores any key other
			// than "Networks" and attaches the default network instead.
			var fields map[string]json.RawMessage
			if err := json.NewDecoder(request.Body).Decode(&fields); err != nil {
				t.Fatalf("decode Podman create request: %v", err)
			}
			var netns struct {
				Mode string `json:"nsmode"`
			}
			if err := json.Unmarshal(fields["netns"], &netns); err != nil || netns.Mode != "bridge" {
				t.Fatalf("network namespace = %s, want bridge mode", fields["netns"])
			}
			var networks map[string]struct{}
			if err := json.Unmarshal(fields["Networks"], &networks); err != nil || len(networks) != 1 {
				t.Fatalf("Networks = %s, want only the workspace network", fields["Networks"])
			}
			if _, ok := networks["cows-net-workspace-123"]; !ok {
				t.Fatalf("named networks = %#v", networks)
			}
			if _, ok := fields["newNetworks"]; ok {
				t.Fatal("request still sends the ignored newNetworks key")
			}
			return podmanResponse(http.StatusCreated, `{"Id":"abcdef0123456789"}`), nil
		default:
			return podmanResponse(http.StatusNotFound, `not found`), nil
		}
	})}}
	_, err := adapter.CreateWorkspace(context.Background(), runtime.WorkspaceSpec{
		WorkspaceID: "workspace-123",
		Image:       runtime.Image{Reference: "registry.example/research:1"},
		Limits:      runtime.ResourceLimits{CPUMillis: 1000, MemoryBytes: 2 << 30},
		NetworkMode: "cows-net-workspace-123",
		User:        &runtime.ContainerUser{Username: "alice", UID: 1000, GID: 1001, Name: "Alice", Home: "/home/alice", Shell: "/bin/bash", PasswdEntry: "alice:x:1000:1001:Alice:/home/alice:/bin/bash"},
	})
	if err != nil {
		t.Fatalf("create Podman workspace with named network: %v", err)
	}
}

func TestCreateWorkspaceSendsNamedVolumesSeparatelyForPodmanLibpod(t *testing.T) {
	type podmanMount struct {
		Type        string   `json:"type"`
		Source      string   `json:"source"`
		Destination string   `json:"destination"`
		Options     []string `json:"options"`
	}
	type namedVolume struct {
		Name    string   `json:"Name"`
		Dest    string   `json:"Dest"`
		Options []string `json:"Options"`
	}
	tests := []struct {
		name        string
		mounts      []runtime.Mount
		wantMounts  []podmanMount
		wantVolumes []namedVolume
	}{
		{
			name:        "managed volume",
			mounts:      []runtime.Mount{{Name: "designs", Type: "volume", Source: "cows-workspace-123-designs", ContainerPath: "/foss/designs", RemapOwnership: true}},
			wantVolumes: []namedVolume{{Name: "cows-workspace-123-designs", Dest: "/foss/designs", Options: []string{"U"}}},
		},
		{
			name:        "untyped mount defaults to derived volume",
			mounts:      []runtime.Mount{{Name: "data", ContainerPath: "/data", ReadOnly: true}},
			wantVolumes: []namedVolume{{Name: "cows-workspace-123-data", Dest: "/data", Options: []string{"ro"}}},
		},
		{
			name: "directory and volume together",
			mounts: []runtime.Mount{
				{Name: "home", Type: "bind", Source: "/srv/cows/mounts/home", ContainerPath: "/home/alice", RemapOwnership: true},
				{Name: "designs", Type: "volume", Source: "cows-workspace-123-designs", ContainerPath: "/foss/designs", RemapOwnership: true},
			},
			wantMounts:  []podmanMount{{Type: "bind", Source: "/srv/cows/mounts/home", Destination: "/home/alice", Options: []string{"U"}}},
			wantVolumes: []namedVolume{{Name: "cows-workspace-123-designs", Dest: "/foss/designs", Options: []string{"U"}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &Adapter{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/version":
					return podmanResponse(http.StatusOK, `{"ApiVersion":"4.9"}`), nil
				case "/v4.9/info":
					return podmanResponse(http.StatusOK, `{"NCPU":8,"MemTotal":8589934592,"ServerVersion":"4.9.3","MemoryLimit":true,"PidsLimit":true,"CgroupVersion":"2","Rootless":true,"IDMappings":{"UIDMap":[{"ContainerID":0,"HostID":100000,"Size":65536}],"GIDMap":[{"ContainerID":0,"HostID":100000,"Size":65536}]}}`), nil
				case "/v4.9/libpod/info":
					return podmanResponse(http.StatusOK, `{"host":{"idMappings":{"uidmap":[{"container_id":0,"host_id":1000,"size":1},{"container_id":1,"host_id":100000,"size":65536}],"gidmap":[{"container_id":0,"host_id":1000,"size":1},{"container_id":1,"host_id":100000,"size":65536}]}}}`), nil
				case "/v4.9/libpod/containers/create":
					var body struct {
						Mounts  []podmanMount `json:"mounts"`
						Volumes []namedVolume `json:"volumes"`
					}
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Fatalf("decode Podman create request: %v", err)
					}
					if !reflect.DeepEqual(body.Mounts, test.wantMounts) {
						t.Fatalf("mounts = %+v, want %+v", body.Mounts, test.wantMounts)
					}
					if !reflect.DeepEqual(body.Volumes, test.wantVolumes) {
						t.Fatalf("volumes = %+v, want %+v", body.Volumes, test.wantVolumes)
					}
					return podmanResponse(http.StatusCreated, `{"Id":"abcdef0123456789"}`), nil
				default:
					return podmanResponse(http.StatusNotFound, `not found`), nil
				}
			})}}
			_, err := adapter.CreateWorkspace(context.Background(), runtime.WorkspaceSpec{
				WorkspaceID: "workspace-123",
				Image:       runtime.Image{Reference: "registry.example/research:1"},
				Limits:      runtime.ResourceLimits{CPUMillis: 1000, MemoryBytes: 2 << 30},
				Mounts:      test.mounts,
				User:        &runtime.ContainerUser{Username: "alice", UID: 1000, GID: 1001, Name: "Alice", Home: "/home/alice", Shell: "/bin/bash", PasswdEntry: "alice:x:1000:1001:Alice:/home/alice:/bin/bash"},
			})
			if err != nil {
				t.Fatalf("create Podman workspace with mounts: %v", err)
			}
		})
	}
}

func TestFileAccessUsesNamespaceIdentityOfConfiguredUser(t *testing.T) {
	tests := []struct {
		name             string
		spec             runtime.FileAccessSpec
		wantUID, wantGID string
		wantUnsupported  bool
	}{
		{name: "configured user is shifted past the unmapped host account", spec: runtime.FileAccessSpec{ContainerUID: 1000, ContainerGID: 1001, ConfiguredUser: true}, wantUID: "1001", wantGID: "1002"},
		{name: "configured root is not namespace root", spec: runtime.FileAccessSpec{ContainerUID: 0, ContainerGID: 0, ConfiguredUser: true}, wantUID: "1", wantGID: "1"},
		{name: "image identity keeps namespace IDs", spec: runtime.FileAccessSpec{ContainerUID: 0, ContainerGID: 0}, wantUID: "0", wantGID: "0"},
		{name: "configured user outside subordinate range", spec: runtime.FileAccessSpec{ContainerUID: 65536, ContainerGID: 1000, ConfiguredUser: true}, wantUnsupported: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &Adapter{helperExecutable: "/usr/local/bin/cows", podmanBinary: "podman", client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/version":
					return podmanResponse(http.StatusOK, `{"ApiVersion":"4.9"}`), nil
				case "/v4.9/info":
					return podmanResponse(http.StatusOK, `{"NCPU":8,"MemTotal":8589934592,"ServerVersion":"4.9.3","Rootless":true}`), nil
				case "/v4.9/libpod/info":
					return podmanResponse(http.StatusOK, `{"host":{"idMappings":{"uidmap":[{"container_id":0,"host_id":1000,"size":1},{"container_id":1,"host_id":100000,"size":65536}],"gidmap":[{"container_id":0,"host_id":1000,"size":1},{"container_id":1,"host_id":100000,"size":65536}]}}}`), nil
				default:
					return podmanResponse(http.StatusNotFound, `not found`), nil
				}
			})}}
			spec := test.spec
			spec.MountType, spec.Source = "directory", "/srv/cows/mounts/cows-workspace-123/data"
			access, err := adapter.OpenFileAccess(context.Background(), spec)
			if test.wantUnsupported {
				if !errors.Is(err, runtime.ErrNotSupported) {
					t.Fatalf("open file access error = %v, want unsupported identity", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("open file access: %v", err)
			}
			command, err := adapter.fileCommand(context.Background(), access.(*fileAccess), "list", ".")
			if err != nil {
				t.Fatalf("file command: %v", err)
			}
			args := strings.Join(command.Args, " ")
			if !strings.Contains(args, " --uid "+test.wantUID+" --gid "+test.wantGID+" ") {
				t.Fatalf("file helper arguments = %q, want --uid %s --gid %s", args, test.wantUID, test.wantGID)
			}
		})
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

func podmanResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type testStream struct {
	*strings.Reader
	Written bytes.Buffer
	onClose func()
}

func (s *testStream) Write(value []byte) (int, error) { return s.Written.Write(value) }
func (s *testStream) Close() error {
	if s.onClose != nil {
		s.onClose()
	}
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
