//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/database"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository/sqlite"
	"github.com/cows-project/cows/internal/runtime/podman"
	"github.com/cows-project/cows/internal/workspace"
)

var (
	repoRoot      string
	e2eBinaryOnce sync.Once
	e2eBinaryPath string
	e2eBinaryErr  error
)

func TestMain(m *testing.M) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "e2e: cannot locate repository root")
		os.Exit(1)
	}
	repoRoot = filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	os.Exit(m.Run())
}

// e2eImage returns the pre-pulled image the suite may use. The suite never
// pulls an image: a missing image is an operator setup problem, not a test
// failure. COWS_PODMAN_TEST_IMAGE is accepted so the live adapter suite and
// this suite can share one environment variable.
func e2eImage(t *testing.T) string {
	t.Helper()
	image := strings.TrimSpace(os.Getenv("COWS_E2E_IMAGE"))
	if image == "" {
		image = strings.TrimSpace(os.Getenv("COWS_PODMAN_TEST_IMAGE"))
	}
	if image == "" {
		t.Skip("set COWS_E2E_IMAGE (or COWS_PODMAN_TEST_IMAGE) to a locally available image")
	}
	return image
}

func e2eSocket(t *testing.T) string {
	t.Helper()
	socket := strings.TrimSpace(os.Getenv("COWS_E2E_SOCKET"))
	if socket == "" {
		socket = strings.TrimSpace(os.Getenv("COWS_PODMAN_SOCKET"))
	}
	if socket == "" {
		socket = filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "podman", "podman.sock")
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Skipf("rootless Podman socket %s is unavailable", socket)
	}
	return socket
}

func ensureE2EBinary(t *testing.T) string {
	t.Helper()
	e2eBinaryOnce.Do(func() {
		dir := filepath.Join(repoRoot, ".cows-test", "e2e-bin")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			e2eBinaryErr = fmt.Errorf("create e2e binary directory: %w", err)
			return
		}
		path := filepath.Join(dir, "cows")
		command := exec.Command("go", "build", "-o", path, "./cmd/cows")
		command.Dir = repoRoot
		output, err := command.CombinedOutput()
		if err != nil {
			e2eBinaryErr = fmt.Errorf("build cows binary: %w\n%s", err, output)
			return
		}
		e2eBinaryPath = path
	})
	if e2eBinaryErr != nil {
		t.Fatal(e2eBinaryErr)
	}
	return e2eBinaryPath
}

type instanceOptions struct {
	SeedUsers           int
	UserQuota           domain.UserQuota
	RegistrationEnabled bool
	RegistrationQuota   domain.UserQuota
	TemplateName        string
}

type instance struct {
	t *testing.T

	root        string
	dbPath      string
	mountRoot   string
	archiveRoot string
	socket      string
	image       string
	templateID  string
	port        int
	baseURL     string

	binary  string
	logPath string
	logFile *os.File
	command *exec.Cmd
	wait    chan struct{}
	waitErr error

	adminID       string
	adminUsername string
	adminPassword string
	userIDs       []string
	usernames     []string
	passwords     []string

	options instanceOptions
}

func newInstance(t *testing.T, options instanceOptions) *instance {
	t.Helper()
	image := e2eImage(t)
	socket := e2eSocket(t)
	binary := ensureE2EBinary(t)
	root, err := os.MkdirTemp("", "cows-e2e-")
	if err != nil {
		t.Fatalf("create e2e data root: %v", err)
	}
	inst := &instance{
		t: t, root: root, socket: socket, image: image, binary: binary, options: options,
		dbPath:      filepath.Join(root, "cows.db"),
		mountRoot:   filepath.Join(root, "mounts"),
		archiveRoot: filepath.Join(root, "archive"),
		logPath:     filepath.Join(root, "server.log"),
	}
	// Register cleanup before fixture creation or process startup: a failed
	// seed, readiness wait, or early subtest must not leak a container, volume,
	// or subordinate-owned temporary directory.
	t.Cleanup(func() { inst.close() })
	if err := os.MkdirAll(inst.mountRoot, 0o700); err != nil {
		t.Fatalf("create mount root: %v", err)
	}
	if err := os.MkdirAll(inst.archiveRoot, 0o700); err != nil {
		t.Fatalf("create archive root: %v", err)
	}
	if options.UserQuota.MaxWorkspaces == 0 && options.UserQuota.MaxRunningWorkspaces == 0 {
		options.UserQuota = defaultUserQuota()
		inst.options = options
	}
	inst.seedDatabase()
	inst.start()
	return inst
}

