package workspace

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/repository"
	"github.com/cows-project/cows/internal/runtime"
)

func concurrencyWorkspace(t *testing.T) (*Service, string, domain.Workspace, *lifecycleRuntime) {
	t.Helper()
	service, _, adminID, store := testService(t)
	input := validTemplateInput()
	input.AllowedRoles = []domain.Role{domain.RoleAdministrator}
	template, err := service.CreateTemplate(context.Background(), adminID, input)
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.CreateWorkspace(context.Background(), adminID, CreateWorkspaceInput{Name: "concurrency", TemplateID: template.ID})
	if err != nil {
		t.Fatal(err)
	}
	fake := &lifecycleRuntime{}
	return NewWithRuntimeAndMountRoots(store, fake, t.TempDir(), t.TempDir()), adminID, value, fake
}

type pauseStartKey struct{}
type pauseStartStore struct {
	repository.Store
	entered, resume chan struct{}
	once            sync.Once
}

func (s *pauseStartStore) FindWorkspaceByID(ctx context.Context, id string) (domain.Workspace, error) {
	if ctx.Value(pauseStartKey{}) == true {
		s.once.Do(func() { close(s.entered); <-s.resume })
	}
	return s.Store.FindWorkspaceByID(ctx, id)
}

func TestStartAndStopUseTheSameLockOrder(t *testing.T) {
	service, actorID, value, _ := concurrencyWorkspace(t)
	paused := &pauseStartStore{Store: service.store, entered: make(chan struct{}), resume: make(chan struct{})}
	service.store = paused
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started, stopped := make(chan error, 1), make(chan error, 1)
	go func() {
		started <- service.StartWorkspace(context.WithValue(ctx, pauseStartKey{}, true), actorID, value.ID)
	}()
	<-paused.entered
	go func() { stopped <- service.StopWorkspace(ctx, actorID, value.ID) }()
	// Let stop reach the workspace lock while start is paused in its load.
	time.Sleep(10 * time.Millisecond)
	close(paused.resume)
	for _, done := range []chan error{started, stopped} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("overlapping lifecycle operation: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("start and stop deadlocked")
		}
	}
}

func TestWorkspaceStreamDoesNotHoldGlobalAdmission(t *testing.T) {
	service, actorID, value, _ := concurrencyWorkspace(t)
	release, err := service.acquireWorkspaceLock(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.StartWorkspace(ctx, actorID, value.ID) }()
	time.Sleep(10 * time.Millisecond)
	admissionCtx, cancelAdmission := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelAdmission()
	admit, err := service.acquireAdmission(admissionCtx)
	if err != nil {
		t.Fatalf("stream blocked admission for unrelated workspaces: %v", err)
	}
	admit()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting start = %v", err)
	}
}

