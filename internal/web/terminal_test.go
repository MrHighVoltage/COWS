package web

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository/sqlite"
	"github.com/cows-project/cows/internal/runtime"
	"github.com/cows-project/cows/internal/workspace"
)

func TestTerminalWebSocketAuthorizesAndBridgesShell(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "cows.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	store := sqlite.New(db)
	authService, err := auth.New(store, time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	if _, err := authService.BootstrapAdministrator(ctx, auth.CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	admin, _, err := authService.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	if err := authService.ChangePassword(ctx, admin.ID, "correct horse battery staple", "changed correct horse battery staple"); err != nil {
		t.Fatalf("change administrator password: %v", err)
	}
	if _, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "student", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create student: %v", err)
	}
	student, token, err := authService.Authenticate(ctx, "student", "another correct password")
	if err != nil {
		t.Fatalf("authenticate student: %v", err)
	}
	if err := authService.ChangePassword(ctx, student.ID, "another correct password", "changed student password"); err != nil {
		t.Fatalf("change student password: %v", err)
	}
	_, token, err = authService.Authenticate(ctx, "student", "changed student password")
	if err != nil {
		t.Fatalf("authenticate changed student password: %v", err)
	}

	fake := newTerminalRuntime()
	service := workspace.NewWithRuntime(store, fake)
	template, err := service.CreateTemplate(ctx, admin.ID, workspace.TemplateInput{
		Name: "Terminal template", ImageReference: "registry.example/research:1", DefaultCPUMillis: 1000,
		MaxCPUMillis: 2000, DefaultMemoryBytes: 2 << 30, MaxMemoryBytes: 4 << 30,
		DefaultStorageBytes: 10 << 30, InitialConnectionTimeoutSeconds: 3600,
		StoppedRetentionSeconds: 3600,
		AccessMethods:           []domain.AccessMethod{domain.AccessTerminal}, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create terminal template: %v", err)
	}
	value, err := service.CreateWorkspace(ctx, student.ID, workspace.CreateWorkspaceInput{Name: "Shell", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := service.UpdateObservedState(ctx, value.ID, "running", "abcdef0123456789", "", time.Now()); err != nil {
		t.Fatalf("mark workspace running: %v", err)
	}
	server, err := New(db, authService, service, nil, fake, Options{SessionLifetime: time.Hour})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen test server: %v", err)
	}
	httpServer := &httptest.Server{Listener: listener, Config: &http.Server{Handler: server.Handler()}}
	httpServer.Start()
	defer httpServer.Close()
	wsURL := "ws" + httpServer.URL[len("http"):] + "/workspaces/" + value.ID + "/terminal/ws"
	connection, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {"cows_session=" + token}}})
	if err != nil {
		t.Fatalf("dial terminal: %v", err)
	}
	defer connection.CloseNow()

	messageType, data, err := connection.Read(ctx)
	if err != nil || messageType != websocket.MessageBinary || string(data) != "$ " {
		t.Fatalf("initial terminal output: type=%v data=%q err=%v", messageType, data, err)
	}
	if err := connection.Write(ctx, websocket.MessageText, []byte("echo hello\n")); err != nil {
		t.Fatalf("write terminal input: %v", err)
	}
	_, data, err = connection.Read(ctx)
	if err != nil || string(data) != "echo hello\r\n" {
		t.Fatalf("terminal echo: data=%q err=%v", data, err)
	}
	if err := connection.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`)); err != nil {
		t.Fatalf("write terminal resize: %v", err)
	}
	if !fake.waitForResize(120, 40) {
		t.Fatal("terminal resize was not forwarded to runtime")
	}
}

type terminalRuntime struct {
	terminal *fakeTerminal
}

func newTerminalRuntime() *terminalRuntime { return &terminalRuntime{terminal: newFakeTerminal()} }
func (r *terminalRuntime) Name(context.Context) (string, error) {
	return runtime.RuntimeNamePodman, nil
}
func (r *terminalRuntime) Capabilities(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{RuntimeName: runtime.RuntimeNamePodman}, nil
}
func (r *terminalRuntime) ListManaged(context.Context) ([]runtime.ObservedWorkspace, error) {
	return nil, nil
}
func (r *terminalRuntime) CreateWorkspace(context.Context, runtime.WorkspaceSpec) (runtime.WorkspaceHandle, error) {
	return runtime.WorkspaceHandle{}, runtime.ErrNotSupported
}
func (r *terminalRuntime) StartWorkspace(context.Context, string) error {
	return runtime.ErrNotSupported
}
func (r *terminalRuntime) StopWorkspace(context.Context, string, time.Duration) error {
	return runtime.ErrNotSupported
}
func (r *terminalRuntime) RemoveWorkspace(context.Context, string) error {
	return runtime.ErrNotSupported
}
func (r *terminalRuntime) InspectWorkspace(context.Context, string) (runtime.ObservedWorkspace, error) {
	return runtime.ObservedWorkspace{}, runtime.ErrNotSupported
}
func (r *terminalRuntime) OpenShell(context.Context, string, []string) (runtime.Terminal, error) {
	return r.terminal, nil
}
func (r *terminalRuntime) waitForResize(cols, rows int) bool {
	return r.terminal.waitForResize(cols, rows)
}

type fakeTerminal struct {
	mu      sync.Mutex
	input   chan []byte
	closed  chan struct{}
	buffer  []byte
	written bytes.Buffer
	cols    int
	rows    int
}

func newFakeTerminal() *fakeTerminal {
	value := &fakeTerminal{input: make(chan []byte, 4), closed: make(chan struct{})}
	value.input <- []byte("$ ")
	return value
}

func (t *fakeTerminal) Read(value []byte) (int, error) {
	for {
		t.mu.Lock()
		if len(t.buffer) > 0 {
			count := copy(value, t.buffer)
			t.buffer = t.buffer[count:]
			t.mu.Unlock()
			return count, nil
		}
		t.mu.Unlock()
		select {
		case data := <-t.input:
			t.mu.Lock()
			t.buffer = append(t.buffer, data...)
			t.mu.Unlock()
		case <-t.closed:
			return 0, io.EOF
		}
	}
}

func (t *fakeTerminal) Write(value []byte) (int, error) {
	t.mu.Lock()
	t.written.Write(value)
	t.mu.Unlock()
	t.input <- append([]byte(nil), bytes.ReplaceAll(value, []byte("\n"), []byte("\r\n"))...)
	return len(value), nil
}
func (t *fakeTerminal) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}
func (t *fakeTerminal) Resize(_ context.Context, cols, rows int) error {
	t.mu.Lock()
	t.cols, t.rows = cols, rows
	t.mu.Unlock()
	return nil
}
func (t *fakeTerminal) waitForResize(cols, rows int) bool {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		t.mu.Lock()
		match := t.cols == cols && t.rows == rows
		t.mu.Unlock()
		if match {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

var _ runtime.Runtime = (*terminalRuntime)(nil)
var _ runtime.ShellRuntime = (*terminalRuntime)(nil)
var _ runtime.Terminal = (*fakeTerminal)(nil)