func defaultUserQuota() domain.UserQuota {
	return domain.UserQuota{
		MaxCPUMillis:         1000,
		MaxMemoryBytes:       1 << 30,
		MaxStorageBytes:      10 << 30,
		MaxWorkspaces:        2,
		MaxRunningWorkspaces: 1,
	}
}

func (inst *instance) seedDatabase() {
	t := inst.t
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := database.Open(ctx, inst.dbPath)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	defer db.Close()
	store := sqlite.New(db)
	policy := auth.RegistrationPolicy{Enabled: inst.options.RegistrationEnabled, DefaultQuota: inst.options.RegistrationQuota}
	authService, err := auth.New(store, time.Hour, policy)
	if err != nil {
		t.Fatalf("create seed auth service: %v", err)
	}
	const initialAdminPassword = "e2e initial admin password"
	inst.adminUsername = "e2e-admin"
	inst.adminPassword = "e2e changed admin password"
	if _, err := authService.BootstrapAdministrator(ctx, auth.CreateUserInput{Username: inst.adminUsername, Password: initialAdminPassword}); err != nil {
		t.Fatalf("bootstrap e2e administrator: %v", err)
	}
	admin, _, err := authService.Authenticate(ctx, inst.adminUsername, initialAdminPassword)
	if err != nil {
		t.Fatalf("authenticate seed administrator: %v", err)
	}
	if err := authService.ChangePassword(ctx, admin.ID, initialAdminPassword, inst.adminPassword); err != nil {
		t.Fatalf("change seed administrator password: %v", err)
	}
	inst.adminID = admin.ID

	templateName := inst.options.TemplateName
	if templateName == "" {
		templateName = "E2E Workspace " + randomSuffix(t)
	}
	service := workspace.New(store)
	template, err := service.CreateTemplate(ctx, admin.ID, e2eTemplateInput(templateName, inst.image))
	if err != nil {
		t.Fatalf("create e2e template: %v", err)
	}
	inst.templateID = template.ID

	quota := inst.options.UserQuota
	if quota.MaxCPUMillis == 0 && quota.MaxMemoryBytes == 0 {
		quota = defaultUserQuota()
	}
	suffix := randomSuffix(t)
	for index := 0; index < inst.options.SeedUsers; index++ {
		username := fmt.Sprintf("e2e-user-%02d-%s", index+1, suffix)
		password := fmt.Sprintf("e2e user password %02d %s", index+1, suffix)
		changed := password + " changed"
		user, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{
			Username: username, Email: username + "@example.test", DisplayName: fmt.Sprintf("E2E User %02d", index+1),
			Password: password, Role: domain.RoleUser,
		})
		if err != nil {
			t.Fatalf("create e2e user %d: %v", index+1, err)
		}
		if err := authService.ChangePassword(ctx, user.ID, password, changed); err != nil {
			t.Fatalf("clear mandatory password change for user %d: %v", index+1, err)
		}
		quota.UserID = user.ID
		quota.CreatedAt = time.Now().UTC()
		quota.UpdatedAt = quota.CreatedAt
		if err := store.UpsertUserQuota(ctx, quota); err != nil {
			t.Fatalf("set e2e user %d quota: %v", index+1, err)
		}
		inst.userIDs = append(inst.userIDs, user.ID)
		inst.usernames = append(inst.usernames, username)
		inst.passwords = append(inst.passwords, changed)
	}
}