func TestAdmissionWaitIsCancelable(t *testing.T) {
	service, actorID, value, _ := concurrencyWorkspace(t)
	release, err := service.acquireAdmission(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := service.StartWorkspace(ctx, actorID, value.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting admission = %v", err)
	}
}

type pauseListStore struct {
	repository.Store
	entered, resume chan struct{}
	once            sync.Once
}

func (s *pauseListStore) ListAllWorkspaces(ctx context.Context) ([]domain.Workspace, error) {
	values, err := s.Store.ListAllWorkspaces(ctx)
	s.once.Do(func() { close(s.entered); <-s.resume })
	return values, err
}

func TestTimeoutReloadsConnectionStateAfterWaiting(t *testing.T) {
	service, actorID, value, fake := concurrencyWorkspace(t)
	if err := service.StartWorkspace(context.Background(), actorID, value.ID); err != nil {
		t.Fatal(err)
	}
	paused := &pauseListStore{Store: service.store, entered: make(chan struct{}), resume: make(chan struct{})}
	service.store = paused
	service.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	release, err := service.acquireWorkspaceLock(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.RunTimeouts(context.Background()) }()
	<-paused.entered
	close(paused.resume)
	if err := service.store.RecordWorkspaceSessionStart(context.Background(), value.ID, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if fake.stopped != 0 {
		t.Fatal("timeout used a stale disconnected snapshot")
	}
}

func TestReconcileDoesNotOverwriteAConcurrentStop(t *testing.T) {
	service, actorID, value, fake := concurrencyWorkspace(t)
	if err := service.StartWorkspace(context.Background(), actorID, value.ID); err != nil {
		t.Fatal(err)
	}
	fake.observed = []runtime.ObservedWorkspace{{WorkspaceID: value.ID, RuntimeID: fake.lastID, State: runtime.StateRunning, ObservedAt: time.Now()}}
	paused := &pauseListStore{Store: service.store, entered: make(chan struct{}), resume: make(chan struct{})}
	service.store = paused
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reconciled, stopped := make(chan error, 1), make(chan error, 1)
	go func() { reconciled <- service.Reconcile(ctx) }()
	<-paused.entered
	go func() { stopped <- service.StopWorkspace(ctx, actorID, value.ID) }()
	deadline := time.After(time.Second)
	for len(service.lockForWorkspace(value.ID).semaphore) == 0 {
		select {
		case err := <-stopped:
			if err != nil {
				t.Fatal(err)
			}
			stopped <- nil
			goto stoppedOrLocked
		case <-deadline:
			t.Fatal("stop never reached its workspace lock")
		default:
			time.Sleep(time.Millisecond)
		}
	}
stoppedOrLocked:
	close(paused.resume)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if err := <-reconciled; err != nil {
		t.Fatal(err)
	}
	current, err := service.store.FindWorkspaceByID(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.ObservedState != "stopped" {
		t.Fatalf("reconciliation overwrote concurrent stop with %q", current.ObservedState)
	}
}

type countedStream struct{ closes int }

func (*countedStream) Read([]byte) (int, error)               { return 0, io.EOF }
func (*countedStream) Write(p []byte) (int, error)            { return len(p), nil }
func (s *countedStream) Close() error                         { s.closes++; return nil }
func (*countedStream) Resize(context.Context, int, int) error { return nil }

type connectionRuntime struct {
	*lifecycleRuntime
	streams []*countedStream
}

func (r *connectionRuntime) OpenShell(context.Context, string, []string) (runtime.Terminal, error) {
	stream := &countedStream{}
	r.streams = append(r.streams, stream)
	return stream, nil
}

func TestStreamCloseIsSingleUseAndCannotCloseANewerRunningPeriod(t *testing.T) {
	service, actorID, value, fake := concurrencyWorkspace(t)
	connectionFake := &connectionRuntime{lifecycleRuntime: fake}
	service.runtime = connectionFake
	// The template already permits terminal access.
	base := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	if err := service.StartWorkspace(context.Background(), actorID, value.ID); err != nil {
		t.Fatal(err)
	}
	old, err := service.OpenTerminal(context.Background(), actorID, value.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RestartWorkspace(context.Background(), actorID, value.ID); err != nil {
		t.Fatal(err)
	}
	current, err := service.OpenTerminal(context.Background(), actorID, value.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}
	}
	state, err := service.store.FindWorkspaceByID(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveSessions != 1 || !state.IdleSince.IsZero() {
		t.Fatalf("old close changed current connections: %+v", state)
	}
	for i := 0; i < 2; i++ {
		if err := current.Close(); err != nil {
			t.Fatal(err)
		}
	}
	state, err = service.store.FindWorkspaceByID(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveSessions != 0 || state.IdleSince.IsZero() {
		t.Fatalf("closed stream retained a phantom connection: %+v", state)
	}
	if connectionFake.streams[0].closes != 1 || connectionFake.streams[1].closes != 1 {
		t.Fatal("runtime stream closed more than once")
	}
}

func TestProcessRestartDropsPhantomConnectionsButReconcileKeepsLiveOnes(t *testing.T) {
	service, actorID, value, fake := concurrencyWorkspace(t)
	base := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	if err := service.StartWorkspace(context.Background(), actorID, value.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordWorkspaceConnection(context.Background(), actorID, value.ID); err != nil {
		t.Fatal(err)
	}
	fake.observed = []runtime.ObservedWorkspace{{WorkspaceID: value.ID, RuntimeID: fake.lastID, State: runtime.StateRunning, ObservedAt: base}}
	restartedAt := base.Add(24 * time.Hour)
	restarted := NewWithRuntime(service.store, fake)
	restarted.now = func() time.Time { return restartedAt }
	if err := restarted.ResetWorkspaceConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := service.store.FindWorkspaceByID(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveSessions != 0 || !state.IdleSince.Equal(restartedAt) {
		t.Fatalf("startup retained phantom connection: %+v", state)
	}
	if err := restarted.RunTimeouts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.stopped != 0 {
		t.Fatal("startup stopped a newly idle workspace before its grace period")
	}
	if err := restarted.RecordWorkspaceConnection(context.Background(), actorID, value.ID); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = service.store.FindWorkspaceByID(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveSessions != 1 || !state.IdleSince.IsZero() {
		t.Fatal("ordinary reconciliation reset a current connection")
	}
	// Model a second crash, with no disconnect callback, then advance past its
	// newly established idle deadline to prove cleanup becomes possible.
	restarted = NewWithRuntime(service.store, fake)
	restarted.now = func() time.Time { return restartedAt }
	if err := restarted.ResetWorkspaceConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return restartedAt.Add(24 * time.Hour) }
	if err := restarted.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RunTimeouts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.stopped != 1 {
		t.Fatalf("post-restart idle stop calls = %d", fake.stopped)
	}
}

func TestProcessStartupPreservesExistingIdleDeadline(t *testing.T) {
	service, actorID, value, _ := concurrencyWorkspace(t)
	base := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	if err := service.StartWorkspace(context.Background(), actorID, value.ID); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return base.Add(time.Hour) }
	if err := service.ResetWorkspaceConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := service.store.FindWorkspaceByID(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.IdleSince.Equal(base) {
		t.Fatalf("startup extended an existing idle deadline to %v", state.IdleSince)
	}
}

type snapshotWorkspaceStore struct {
	repository.Store
	loaded chan struct{}
	once   sync.Once
}

func (s *snapshotWorkspaceStore) FindWorkspaceByID(ctx context.Context, id string) (domain.Workspace, error) {
	value, err := s.Store.FindWorkspaceByID(ctx, id)
	s.once.Do(func() { close(s.loaded) })
	return value, err
}

func TestLifecycleReloadsStateAfterWaitingForAFileStream(t *testing.T) {
	for _, operation := range []string{"stop", "delete"} {
		t.Run(operation, func(t *testing.T) {
			service, actorID, value, fake := concurrencyWorkspace(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := service.store.UpdateWorkspaceObservedState(ctx, value.ID, "stopped", "runtime-current", "", "", time.Now(), time.Now()); err != nil {
				t.Fatal(err)
			}
			snapshot := &snapshotWorkspaceStore{Store: service.store, loaded: make(chan struct{})}
			service.store = snapshot
			release, err := service.acquireWorkspaceLock(ctx, value.ID)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if operation == "stop" {
					done <- service.StopWorkspace(ctx, actorID, value.ID)
				} else {
					done <- service.DeleteWorkspace(ctx, actorID, value.ID)
				}
			}()
			<-snapshot.loaded
			if err := service.store.UpdateWorkspaceObservedState(ctx, value.ID, "running", "runtime-current", "", "", time.Now(), time.Now()); err != nil {
				t.Fatal(err)
			}
			release()
			err = <-done
			if operation == "stop" {
				if err != nil || fake.stopped != 1 {
					t.Fatalf("stop skipped newly running workspace: stops=%d err=%v", fake.stopped, err)
				}
			} else if !errors.Is(err, ErrWorkspaceStateConflict) || fake.removed != 0 {
				t.Fatalf("delete used stale stopped state: removes=%d err=%v", fake.removed, err)
			}
		})
	}
}