func e2eTemplateInput(name, image string) workspace.TemplateInput {
	return workspace.TemplateInput{
		Name:                            name,
		Description:                     "End-to-end test template",
		ImageReference:                  image,
		DefaultCPUMillis:                100,
		MaxCPUMillis:                    500,
		DefaultMemoryBytes:              256 << 20,
		MaxMemoryBytes:                  512 << 20,
		DefaultStorageBytes:             1 << 30,
		InitialConnectionTimeoutSeconds: 3600,
		StoppedRetentionSeconds:         3600,
		AccessMethods:                   []domain.AccessMethod{domain.AccessTerminal, domain.AccessFiles, domain.AccessLogs},
		AllowedRoles:                    []domain.Role{domain.RoleUser, domain.RoleAdministrator},
		GroupAccessMode:                 domain.GroupAccessExclude,
		Enabled:                         true,
		Configuration: domain.TemplateConfiguration{
			// A PID-1 process that handles SIGTERM keeps the normal stop
			// scenario well inside Podman's ten-second stop grace period.
			// A bare `sleep 3600` ignores SIGTERM and makes stop take the full
			// grace period (see the stop-timeout finding in the test plan).
			Command: []string{"/bin/sh", "-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
			Mounts: []domain.TemplateMount{
				{Name: "data", Type: domain.TemplateMountVolume, ContainerPath: "/data", FileManager: true},
				{Name: "home", Type: domain.TemplateMountDirectory, ContainerPath: "/home/cows", FileManager: true},
			},
		},
	}
}

func (inst *instance) start() {
	t := inst.t
	t.Helper()
	if inst.port == 0 {
		inst.port = reservePort(t)
	}
	logFile, err := os.OpenFile(inst.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open e2e server log: %v", err)
	}
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"COWS_LISTEN_ADDR="+fmt.Sprintf("127.0.0.1:%d", inst.port),
		"COWS_DATABASE_PATH="+inst.dbPath,
		"COWS_MOUNT_ROOT="+inst.mountRoot,
		"COWS_MOUNT_ARCHIVE_ROOT="+inst.archiveRoot,
		"COWS_PODMAN_SOCKET="+inst.socket,
		"COWS_HOST_STORAGE_BYTES="+strconv.FormatInt(10<<30, 10),
		"COWS_HOST_CPU_OVERBOOKING_FACTOR=1",
		"COWS_HOST_MEMORY_OVERBOOKING_FACTOR=1",
		"COWS_LOG_LEVEL=warn",
		"COWS_SESSION_LIFETIME=1h",
		"COWS_COOKIE_SECURE=false",
		"COWS_NETWORK_ISOLATION_ENABLED=false",
	)
	if inst.options.RegistrationEnabled {
		quota := inst.options.RegistrationQuota
		if quota.MaxCPUMillis == 0 {
			quota = defaultUserQuota()
		}
		env = append(env,
			"COWS_REGISTRATION_ENABLED=true",
			"COWS_REGISTRATION_DEFAULT_CPU_MILLIS="+strconv.FormatInt(quota.MaxCPUMillis, 10),
			"COWS_REGISTRATION_DEFAULT_MEMORY_BYTES="+strconv.FormatInt(quota.MaxMemoryBytes, 10),
			"COWS_REGISTRATION_DEFAULT_STORAGE_BYTES="+strconv.FormatInt(quota.MaxStorageBytes, 10),
			"COWS_REGISTRATION_DEFAULT_MAX_WORKSPACES="+strconv.FormatInt(quota.MaxWorkspaces, 10),
			"COWS_REGISTRATION_DEFAULT_MAX_RUNNING_WORKSPACES="+strconv.FormatInt(quota.MaxRunningWorkspaces, 10),
		)
	}
	command := exec.Command(inst.binary)
	command.Dir = inst.root
	command.Env = env
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start e2e cows process: %v", err)
	}
	inst.command = command
	inst.logFile = logFile
	inst.wait = make(chan struct{})
	go func() {
		inst.waitErr = command.Wait()
		close(inst.wait)
	}()
	inst.baseURL = fmt.Sprintf("http://127.0.0.1:%d", inst.port)
	inst.waitReady()
}

func (inst *instance) waitReady() {
	t := inst.t
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(2 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-inst.wait:
			t.Fatalf("e2e cows process exited before readiness: %v\n%s", inst.waitErr, tailFile(inst.logPath, 16<<10))
		default:
		}
		response, err := client.Get(inst.baseURL + "/readyz")
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("readyz status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("e2e cows process did not become ready: %v\n%s", lastErr, tailFile(inst.logPath, 16<<10))
}

func (inst *instance) stop() {
	if inst.command == nil || inst.wait == nil {
		return
	}
	_ = inst.command.Process.Signal(syscall.SIGTERM)
	select {
	case <-inst.wait:
	case <-time.After(25 * time.Second):
		_ = inst.command.Process.Kill()
		<-inst.wait
	}
	if inst.logFile != nil {
		_ = inst.logFile.Close()
		inst.logFile = nil
	}
	inst.command = nil
	inst.wait = nil
}

func (inst *instance) restart() {
	inst.t.Helper()
	inst.stop()
	inst.start()
}

func (inst *instance) close() {
	inst.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	inst.cleanupRuntime(ctx)
	removeDataRoot(inst.root)
}

func (inst *instance) cleanupRuntime(ctx context.Context) {
	if _, err := os.Stat(inst.dbPath); err != nil {
		return
	}
	db, err := database.Open(ctx, inst.dbPath)
	if err != nil {
		return
	}
	defer db.Close()
	store := sqlite.New(db)
	adapter, err := podman.New(inst.socket)
	if err != nil {
		return
	}
	service := workspace.NewWithRuntimeAndMountRoots(store, adapter, inst.mountRoot, inst.archiveRoot)
	workspaces, err := store.ListAllWorkspaces(ctx)
	if err == nil {
		for _, value := range workspaces {
			if value.ObservedState == "running" {
				_ = service.StopWorkspace(ctx, value.OwnerUserID, value.ID)
			}
			if err := service.DeleteWorkspace(ctx, value.OwnerUserID, value.ID); err != nil {
				inst.t.Logf("e2e cleanup: delete workspace %s: %v", value.ID, err)
				if value.RuntimeID != "" {
					_ = exec.CommandContext(ctx, "podman", "rm", "--force", "--time", "0", value.RuntimeID).Run()
				}
			}
		}
	}
	volumes, _ := store.ListAllRetainedWorkspaceVolumes(ctx)
	for _, volume := range volumes {
		_ = adapter.RemoveVolume(ctx, volume.VolumeName)
		_, _ = store.ConsumeRetainedWorkspaceVolume(ctx, volume.WorkspaceID, volume.MountName, volume.OwnerUserID)
	}
	directories, _ := store.ListAllRetainedWorkspaceDirectories(ctx)
	for _, directory := range directories {
		_ = service.DeleteRetainedDirectoryByID(ctx, directory.WorkspaceID)
	}
}

func removeDataRoot(root string) {
	// Mapped files belong to subordinate IDs, so the host account cannot
	// always remove them without the Podman namespace.
	command := exec.Command("podman", "unshare", "rm", "-rf", root)
	if err := command.Run(); err == nil {
		return
	}
	_ = os.RemoveAll(root)
}

func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve e2e HTTP port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	value := make([]byte, 6)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("generate e2e suffix: %v", err)
	}
	return hex.EncodeToString(value)
}

func tailFile(path string, limit int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	start := info.Size() - limit
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, _ := io.ReadAll(file)
	return string(data)
}

func (inst *instance) adminClient(t *testing.T) *client {
	t.Helper()
	return newLoggedInClient(t, inst.baseURL, inst.adminUsername, inst.adminPassword)
}

func (inst *instance) userClient(t *testing.T, index int) *client {
	t.Helper()
	if index < 0 || index >= len(inst.usernames) {
		t.Fatalf("e2e user index %d out of range", index)
	}
	return newLoggedInClient(t, inst.baseURL, inst.usernames[index], inst.passwords[index])
}

func (inst *instance) userClientByName(t *testing.T, username string) *client {
	t.Helper()
	for index, candidate := range inst.usernames {
		if candidate == username {
			return inst.userClient(t, index)
		}
	}
	t.Fatalf("unknown e2e user %q", username)
	return nil
}
