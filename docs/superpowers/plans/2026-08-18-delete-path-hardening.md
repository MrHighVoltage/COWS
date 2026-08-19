# Delete-Path Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close every data-loss and permanent-orphan gap found by an audit of COWS's delete/discard/consume code paths, run after fixing commit `6cb4195` ("Fix data loss when workspace creation fails after directory reattachment") revealed this whole bug class.

**Architecture:** Each task fixes one function or adds one missing capability, following the pattern established in `6cb4195`: never let a failure-cleanup path destroy data that an earlier, already-committed step made real (a moved file, a materialized DB row). Where a physical resource (file, Podman volume) and its DB "tombstone" record must both be removed, always destroy the physical resource *before* deleting the tombstone that makes it discoverable, and surface (never swallow) a failure of the physical step so the tombstone survives and the operation is retryable.

**Tech Stack:** Go 1.x, `database/sql` + SQLite (`internal/repository/sqlite`), Podman via a `runtime.Runtime` interface, `net/http` with Go 1.22+ method-pattern routing, Go templates for HTML, table-driven and `httptest`-based tests.

## Global Constraints

- Every new/changed function needs a passing test before the task is considered done — write the failing test first (TDD), per `superpowers:test-driven-development`.
- Run `go build ./...`, `go vet ./...`, and `go test ./...` at the end of every task. All must pass before moving to the next task.
- `gofmt -l <changed files>` must report nothing.
- Do not touch files outside a task's stated `Files` list.
- Follow existing code style exactly: error wrapping conventions, comment style (only non-obvious "why", never "what"), the existing `repository.ErrNotFound`/`repository.ErrConflict` sentinel-error patterns.
- Never weaken an existing ownership check (`owner_user_id` scoping) — administrator-only bypasses are only acceptable on `/admin/*` routes, exactly mirroring how `/admin/volumes/*` already works.
- Commit after each task with `git add <exact files>` (never `-A`) and a message describing the *why*, matching this repo's existing commit style (see `git log`).

---

## Background: what the audit found

Three research passes over `internal/workspace`, `internal/web`, `internal/auth`, and `internal/repository/sqlite` found, ranked by severity:

1. **Real data loss, untested** (same bug class as `6cb4195`, one level deeper): `restoreDirectoryMounts` (`internal/workspace/mounts.go:266-315`) moves a template's directory-type mounts one at a time. It is not atomic across mounts — if a template has 2+ directory mounts and the second mount's move fails after the first succeeded, `CreateWorkspace`'s failure cleanup (`removeMountDirectories`) deletes the entire new-workspace mount tree, destroying the first mount's just-restored archived data, while its DB tombstone is already gone. Found independently by two research agents.
2. **Permanent, high-frequency orphan** (fires on every normal admin action, not just a rare race): deleting a user via `adminUserDelete` → `DeleteUserWorkspaces` → `DeleteWorkspace` creates `retained_workspace_directories` tombstone rows scoped to that user's `owner_user_id`. Once the user row is deleted, every read/download/delete path for that tombstone (`requireActor`-gated) becomes permanently unreachable, and — unlike named volumes, which have `/admin/volumes` — there is no administrator recovery page for directories at all. Real archived user files sit on disk forever with zero code path to reach them.
3. **Silent leak with a false "success" signal**: `storageVolumeDelete` (`internal/web/server.go:1934-1971`) consumes (deletes) the volume tombstone *before* attempting `RemoveVolume`, discards any error from `RemoveVolume`, and unconditionally records a `storage.volume_deleted` success audit event. If removal fails, the named volume is orphaned in Podman with zero DB pointer, while the user is told it succeeded. `adminVolumeDelete` (`internal/web/server.go:3241-3281`) already gets this right (remove real resource first, surface failures, delete the row last) — this is a direct, already-solved-elsewhere inconsistency.
4. **Same anti-pattern, no recovery path at all**: `DeleteRetainedDirectory` (`internal/workspace/mounts.go:465-489`) has the identical consume-then-best-effort-remove ordering as #3. Unlike volumes, there's no admin fallback for directories today (see #2), so an orphan here is *permanently* unrecoverable, and this path also never writes to the archive-activity forensic log unlike every other destructive step in this codebase.
5. **Missing defense-in-depth**: `auth.Service.DeleteUser` (`internal/auth/service.go:456-476`) has no invariant check that the target user actually has zero workspaces before deleting the row. It is safe today only because the one caller (`adminUserDelete` in `server.go`) happens to call `DeleteUserWorkspaces` first and check its error. `workspaces.owner_user_id` is `ON DELETE CASCADE` — a future caller (a new admin action, a CLI, a bugfix that reorders these two calls) would silently bypass all of `DeleteWorkspace`'s container-removal/archival/tombstoning logic via the DB cascade, reproducing this entire bug class at the schema level with no application-level trace.
6. **Untested but self-healing** (verify, don't necessarily need a code fix): `DeleteWorkspace`'s container-present branch (`internal/workspace/workspace.go:850-918`) archives mount directories to disk, then performs its *only* fallible-after-irreversible-step DB write, `DeleteWorkspaceRetainingStorage`, as a single SQL transaction (`internal/repository/sqlite/store.go:770-812`). The design is sound (a mid-way DB failure rolls back cleanly; a retry recomputes identical tombstone rows since `archiveMountDirectories` is itself idempotent on retry), but nothing in the test suite exercises "archive succeeds, `DeleteWorkspaceRetainingStorage` fails" to lock this guarantee in, unlike the regression test `6cb4195` added for the analogous `CreateWorkspace` scenario.

Reviewed and confirmed **not** to need changes: `Reconcile` (read-only re: destructive action), `RunTimeouts`'s delete branch (only removes the container, self-heals on retry, never touches storage), the background retention/purge sweep (confirmed not to exist anywhere, matching ADR 0022's explicit "automatic timeout cleanup never deletes or archives user data" guarantee — this is by design, not a gap), template deletion (the capability doesn't exist in the codebase at all), and `CreateWorkspace`'s volume-tombstone consumption (matches ADR 0025's documented, tested, accepted trade-off — the underlying volume is never deleted by any cleanup path).

---

### Task 1: Fix multi-mount partial-restore data loss in `restoreDirectoryMounts`

**Files:**
- Modify: `internal/workspace/mounts.go:266-315`
- Test: `internal/workspace/reattach_test.go`

**Interfaces:**
- Consumes: `domain.TemplateMount`, `domain.RetainedDirectoryMount` (existing, `internal/domain`), `ensureMountDirectories(root, workspaceID string, mounts []domain.TemplateMount) error` (existing, `mounts.go:156`).
- Produces: `restoreDirectoryMounts(root, archivedContainerPath, newWorkspaceID string, newMounts []domain.TemplateMount, archivedMounts []domain.RetainedDirectoryMount) error` keeps its exact existing signature and error (`ErrMountUnavailable`) — callers in `workspace.go` need no changes. New unexported helper: `rollbackDirectoryMoves(completed []directoryMove)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/workspace/reattach_test.go` (needs no new imports beyond what the file already has: `context`, `errors`, `os`, `path/filepath`, `testing`, plus `domain` for `TemplateMount`/`RetainedDirectoryMount`, all already imported):

```go
func TestRestoreDirectoryMountsRollsBackPartialMoveOnFailure(t *testing.T) {
	root := t.TempDir()
	archiveRoot := t.TempDir()
	workspaceID := "new-workspace"
	archivedContainerPath := filepath.Join(archiveRoot, "cows-old-workspace")

	mounts := []domain.TemplateMount{
		{Name: "alpha", Type: domain.TemplateMountDirectory, ContainerPath: "/alpha"},
		{Name: "beta", Type: domain.TemplateMountDirectory, ContainerPath: "/beta"},
	}
	archivedMounts := []domain.RetainedDirectoryMount{
		{Name: "alpha"},
		{Name: "beta"},
	}

	for _, name := range []string{"alpha", "beta"} {
		src := filepath.Join(archivedContainerPath, name)
		if err := os.MkdirAll(src, 0o700); err != nil {
			t.Fatalf("seed archived %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("content-"+name), 0o600); err != nil {
			t.Fatalf("seed archived %s file: %v", name, err)
		}
	}

	if err := ensureMountDirectories(root, workspaceID, mounts); err != nil {
		t.Fatalf("ensure mount directories: %v", err)
	}
	// Sabotage the SECOND mount's destination (non-empty directories fail
	// os.Remove with ENOTEMPTY regardless of user/permissions, so this is a
	// reliable, portable way to force a failure after the first move has
	// already succeeded, without relying on permission bits that root
	// ignores).
	betaDestination := filepath.Join(root, "cows-"+workspaceID, "beta")
	if err := os.WriteFile(filepath.Join(betaDestination, "blocker.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("sabotage beta destination: %v", err)
	}

	err := restoreDirectoryMounts(root, archivedContainerPath, workspaceID, mounts, archivedMounts)
	if !errors.Is(err, ErrMountUnavailable) {
		t.Fatalf("restoreDirectoryMounts error = %v, want ErrMountUnavailable", err)
	}

	// The first mount's move must have been rolled back: its archived
	// content must be back at the original archive path, not left sitting
	// inside the new workspace's mount tree where a caller's failure
	// cleanup would delete it.
	restoredAlpha := filepath.Join(root, "cows-"+workspaceID, "alpha", "file.txt")
	if _, err := os.Stat(restoredAlpha); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("alpha should have been rolled back out of the new workspace, stat err = %v", err)
	}
	archivedAlpha := filepath.Join(archivedContainerPath, "alpha", "file.txt")
	content, err := os.ReadFile(archivedAlpha)
	if err != nil {
		t.Fatalf("alpha content should be back at the archive path: %v", err)
	}
	if string(content) != "content-alpha" {
		t.Fatalf("rolled-back alpha content = %q, want original content", content)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/workspace/... -run TestRestoreDirectoryMountsRollsBackPartialMoveOnFailure -v`
Expected: FAIL — `alpha` is not rolled back (the current code leaves it moved into the new workspace's tree since there is no rollback logic).

- [ ] **Step 3: Add the `directoryMove` type at package level and rewrite the move loop with rollback**

In `internal/workspace/mounts.go`, replace the local `type move struct{ source, destination string }` (currently declared inside `restoreDirectoryMounts`) and the final move loop. The full function becomes:

```go
type directoryMove struct{ source, destination string }

// restoreDirectoryMounts renames each archived directory-type mount from a
// retained-directory tombstone into a newly created workspace's
// corresponding mount directory (self-service reattachment, decision 0025).
// It is all-or-nothing: every mount recorded in archivedMounts must have a
// same-named directory-type mount in newMounts, or nothing is renamed and an
// error is returned, so a tombstone is never left half-consumed. The
// destination directories are the empty ones ensureMountDirectories already
// created for the new workspace; each is removed immediately before its
// Rename since not every filesystem honors POSIX's allowance to rename a
// directory onto an existing empty one.
func restoreDirectoryMounts(root, archivedContainerPath, newWorkspaceID string, newMounts []domain.TemplateMount, archivedMounts []domain.RetainedDirectoryMount) error {
	if len(archivedMounts) == 0 {
		return nil
	}
	if root == "" || archivedContainerPath == "" {
		return ErrMountUnavailable
	}
	newByName := make(map[string]domain.TemplateMount, len(newMounts))
	for _, mount := range newMounts {
		if normalizedMountType(mount.Type) == domain.TemplateMountDirectory {
			newByName[mount.Name] = mount
		}
	}
	moves := make([]directoryMove, 0, len(archivedMounts))
	for _, archived := range archivedMounts {
		newMount, ok := newByName[archived.Name]
		if !ok {
			return fmt.Errorf("%w: no matching directory mount %q in the new template", ErrMountUnavailable, archived.Name)
		}
		archivedEntryName := archived.NamePrefix + archived.Name + archived.NameSuffix
		source, err := filepath.Abs(filepath.Join(archivedContainerPath, archivedEntryName))
		if err != nil {
			return ErrMountUnavailable
		}
		destination, err := filepath.Abs(filepath.Join(root, mountRootName(newWorkspaceID, newMount)))
		if err != nil {
			return ErrMountUnavailable
		}
		moves = append(moves, directoryMove{source: source, destination: destination})
	}
	for _, m := range moves {
		if _, err := os.Lstat(m.source); err != nil {
			return ErrMountUnavailable
		}
	}
	completed := make([]directoryMove, 0, len(moves))
	for _, m := range moves {
		// The destination is the empty directory ensureMountDirectories just
		// created. Remove it explicitly rather than relying on Rename to
		// replace an empty directory in place: that POSIX allowance is not
		// honored consistently by every filesystem this runs on.
		if err := os.Remove(m.destination); err != nil {
			rollbackDirectoryMoves(completed)
			return ErrMountUnavailable
		}
		if err := os.Rename(m.source, m.destination); err != nil {
			rollbackDirectoryMoves(completed)
			return ErrMountUnavailable
		}
		completed = append(completed, m)
	}
	return nil
}

// rollbackDirectoryMoves reverses a prefix of restoreDirectoryMounts' moves,
// most-recent first, after a later move in the same call fails. Without
// this, a partial restore (e.g. 2 of 3 directory mounts renamed before the
// 3rd fails) would leave the first two mounts' real archived content sitting
// inside the new workspace's mount tree with their tombstone already
// consumed by the caller - exactly the data a caller's failure cleanup
// (removeMountDirectories) would then delete, reproducing the bug fixed for
// CreateWorkspace as a whole in 6cb4195, one level deeper. Best-effort: a
// failed rename-back leaves that one mount's content orphaned in the new
// workspace's tree rather than destroyed, which is still strictly better
// than deletion.
func rollbackDirectoryMoves(completed []directoryMove) {
	for i := len(completed) - 1; i >= 0; i-- {
		_ = os.Rename(completed[i].destination, completed[i].source)
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/workspace/... -run TestRestoreDirectoryMountsRollsBackPartialMoveOnFailure -v`
Expected: PASS

- [ ] **Step 5: Run the full existing reattach test suite to check for regressions**

Run: `go test ./internal/workspace/... -run 'TestDirectoryReattach|TestVolumeReattach|TestReattach|TestConsumeRetained|TestRestoreDirectoryMounts' -v`
Expected: all PASS (in particular `TestDirectoryReattachmentRestoresContentIntoNewWorkspace` and `TestDirectoryReattachmentSurvivesLaterFailure` must still pass unchanged).

- [ ] **Step 6: Run full build, vet, and test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add internal/workspace/mounts.go internal/workspace/reattach_test.go
git commit -m "$(cat <<'EOF'
Roll back partial directory-mount restores instead of losing data

restoreDirectoryMounts moved a template's directory-type mounts one at
a time with no rollback: if mount N's move failed after mount 1..N-1
already succeeded, CreateWorkspace's failure cleanup deleted the
entire new workspace's mount tree, destroying the already-restored
mounts' real data while their tombstone was already consumed - the
same bug class fixed in 6cb4195, one level deeper. Failed moves now
unwind everything already renamed in the same call.
EOF
)"
```

---

### Task 2: Add a defensive workspace-ownership guard to `auth.Service.DeleteUser`

**Files:**
- Modify: `internal/auth/service.go:456-476` (function `DeleteUser`) and its error-var block (near the existing `ErrSelfDelete`/`ErrUserMustBeDisabled` declarations)
- Test: `internal/auth/service_test.go`

**Interfaces:**
- Consumes: `repository.Store.ListWorkspacesForUser(ctx, ownerUserID string) ([]domain.Workspace, error)` (existing, `internal/repository/repository.go:109`).
- Produces: new sentinel error `ErrUserHasWorkspaces = errors.New("user still owns workspaces")` in `internal/auth/service.go`. `DeleteUser`'s signature and existing error returns (`ErrSelfDelete`, `ErrUserMustBeDisabled`) are unchanged.

- [ ] **Step 1: Find the exact location of the existing error vars**

Run: `grep -n "ErrSelfDelete\|ErrUserMustBeDisabled\|ErrLastAdministrator" /home/georg/programms/COWS/internal/auth/service.go`

You'll see a `var (...)` block containing these three; add `ErrUserHasWorkspaces` there, alphabetically/logically near `ErrUserMustBeDisabled`.

- [ ] **Step 2: Write the failing test**

Add to `internal/auth/service_test.go` (check the file's existing imports/helpers first — it will already have a pattern for bootstrapping an administrator and creating a disabled user; a `workspace.Service` will additionally be needed, so add `"github.com/cows-project/cows/internal/workspace"` and `"github.com/cows-project/cows/internal/domain"` imports if not already present):

```go
func TestDeleteUserRejectsWhenWorkspacesStillOwned(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t) // use whatever this file's existing store-construction helper is called; do not invent a new one if one already exists
	authService, err := New(store, time.Hour)
	if err != nil {
		t.Fatalf("create auth service: %v", err)
	}
	admin, err := authService.BootstrapAdministrator(ctx, CreateUserInput{Username: "admin", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	target, err := authService.CreateUser(ctx, admin.ID, CreateUserInput{Username: "still-owns-workspaces", Password: "another correct password", Role: domain.RoleUser})
	if err != nil {
		t.Fatalf("create target user: %v", err)
	}
	if err := authService.SetUserDisabled(ctx, admin.ID, target.ID, true); err != nil {
		t.Fatalf("disable target user: %v", err)
	}

	workspaceService := workspace.New(store)
	template, err := workspaceService.CreateTemplate(ctx, admin.ID, workspace.TemplateInput{
		Name: "Guard Template", ImageReference: "registry.example/research:1", DefaultCPUMillis: 1000,
		MaxCPUMillis: 2000, DefaultMemoryBytes: 2 << 30, MaxMemoryBytes: 4 << 30,
		DefaultStorageBytes: 10 << 30, InitialConnectionTimeoutSeconds: 3600, StoppedRetentionSeconds: 3600,
		AccessMethods: []domain.AccessMethod{domain.AccessTerminal}, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if _, err := workspaceService.CreateWorkspace(ctx, target.ID, workspace.CreateWorkspaceInput{Name: "still-here", TemplateID: template.ID}); err != nil {
		t.Fatalf("create workspace for target: %v", err)
	}

	if err := authService.DeleteUser(ctx, admin.ID, target.ID); !errors.Is(err, ErrUserHasWorkspaces) {
		t.Fatalf("DeleteUser with an owned workspace error = %v, want ErrUserHasWorkspaces", err)
	}
	if _, err := authService.FindByID(ctx, target.ID); err != nil {
		t.Fatalf("target user should still exist after rejected deletion: %v", err)
	}
}
```

Adjust helper/method names (`newTestStore`, `FindByID`, `SetUserDisabled`'s exact signature) to match whatever `internal/auth/service_test.go` already uses elsewhere in the file — read the file first and copy its established patterns exactly rather than guessing.

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/auth/... -run TestDeleteUserRejectsWhenWorkspacesStillOwned -v`
Expected: FAIL — either a compile error (`ErrUserHasWorkspaces` undefined) or the deletion succeeding instead of being rejected.

- [ ] **Step 4: Add the guard to `DeleteUser`**

In `internal/auth/service.go`, change:

```go
	if !target.Disabled {
		return ErrUserMustBeDisabled
	}
	if err := s.store.CancelEmailNotificationsForUser(ctx, targetID); err != nil {
		return err
	}
```

to:

```go
	if !target.Disabled {
		return ErrUserMustBeDisabled
	}
	// Defense in depth: today the only caller (adminUserDelete) always calls
	// DeleteUserWorkspaces first and checks its error, but workspaces.owner_user_id
	// is ON DELETE CASCADE - a future caller that skips or reorders that step
	// would otherwise have the database cascade silently delete workspace rows,
	// bypassing DeleteWorkspace's container removal, directory archival, and
	// retained-storage tombstoning entirely.
	if workspaces, err := s.store.ListWorkspacesForUser(ctx, targetID); err != nil {
		return err
	} else if len(workspaces) > 0 {
		return ErrUserHasWorkspaces
	}
	if err := s.store.CancelEmailNotificationsForUser(ctx, targetID); err != nil {
		return err
	}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/auth/... -run TestDeleteUserRejectsWhenWorkspacesStillOwned -v`
Expected: PASS

- [ ] **Step 6: Check `adminUserDelete` in `internal/web/server.go` still behaves correctly**

Run: `grep -n "adminUserDelete" -A 20 /home/georg/programms/COWS/internal/web/server.go`

Confirm it already calls `DeleteUserWorkspaces` before `auth.DeleteUser` and returns early on its error (per the audit, it does) — this new guard is unreachable through the normal admin flow and exists purely as defense-in-depth. No changes should be needed here; if the handler maps unknown `auth.DeleteUser` errors to a generic message already, `ErrUserHasWorkspaces` will surface the same way `ErrUserMustBeDisabled` does today. If it does NOT already handle arbitrary errors gracefully (i.e., it would panic or leak an unhelpful message), fix the handler's error mapping minimally to match how it already handles `ErrUserMustBeDisabled`.

- [ ] **Step 7: Run full build, vet, and test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add internal/auth/service.go internal/auth/service_test.go
git commit -m "$(cat <<'EOF'
Reject deleting a user who still owns workspaces

DeleteUser only ever checked that the target was disabled, relying
entirely on its one caller to have already deleted the user's
workspaces first. workspaces.owner_user_id is ON DELETE CASCADE, so a
future caller that skips or reorders that step would have the
database cascade silently bypass all of DeleteWorkspace's container
removal, archival, and retained-storage tombstoning. Add the missing
invariant check directly in DeleteUser.
EOF
)"
```

---

### Task 3: Fix `storageVolumeDelete`'s consume-before-remove ordering and swallowed errors

**Files:**
- Modify: `internal/web/server.go:1934-1971` (function `storageVolumeDelete`)
- Test: `internal/web/server_test.go`

**Interfaces:**
- Consumes: `repository.Store.FindRetainedWorkspaceVolume(ctx, workspaceID, mountName, ownerUserID) (domain.RetainedWorkspaceVolume, error)` (existing, read-only counterpart already used by `storageVolumeDownload`), `repository.Store.ConsumeRetainedWorkspaceVolume` (existing), `runtime.VolumeRuntime.RemoveVolume` (existing).
- Produces: `storageVolumeDelete`'s HTTP contract is unchanged for the caller (same routes, same redirect/HTMX behavior on success or "already gone"), except that a real `RemoveVolume` failure now returns `503` instead of a false `200`/redirect.

- [ ] **Step 1: Read the existing HTTP test patterns in this package**

Run: `sed -n '1,60p;100,120p' /home/georg/programms/COWS/internal/web/server_test.go` and `grep -n "rowActionRuntime\|terminalRuntime" /home/georg/programms/COWS/internal/web/*_test.go`

Confirm the pattern: build `store := sqlite.New(db)`, `authService, _ := auth.New(store, time.Hour)`, a runtime fake embedding `*terminalRuntime` (from `internal/web/terminal_test.go`), `templateService := workspace.NewWithRuntimeAndMountRoots(store, fake, t.TempDir(), t.TempDir())`, then `server, _ := New(db, authService, templateService, nil, fake, Options{SessionLifetime: time.Hour, Store: store})` — note `Options.Store` must be set explicitly (it is not auto-populated by `New`), unlike some existing tests in this file that don't exercise `/storage/*` and so never needed it. Requests are dispatched via `server.Handler().ServeHTTP(recorder, request)`, sessions via a `&http.Cookie{Name: "cows_session", Value: token}` obtained from `authService.Authenticate`, and CSRF tokens via a GET request first, reading the `cows_csrf` cookie off the response (see `TestAdministratorCanEditGroupQuotaFromGroupView` for a complete worked example).

- [ ] **Step 2: Write the failing test**

Add to `internal/web/server_test.go`:

```go
type storageTestRuntime struct {
	*terminalRuntime
	removeVolumeErr error
	removedVolumes  []string
}

func (r *storageTestRuntime) RemoveWorkspace(context.Context, string) error { return nil }
func (r *storageTestRuntime) VolumeExists(context.Context, string) (bool, error) { return true, nil }
func (r *storageTestRuntime) RemoveVolume(_ context.Context, name string) error {
	if r.removeVolumeErr != nil {
		return r.removeVolumeErr
	}
	r.removedVolumes = append(r.removedVolumes, name)
	return nil
}

func TestStorageVolumeDeleteKeepsTombstoneWhenRemovalFails(t *testing.T) {
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
	if _, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "volume-owner", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, sessionToken, err := authService.Authenticate(ctx, "volume-owner", "another correct password")
	if err != nil {
		t.Fatalf("authenticate owner: %v", err)
	}

	fake := &storageTestRuntime{terminalRuntime: newTerminalRuntime()}
	service := workspace.NewWithRuntimeAndMountRoots(store, fake, t.TempDir(), t.TempDir())
	template, err := service.CreateTemplate(ctx, admin.ID, workspace.TemplateInput{
		Name: "Storage Volume Template", ImageReference: "registry.example/research:1", DefaultCPUMillis: 1000,
		MaxCPUMillis: 2000, DefaultMemoryBytes: 2 << 30, MaxMemoryBytes: 4 << 30,
		DefaultStorageBytes: 10 << 30, InitialConnectionTimeoutSeconds: 3600, StoppedRetentionSeconds: 3600,
		AccessMethods: []domain.AccessMethod{domain.AccessFiles}, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true,
		Configuration: domain.TemplateConfiguration{Mounts: []domain.TemplateMount{{Name: "data", Type: domain.TemplateMountVolume, ContainerPath: "/data", FileManager: true}}},
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	old, err := service.CreateWorkspace(ctx, owner.ID, workspace.CreateWorkspaceInput{Name: "old volume workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := store.UpdateWorkspaceObservedState(ctx, old.ID, "stopped", "runtime-storage-test", "", "", old.CreatedAt, old.CreatedAt); err != nil {
		t.Fatalf("set stopped state: %v", err)
	}
	if err := service.DeleteWorkspace(ctx, owner.ID, old.ID); err != nil {
		t.Fatalf("delete workspace to create retained volume: %v", err)
	}
	volumes, err := store.ListRetainedWorkspaceVolumesForOwner(ctx, owner.ID)
	if err != nil || len(volumes) != 1 {
		t.Fatalf("retained volumes = %d, err=%v, want 1", len(volumes), err)
	}

	server, err := New(db, authService, service, nil, fake, Options{SessionLifetime: time.Hour, Store: store})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	sessionCookie := &http.Cookie{Name: "cows_session", Value: sessionToken}
	pageRequest := httptest.NewRequest(http.MethodGet, "/storage", nil)
	pageRequest.AddCookie(sessionCookie)
	pageRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(pageRecorder, pageRequest)
	csrfCookie := cookieByName(pageRecorder.Result().Cookies(), "cows_csrf")
	if pageRecorder.Code != http.StatusOK || csrfCookie == nil {
		t.Fatalf("storage page response: status=%d csrf=%#v", pageRecorder.Code, csrfCookie)
	}

	fake.removeVolumeErr = errors.New("simulated podman failure")
	deletePath := "/storage/volumes/" + old.ID + "/data/delete"
	form := url.Values{"csrf_token": {csrfCookie.Value}}
	deleteRequest := httptest.NewRequest(http.MethodPost, deletePath, strings.NewReader(form.Encode()))
	deleteRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteRequest.AddCookie(sessionCookie)
	deleteRequest.AddCookie(csrfCookie)
	deleteRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRecorder, deleteRequest)
	if deleteRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("delete-with-failing-removal status = %d, want 503, body = %s", deleteRecorder.Code, deleteRecorder.Body.String())
	}

	volumesAfter, err := store.ListRetainedWorkspaceVolumesForOwner(ctx, owner.ID)
	if err != nil || len(volumesAfter) != 1 {
		t.Fatalf("retained volumes after failed removal = %d, err=%v, want 1 (tombstone must survive)", len(volumesAfter), err)
	}
}
```

Check the exact route pattern (`/storage/volumes/{workspace_id}/{mount_name}/delete`) against `mux.HandleFunc` in `server.go` before finalizing `deletePath` — grep for `storageVolumeDelete` in the routes section to confirm the literal path template.

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/web/... -run TestStorageVolumeDeleteKeepsTombstoneWhenRemovalFails -v`
Expected: FAIL — current code returns `200`/redirect even though removal failed, and the tombstone is gone (0 retained volumes, not 1).

- [ ] **Step 4: Fix `storageVolumeDelete`**

Replace the full function body in `internal/web/server.go` with:

```go
func (s *Server) storageVolumeDelete(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if s.options.Store == nil {
		http.Error(w, "volume metadata is unavailable", http.StatusServiceUnavailable)
		return
	}
	workspaceID, mountName := r.PathValue("workspace_id"), r.PathValue("mount_name")
	volume, err := s.options.Store.FindRetainedWorkspaceVolume(r.Context(), workspaceID, mountName, user.ID)
	if errors.Is(err, repository.ErrNotFound) {
		if isHTMXRequest(r) {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load retained volume", http.StatusInternalServerError)
		return
	}
	// Remove the real volume before consuming its tombstone: if removal
	// fails, the tombstone must survive so the volume stays discoverable
	// and the user can retry, instead of silently leaking it while
	// reporting success (mirrors the already-correct ordering in
	// adminVolumeDelete).
	if volumeRuntime, ok := s.runtime.(runtime.VolumeRuntime); ok {
		if err := volumeRuntime.RemoveVolume(r.Context(), volume.VolumeName); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			http.Error(w, "the retained volume could not be removed", http.StatusServiceUnavailable)
			return
		}
	}
	if _, err := s.options.Store.ConsumeRetainedWorkspaceVolume(r.Context(), workspaceID, mountName, user.ID); err != nil && !errors.Is(err, repository.ErrNotFound) && !errors.Is(err, repository.ErrConflict) {
		http.Error(w, "failed to remove retained volume", http.StatusInternalServerError)
		return
	}
	s.recordStorageAudit(r.Context(), user.ID, "storage.volume_deleted", "volume", volume.VolumeName, map[string]string{"workspace_id": volume.WorkspaceID, "mount_name": volume.MountName})
	if isHTMXRequest(r) {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/storage", http.StatusSeeOther)
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/web/... -run TestStorageVolumeDeleteKeepsTombstoneWhenRemovalFails -v`
Expected: PASS

- [ ] **Step 6: Run the full web package test suite to check for regressions**

Run: `go test ./internal/web/... -v 2>&1 | tail -100`
Expected: all pass, in particular anything with "Storage" or "Volume" in its name.

- [ ] **Step 7: Run full build, vet, and test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add internal/web/server.go internal/web/server_test.go
git commit -m "$(cat <<'EOF'
Fix storageVolumeDelete leaking volumes on removal failure

storageVolumeDelete consumed (deleted) the retained-volume tombstone
before attempting RemoveVolume, discarded any error from it, and
recorded a false success audit event regardless. A failed removal
orphaned the named volume in Podman with no DB pointer while telling
the user it succeeded. Reorder to remove the real volume first and
surface a real failure, matching adminVolumeDelete's already-correct
pattern.
EOF
)"
```

---

### Task 4: Fix `DeleteRetainedDirectory`'s consume-before-remove ordering and add a forensic log entry

**Files:**
- Modify: `internal/workspace/mounts.go:465-489` (function `DeleteRetainedDirectory`)
- Test: `internal/workspace/mounts_test.go` (create this file if it does not already exist — check first with `ls internal/workspace/*_test.go`)

**Interfaces:**
- Consumes: `repository.Store.FindRetainedWorkspaceDirectory(ctx, workspaceID, ownerUserID) (domain.RetainedWorkspaceDirectory, error)` (existing), `repository.Store.ConsumeRetainedWorkspaceDirectory` (existing), `(*Service).logArchiveActivity(value domain.Workspace, action, status string, activityErr error) error` (existing, `mounts.go:340`).
- Produces: `DeleteRetainedDirectory`'s signature (`ctx, actorID, workspaceID string) error`) and its `repository.ErrNotFound` behavior on an unknown/foreign tombstone are unchanged; a filesystem removal failure now returns `ErrMountUnavailable` instead of silently succeeding, and every attempt now leaves a `directory_recovery_discarded` entry in `archive-activity.jsonl`.

- [ ] **Step 1: Write the failing test**

Create/append to `internal/workspace/mounts_test.go`:

```go
package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteRetainedDirectoryKeepsTombstoneWhenRemovalFails(t *testing.T) {
	ctx := context.Background()
	_, authService, adminID, store := testService(t)
	mountRoot := t.TempDir()
	archiveRoot := t.TempDir()
	service := NewWithRuntimeAndMountRoots(store, &lifecycleRuntime{}, mountRoot, archiveRoot)

	template, err := service.CreateTemplate(ctx, adminID, directoryTemplateInput("Discard Guard Template", "designs"))
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	owner := newReattachUser(t, authService, adminID, "discard-guard-owner")
	old, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "discard guard workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := service.DeleteWorkspace(ctx, owner.ID, old.ID); err != nil {
		t.Fatalf("delete workspace to create retained directory: %v", err)
	}
	directories, err := store.ListRetainedWorkspaceDirectoriesForOwner(ctx, owner.ID)
	if err != nil || len(directories) != 1 {
		t.Fatalf("retained directories = %d, err=%v, want 1", len(directories), err)
	}

	// Sabotage the archived directory so os.RemoveAll fails partway: make
	// the "designs" entry unreadable as a directory by replacing it with a
	// file of the same name is not viable (RemoveAll on a file always
	// succeeds); instead remove write permission on the archive's parent so
	// RemoveAll cannot unlink entries inside it. This is skipped when
	// running as root, which ignores permission bits.
	if os.Geteuid() == 0 {
		t.Skip("cannot force a permission-denied RemoveAll failure while running as root")
	}
	archivedPath := filepath.Join(archiveRoot, "cows-"+old.ID)
	if err := os.Chmod(archivedPath, 0o500); err != nil {
		t.Fatalf("sabotage archived directory permissions: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(archivedPath, 0o700) })

	if err := service.DeleteRetainedDirectory(ctx, owner.ID, old.ID); !errors.Is(err, ErrMountUnavailable) {
		t.Fatalf("DeleteRetainedDirectory with a failing removal error = %v, want ErrMountUnavailable", err)
	}

	directoriesAfter, err := store.ListRetainedWorkspaceDirectoriesForOwner(ctx, owner.ID)
	if err != nil || len(directoriesAfter) != 1 {
		t.Fatalf("retained directories after failed removal = %d, err=%v, want 1 (tombstone must survive)", len(directoriesAfter), err)
	}
}
```

If `internal/workspace/mounts_test.go` already exists with a `package workspace` declaration and its own imports, merge this test into it instead of creating a conflicting second file — read it first.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/workspace/... -run TestDeleteRetainedDirectoryKeepsTombstoneWhenRemovalFails -v`
Expected: FAIL — current code always returns `nil` regardless of the `os.RemoveAll` outcome, so the assertion on the returned error fails first.

- [ ] **Step 3: Fix `DeleteRetainedDirectory`**

Replace the function in `internal/workspace/mounts.go` (currently at lines 465-489) with:

```go
// DeleteRetainedDirectory permanently discards a retained-directory
// tombstone and its archived content (self-service, decision 0025). The
// physical removal is attempted before the tombstone is consumed: if it
// fails, the tombstone must survive so the archived data stays
// discoverable and the caller can retry, instead of silently leaking it
// on disk with no way back to it (mirrors the volume-deletion ordering
// fix applied to storageVolumeDelete).
func (s *Service) DeleteRetainedDirectory(ctx context.Context, actorID, workspaceID string) error {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return err
	}
	directory, err := s.store.FindRetainedWorkspaceDirectory(ctx, workspaceID, user.ID)
	if err != nil {
		return err
	}
	if s.mountArchiveRoot != "" {
		path, pathErr := filepath.Abs(filepath.Join(s.mountArchiveRoot, managedContainerName(directory.WorkspaceID)))
		if pathErr != nil {
			return ErrMountUnavailable
		}
		if err := os.RemoveAll(path); err != nil {
			_ = recordArchiveActivity(s.mountArchiveRoot, archiveActivity{Action: "directory_recovery_discarded", WorkspaceID: directory.WorkspaceID, ArchivePath: path, Status: "failed", Error: err.Error()})
			return ErrMountUnavailable
		}
		_ = recordArchiveActivity(s.mountArchiveRoot, archiveActivity{Action: "directory_recovery_discarded", WorkspaceID: directory.WorkspaceID, ArchivePath: path, Status: "succeeded"})
	}
	if _, err := s.store.ConsumeRetainedWorkspaceDirectory(ctx, workspaceID, user.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}
	return nil
}
```

This drops the old doc comment's "filesystem removal is best-effort" framing (that was the bug) and reuses the existing unexported `recordArchiveActivity`/`archiveActivity` helpers already used by `logArchiveActivity` elsewhere in this file, called directly since there's no `domain.Workspace` value here (only a `domain.RetainedWorkspaceDirectory`) — check the exact field names on `archiveActivity` (`internal/workspace/mounts.go:317-326`) match what's used above before finalizing.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/workspace/... -run TestDeleteRetainedDirectoryKeepsTombstoneWhenRemovalFails -v`
Expected: PASS (or SKIP if running as root — that's an acceptable outcome for this specific test, not a failure).

- [ ] **Step 5: Run the full reattach/storage test suite to check for regressions**

Run: `go test ./internal/workspace/... -v 2>&1 | tail -150`
Expected: all pass, in particular anything with "RetainedDirectory" or "Discard" in its name, and the `storageDirectoryDelete` HTTP path in `internal/web` (no code change there, but re-run `go test ./internal/web/...` too since it calls into this function).

- [ ] **Step 6: Run full build, vet, and test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add internal/workspace/mounts.go internal/workspace/mounts_test.go
git commit -m "$(cat <<'EOF'
Fix DeleteRetainedDirectory leaking archives on removal failure

DeleteRetainedDirectory consumed the tombstone before attempting
os.RemoveAll and discarded its error, matching the same anti-pattern
just fixed for volumes - except directories have no admin recovery
path at all, so a failed removal here was permanently unrecoverable.
Reorder to remove first and surface failures, and add the forensic
archive-activity log entry every other destructive step in this
package already writes.
EOF
)"
```

---

### Task 5: Add an administrator recovery page for retained directories

**Files:**
- Modify: `internal/repository/repository.go` (interface method), `internal/repository/sqlite/store.go` (implementation), `internal/web/server.go` (types, handlers, route registrations), `web/templates/layouts/base.html` (nav link)
- Create: `web/templates/pages/admin-directories.html`
- Test: `internal/web/server_test.go`

**Interfaces:**
- Consumes: `domain.RetainedWorkspaceDirectory` (existing), the existing `retained_workspace_directories` table/`scanRetainedWorkspaceDirectory` helper (`internal/repository/sqlite/store.go`), `(*Service).OpenRetainedDirectoryZip` — **not reused directly** since it is owner-scoped self-service only per its own doc comment; the admin path needs its own unscoped read.
- Produces: `repository.Store.ListAllRetainedWorkspaceDirectories(ctx) ([]domain.RetainedWorkspaceDirectory, error)` and `repository.Store.FindRetainedWorkspaceDirectoryByID(ctx, workspaceID) (domain.RetainedWorkspaceDirectory, error)` (new interface methods + sqlite implementations), new HTTP routes `GET /admin/directories`, `GET /admin/directories/{workspace_id}/download.zip`, `POST /admin/directories/{workspace_id}/delete`, new type `retainedDirectoryView` in `server.go`, new `pageData.RetainedDirectories []retainedDirectoryView` field.

- [ ] **Step 1: Add the two new repository methods to the interface**

In `internal/repository/repository.go`, find this existing line (around line 129):

```go
	ListRetainedWorkspaceDirectoriesForOwner(ctx context.Context, ownerUserID string) ([]domain.RetainedWorkspaceDirectory, error)
```

Add directly above it:

```go
	// ListAllRetainedWorkspaceDirectories is the administrator-recovery
	// counterpart of ListRetainedWorkspaceDirectoriesForOwner (decision 0022):
	// it returns every retained directory regardless of owner, including
	// ones whose owning user account no longer exists.
	ListAllRetainedWorkspaceDirectories(ctx context.Context) ([]domain.RetainedWorkspaceDirectory, error)
```

And directly above `ConsumeRetainedWorkspaceDirectory` (around line 132), add:

```go
	// FindRetainedWorkspaceDirectoryByID is the administrator-recovery
	// counterpart of FindRetainedWorkspaceDirectory: it looks a tombstone up
	// by workspace ID alone, without an owner check, since an administrator
	// must be able to reach a directory whose owning user was deleted.
	FindRetainedWorkspaceDirectoryByID(ctx context.Context, workspaceID string) (domain.RetainedWorkspaceDirectory, error)
```

- [ ] **Step 2: Implement both methods in the sqlite store**

In `internal/repository/sqlite/store.go`, directly above `func (s *Store) ListRetainedWorkspaceDirectoriesForOwner` (around line 937), add:

```go
func (s *Store) ListAllRetainedWorkspaceDirectories(ctx context.Context) ([]domain.RetainedWorkspaceDirectory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workspace_id, owner_user_id, template_id, template_name, workspace_name,
		archive_path, mounts_json, retained_at FROM retained_workspace_directories ORDER BY retained_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list all retained workspace directories: %w", err)
	}
	defer rows.Close()
	directories := make([]domain.RetainedWorkspaceDirectory, 0)
	for rows.Next() {
		directory, err := scanRetainedWorkspaceDirectory(rows)
		if err != nil {
			return nil, err
		}
		directories = append(directories, directory)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate all retained workspace directories: %w", err)
	}
	return directories, nil
}
```

And directly above `func (s *Store) FindRetainedWorkspaceDirectory` (the owner-scoped one, around line 973), add:

```go
// FindRetainedWorkspaceDirectoryByID is the administrator-recovery
// counterpart of FindRetainedWorkspaceDirectory: no owner_user_id filter,
// since an administrator must be able to reach a tombstone whose owning
// user account has since been deleted.
func (s *Store) FindRetainedWorkspaceDirectoryByID(ctx context.Context, workspaceID string) (domain.RetainedWorkspaceDirectory, error) {
	row := s.db.QueryRowContext(ctx, `SELECT workspace_id, owner_user_id, template_id, template_name, workspace_name,
		archive_path, mounts_json, retained_at FROM retained_workspace_directories WHERE workspace_id = ?`, workspaceID)
	directory, err := scanRetainedWorkspaceDirectory(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.RetainedWorkspaceDirectory{}, repository.ErrNotFound
		}
		return domain.RetainedWorkspaceDirectory{}, err
	}
	return directory, nil
}
```

- [ ] **Step 3: Verify the interface is satisfied**

Run: `go build ./...`
Expected: success. If it fails because some other type also implements `repository.Store` (check for test fakes/mocks with `grep -rln "repository.Store" internal/`), add the same two methods there too before proceeding.

- [ ] **Step 4: Add the `retainedDirectoryView` type and `pageData` field**

In `internal/web/server.go`, directly below the existing `retainedVolumeView` type (around line 262), add:

```go
type retainedDirectoryView struct {
	domain.RetainedWorkspaceDirectory
	CSRFToken string
	// RowError is a transient, request-scoped message shown only in the
	// just-swapped-in row after a rejected dynamic row action.
	RowError string
}
```

In the `pageData` struct, directly below the existing `RetainedVolumes             []retainedVolumeView` field, add:

```go
	RetainedDirectories         []retainedDirectoryView
```

- [ ] **Step 5: Add the three handlers**

In `internal/web/server.go`, directly below the closing brace of `adminVolumes` (the function ending around line 3197), add:

```go
func (s *Server) adminDirectories(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	if s.options.Store == nil {
		http.Error(w, "directory metadata is unavailable", http.StatusServiceUnavailable)
		return
	}
	values, err := s.options.Store.ListAllRetainedWorkspaceDirectories(r.Context())
	if err != nil {
		http.Error(w, "failed to load retained directories", http.StatusInternalServerError)
		return
	}
	csrfToken := s.ensureCSRF(w, r)
	views := make([]retainedDirectoryView, 0, len(values))
	for _, value := range values {
		views = append(views, retainedDirectoryView{RetainedWorkspaceDirectory: value, CSRFToken: csrfToken})
	}
	s.render(w, http.StatusOK, "admin-directories-page", pageData{Title: "Retained directories | COWS", User: &user, CSRFToken: csrfToken, RetainedDirectories: views})
}

func (s *Server) adminDirectoryDownload(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	if s.options.Store == nil {
		http.Error(w, "directory metadata is unavailable", http.StatusServiceUnavailable)
		return
	}
	workspaceID := r.PathValue("workspace_id")
	directory, err := s.options.Store.FindRetainedWorkspaceDirectoryByID(r.Context(), workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load retained directory", http.StatusInternalServerError)
		return
	}
	reader, name, err := s.workspace.OpenRetainedDirectoryZipByID(r.Context(), workspaceID)
	if err != nil {
		http.Error(w, "the retained directory could not be read", http.StatusServiceUnavailable)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
	s.recordAdminAudit(r.Context(), user.ID, "directory.recovery_downloaded", directory.WorkspaceID, map[string]string{"workspace_id": directory.WorkspaceID, "owner_user_id": directory.OwnerUserID})
	_, _ = io.Copy(w, reader)
}

func (s *Server) adminDirectoryDelete(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if s.options.Store == nil {
		http.Error(w, "directory metadata is unavailable", http.StatusServiceUnavailable)
		return
	}
	workspaceID := r.PathValue("workspace_id")
	directory, err := s.options.Store.FindRetainedWorkspaceDirectoryByID(r.Context(), workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "failed to load retained directory", http.StatusInternalServerError)
		return
	}
	rowErrorText := ""
	if err := s.workspace.DeleteRetainedDirectoryByID(r.Context(), workspaceID); err != nil {
		rowErrorText = "The retained directory could not be removed."
	} else {
		s.recordAdminAudit(r.Context(), user.ID, "directory.recovery_removed", directory.WorkspaceID, map[string]string{"workspace_id": directory.WorkspaceID, "owner_user_id": directory.OwnerUserID})
	}
	if isHTMXRequest(r) {
		if rowErrorText == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		view := retainedDirectoryView{RetainedWorkspaceDirectory: directory, RowError: rowErrorText, CSRFToken: s.ensureCSRF(w, r)}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.templates.ExecuteTemplate(w, "admin-directory-row", view)
		return
	}
	if rowErrorText != "" {
		http.Error(w, rowErrorText, http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, "/admin/directories", http.StatusSeeOther)
}
```

This calls two new `workspace.Service` methods (`OpenRetainedDirectoryZipByID`, `DeleteRetainedDirectoryByID`) that don't exist yet — add them next.

- [ ] **Step 6: Add the unscoped service-layer methods `OpenRetainedDirectoryZipByID` and `DeleteRetainedDirectoryByID`**

In `internal/workspace/mounts.go`, the existing `OpenRetainedDirectoryZip` currently reads (verify this matches before editing — line numbers may have shifted slightly from earlier tasks):

```go
func (s *Service) OpenRetainedDirectoryZip(ctx context.Context, actorID, workspaceID string) (io.ReadCloser, string, error) {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return nil, "", err
	}
	directory, err := s.store.FindRetainedWorkspaceDirectory(ctx, workspaceID, user.ID)
	if err != nil {
		return nil, "", err
	}
	if s.mountArchiveRoot == "" {
		return nil, "", ErrMountUnavailable
	}
	root, err := os.OpenRoot(s.mountArchiveRoot)
	if err != nil {
		return nil, "", ErrMountUnavailable
	}
	entryName := managedContainerName(directory.WorkspaceID)
	if _, err := root.Lstat(entryName); err != nil {
		root.Close()
		return nil, "", ErrMountUnavailable
	}
	reader, writer := io.Pipe()
	go func() {
		defer root.Close()
		zipWriter := zip.NewWriter(writer)
		state := archive.State{}
		walkErr := archive.WriteZip(ctx, root, entryName, zipWriter, &state)
		closeErr := zipWriter.Close()
		if walkErr != nil {
			_ = writer.CloseWithError(walkErr)
			return
		}
		_ = writer.CloseWithError(closeErr)
	}()
	name := directory.WorkspaceName
	if name == "" {
		name = directory.WorkspaceID
	}
	return reader, name, nil
}
```

Replace it with these three functions (the first two are the same function split at the point where the owner check ends and the shared streaming logic begins; the third is the new administrator entry point):

```go
func (s *Service) OpenRetainedDirectoryZip(ctx context.Context, actorID, workspaceID string) (io.ReadCloser, string, error) {
	user, err := s.requireActor(ctx, actorID)
	if err != nil {
		return nil, "", err
	}
	directory, err := s.store.FindRetainedWorkspaceDirectory(ctx, workspaceID, user.ID)
	if err != nil {
		return nil, "", err
	}
	return s.streamRetainedDirectoryZip(ctx, directory)
}

// streamRetainedDirectoryZip is the shared streaming logic behind
// OpenRetainedDirectoryZip (owner-scoped, self-service) and
// OpenRetainedDirectoryZipByID (unscoped, administrator recovery) - the
// tombstone lookup differs between the two callers, everything after it
// does not.
func (s *Service) streamRetainedDirectoryZip(ctx context.Context, directory domain.RetainedWorkspaceDirectory) (io.ReadCloser, string, error) {
	if s.mountArchiveRoot == "" {
		return nil, "", ErrMountUnavailable
	}
	root, err := os.OpenRoot(s.mountArchiveRoot)
	if err != nil {
		return nil, "", ErrMountUnavailable
	}
	entryName := managedContainerName(directory.WorkspaceID)
	if _, err := root.Lstat(entryName); err != nil {
		root.Close()
		return nil, "", ErrMountUnavailable
	}
	reader, writer := io.Pipe()
	go func() {
		defer root.Close()
		zipWriter := zip.NewWriter(writer)
		state := archive.State{}
		walkErr := archive.WriteZip(ctx, root, entryName, zipWriter, &state)
		closeErr := zipWriter.Close()
		if walkErr != nil {
			_ = writer.CloseWithError(walkErr)
			return
		}
		_ = writer.CloseWithError(closeErr)
	}()
	name := directory.WorkspaceName
	if name == "" {
		name = directory.WorkspaceID
	}
	return reader, name, nil
}

// OpenRetainedDirectoryZipByID is the administrator-recovery counterpart of
// OpenRetainedDirectoryZip: no ownership check, since an administrator must
// be able to recover a directory whose owning user account was deleted
// (decision 0022's recovery guarantee, extended to directories by decision
// 0025's directory tombstones).
func (s *Service) OpenRetainedDirectoryZipByID(ctx context.Context, workspaceID string) (io.ReadCloser, string, error) {
	directory, err := s.store.FindRetainedWorkspaceDirectoryByID(ctx, workspaceID)
	if err != nil {
		return nil, "", err
	}
	return s.streamRetainedDirectoryZip(ctx, directory)
}
```

Directly below `DeleteRetainedDirectory` (as fixed in Task 4), add:

```go
// DeleteRetainedDirectoryByID is the administrator-recovery counterpart of
// DeleteRetainedDirectory: no ownership check, and it removes the real
// archived content before consuming the tombstone for the same reason
// described there.
func (s *Service) DeleteRetainedDirectoryByID(ctx context.Context, workspaceID string) error {
	directory, err := s.store.FindRetainedWorkspaceDirectoryByID(ctx, workspaceID)
	if err != nil {
		return err
	}
	if s.mountArchiveRoot != "" {
		path, pathErr := filepath.Abs(filepath.Join(s.mountArchiveRoot, managedContainerName(directory.WorkspaceID)))
		if pathErr != nil {
			return ErrMountUnavailable
		}
		if err := os.RemoveAll(path); err != nil {
			_ = recordArchiveActivity(s.mountArchiveRoot, archiveActivity{Action: "directory_recovery_discarded", WorkspaceID: directory.WorkspaceID, ArchivePath: path, Status: "failed", Error: err.Error()})
			return ErrMountUnavailable
		}
		_ = recordArchiveActivity(s.mountArchiveRoot, archiveActivity{Action: "directory_recovery_discarded", WorkspaceID: directory.WorkspaceID, ArchivePath: path, Status: "succeeded"})
	}
	result := s.store.DeleteRetainedWorkspaceDirectory(ctx, workspaceID)
	if result != nil && !errors.Is(result, repository.ErrNotFound) {
		return result
	}
	return nil
}
```

Note this calls `DeleteRetainedWorkspaceDirectory` (the unconditional-by-ID delete, already existing at `internal/repository/repository.go:130`), not `ConsumeRetainedWorkspaceDirectory` (which requires an owner match) — correct, since this is the unscoped admin path.

- [ ] **Step 7: Register the three routes**

In `internal/web/server.go`, directly below the existing line `mux.HandleFunc("GET /admin/volumes/{workspace_id}/{mount_name}/download.zip", s.adminVolumeDownload)` (around line 614) and its sibling `POST .../delete` registration, add:

```go
	mux.HandleFunc("GET /admin/directories", s.adminDirectories)
	mux.HandleFunc("GET /admin/directories/{workspace_id}/download.zip", s.adminDirectoryDownload)
	mux.HandleFunc("POST /admin/directories/{workspace_id}/delete", s.adminDirectoryDelete)
```

- [ ] **Step 8: Add the nav link**

In `web/templates/layouts/base.html`, directly below the existing line:

```html
            <a class="nav-menu-item" href="/admin/volumes">Volumes</a>
```

add:

```html
            <a class="nav-menu-item" href="/admin/directories">Directories</a>
```

- [ ] **Step 9: Create the admin-directories template**

Create `web/templates/pages/admin-directories.html`, mirroring `web/templates/pages/admin-volumes.html`'s structure exactly (read that file first — it defines `admin-volumes-page` and `admin-volume-row` templates):

```html
{{ define "admin-directories-page" }}
{{ template "base-start" . }}
<section class="intro" aria-labelledby="directories-title">
  <p class="eyebrow">Administration</p>
  <h1 id="directories-title">Retained directories</h1>
  <p class="lede">Explicitly deleted workspaces retain archived directory mounts for manual recovery, including ones whose owning user account has since been deleted. These records do not grant user access.</p>
</section>
<section class="panel table-panel">
  <div class="table-scroll"><table><thead><tr><th>Former workspace</th><th>Owner</th><th>Retained</th><th>Actions</th></tr></thead><tbody>
  {{ range .RetainedDirectories }}{{ template "admin-directory-row" . }}{{ else }}<tr><td colspan="4" class="muted">No retained directories.</td></tr>{{ end }}
  </tbody></table></div>
</section>
{{ template "base-end" . }}
{{ end }}

{{ define "admin-directory-row" }}
<tr id="admin-directory-row-{{ .WorkspaceID }}">
  <td>{{ .WorkspaceName }}<br><span class="table-subtext">{{ .TemplateName }}</span></td>
  <td><code>{{ .OwnerUserID }}</code></td>
  <td>{{ .RetainedAt.Format "2006-01-02 15:04 UTC" }}</td>
  <td>{{ if .RowError }}<p class="table-subtext status-disabled">{{ .RowError }}</p>{{ end }}<div class="table-actions"><a class="table-action access-action button-link" href="/admin/directories/{{ .WorkspaceID }}/download.zip">Download ZIP</a><form method="post" action="/admin/directories/{{ .WorkspaceID }}/delete" hx-post="/admin/directories/{{ .WorkspaceID }}/delete" hx-target="#admin-directory-row-{{ .WorkspaceID }}" hx-swap="outerHTML" hx-disabled-elt="find button" hx-confirm="Remove this retained directory permanently?"><input type="hidden" name="csrf_token" value="{{ .CSRFToken }}"><button class="table-action danger-action" type="submit"><span class="action-label">Remove</span><span class="action-pending">Removing&hellip;</span></button></form></div></td>
</tr>
{{ end }}
```

`.OwnerUserID` is shown as a raw ID rather than a resolved username deliberately: the owning user may no longer exist, so there is nothing to join against.

- [ ] **Step 10: Write the failing test, then verify the whole flow works**

Add to `internal/web/server_test.go`, closely mirroring the pattern from `TestStorageVolumeDeleteKeepsTombstoneWhenRemovalFails` in Task 3 but for a directory template, asserting the admin recovery flow works **for a directory whose owning user has been deleted**:

```go
func TestAdminDirectoriesRecoversStorageAfterOwnerDeleted(t *testing.T) {
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
	admin, adminToken, err := authService.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	target, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "orphan-owner", Password: "another correct password", Role: domain.RoleUser})
	if err != nil {
		t.Fatalf("create target user: %v", err)
	}

	fake := &storageTestRuntime{terminalRuntime: newTerminalRuntime()}
	mountRoot, archiveRoot := t.TempDir(), t.TempDir()
	service := workspace.NewWithRuntimeAndMountRoots(store, fake, mountRoot, archiveRoot)
	template, err := service.CreateTemplate(ctx, admin.ID, workspace.TemplateInput{
		Name: "Admin Directory Recovery Template", ImageReference: "registry.example/research:1", DefaultCPUMillis: 1000,
		MaxCPUMillis: 2000, DefaultMemoryBytes: 2 << 30, MaxMemoryBytes: 4 << 30,
		DefaultStorageBytes: 10 << 30, InitialConnectionTimeoutSeconds: 3600, StoppedRetentionSeconds: 3600,
		AccessMethods: []domain.AccessMethod{domain.AccessFiles}, AllowedRoles: []domain.Role{domain.RoleUser}, Enabled: true,
		Configuration: domain.TemplateConfiguration{Mounts: []domain.TemplateMount{{Name: "designs", Type: domain.TemplateMountDirectory, ContainerPath: "/designs", FileManager: true}}},
	})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	old, err := service.CreateWorkspace(ctx, target.ID, workspace.CreateWorkspaceInput{Name: "orphaned directory workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := service.DeleteWorkspace(ctx, target.ID, old.ID); err != nil {
		t.Fatalf("delete workspace to create retained directory: %v", err)
	}
	if err := authService.SetUserDisabled(ctx, admin.ID, target.ID, true); err != nil {
		t.Fatalf("disable target: %v", err)
	}
	if err := authService.DeleteUser(ctx, admin.ID, target.ID); err != nil {
		t.Fatalf("delete target (must succeed: they no longer own any workspace rows): %v", err)
	}

	server, err := New(db, authService, service, nil, fake, Options{SessionLifetime: time.Hour, Store: store})
	if err != nil {
		t.Fatalf("create web server: %v", err)
	}
	sessionCookie := &http.Cookie{Name: "cows_session", Value: adminToken}
	pageRequest := httptest.NewRequest(http.MethodGet, "/admin/directories", nil)
	pageRequest.AddCookie(sessionCookie)
	pageRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(pageRecorder, pageRequest)
	if pageRecorder.Code != http.StatusOK || !strings.Contains(pageRecorder.Body.String(), "orphaned directory workspace") {
		t.Fatalf("admin directories page status=%d body=%s", pageRecorder.Code, pageRecorder.Body.String())
	}
	csrfCookie := cookieByName(pageRecorder.Result().Cookies(), "cows_csrf")
	if csrfCookie == nil {
		t.Fatalf("expected a csrf cookie")
	}

	downloadRequest := httptest.NewRequest(http.MethodGet, "/admin/directories/"+old.ID+"/download.zip", nil)
	downloadRequest.AddCookie(sessionCookie)
	downloadRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(downloadRecorder, downloadRequest)
	if downloadRecorder.Code != http.StatusOK || downloadRecorder.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download status=%d content-type=%q", downloadRecorder.Code, downloadRecorder.Header().Get("Content-Type"))
	}

	form := url.Values{"csrf_token": {csrfCookie.Value}}
	deleteRequest := httptest.NewRequest(http.MethodPost, "/admin/directories/"+old.ID+"/delete", strings.NewReader(form.Encode()))
	deleteRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteRequest.AddCookie(sessionCookie)
	deleteRequest.AddCookie(csrfCookie)
	deleteRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(deleteRecorder, deleteRequest)
	if deleteRecorder.Code != http.StatusSeeOther {
		t.Fatalf("delete status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if directories, err := store.ListAllRetainedWorkspaceDirectories(ctx); err != nil || len(directories) != 0 {
		t.Fatalf("retained directories after admin delete = %d, err=%v, want 0", len(directories), err)
	}
}
```

- [ ] **Step 11: Run the test to verify it fails, then implement until it passes**

Run: `go test ./internal/web/... -run TestAdminDirectoriesRecoversStorageAfterOwnerDeleted -v`
Expected first: a compile error or 404s, since the routes/handlers/template don't exist until steps 5-9 above are done. Once all steps are complete:
Run: `go test ./internal/web/... -run TestAdminDirectoriesRecoversStorageAfterOwnerDeleted -v`
Expected: PASS. This test is the concrete proof that Task 5 closes audit finding #2 (permanently orphaned directories after a user is deleted).

- [ ] **Step 12: Run the full test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass. Also run `gofmt -l internal/web/server.go internal/workspace/mounts.go internal/repository/repository.go internal/repository/sqlite/store.go` — expect no output.

- [ ] **Step 13: Manually verify in a running instance**

Per this repo's `run-cows` skill: build `go build -o bin/cows ./cmd/cows`, start a test instance, log in as an admin, and confirm `/admin/directories` renders and the nav link works. This is a UI-surfacing change, not just a backend fix, so exercise it visually once before committing.

- [ ] **Step 14: Commit**

```bash
git add internal/repository/repository.go internal/repository/sqlite/store.go internal/web/server.go internal/workspace/mounts.go web/templates/layouts/base.html web/templates/pages/admin-directories.html internal/web/server_test.go
git commit -m "$(cat <<'EOF'
Add administrator recovery for retained directories

Retained-directory tombstones are scoped to owner_user_id, and every
read/download/delete path for them required actor == owner. Once an
admin deleted that user (a routine, expected action, not a race), the
tombstone - and the real archived files it pointed at - became
permanently unreachable by any code path: unlike named volumes, there
was no /admin/directories equivalent. Add the same
list-all/download/delete recovery surface volumes already have.
EOF
)"
```

---

### Task 6: Regression test proving `DeleteWorkspace` survives a late `DeleteWorkspaceRetainingStorage` failure

**Files:**
- Test: `internal/workspace/lifecycle_test.go`

**Interfaces:**
- Consumes: `repository.Store` (existing interface), `sqlite.Store` (existing concrete type returned by `testService`), `NewWithRuntimeAndMountRoots` (existing).
- Produces: a new unexported test-only type `failingDeleteStore` (embeds `repository.Store`, overrides `DeleteWorkspaceRetainingStorage`) — used only within this test file, no production code changes.

This task is pure verification: the audit concluded `DeleteWorkspace`'s design (archive-then-single-transaction-DB-write, both idempotent on retry) is already sound, but nothing proves it. No source fix is expected; if this test reveals an actual bug, stop and report it rather than guessing a fix — that would mean the audit's conclusion was wrong and needs re-analysis first.

- [ ] **Step 1: Write the fault-injecting store wrapper and the test**

Add to `internal/workspace/lifecycle_test.go`:

```go
type failingDeleteStore struct {
	repository.Store
	failRetainingStorage bool
}

func (s *failingDeleteStore) DeleteWorkspaceRetainingStorage(ctx context.Context, id string, volumes []domain.RetainedWorkspaceVolume, directory *domain.RetainedWorkspaceDirectory) error {
	if s.failRetainingStorage {
		return errors.New("injected failure")
	}
	return s.Store.DeleteWorkspaceRetainingStorage(ctx, id, volumes, directory)
}

func TestDeleteWorkspaceSurvivesRetainingStorageFailureAndSelfHealsOnRetry(t *testing.T) {
	ctx := context.Background()
	_, authService, adminID, sqliteStore := testService(t)
	failing := &failingDeleteStore{Store: sqliteStore}
	mountRoot, archiveRoot := t.TempDir(), t.TempDir()
	fake := &lifecycleRuntime{}
	service := NewWithRuntimeAndMountRoots(failing, fake, mountRoot, archiveRoot)

	template, err := service.CreateTemplate(ctx, adminID, directoryTemplateInput("Delete Survival Template", "designs"))
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	owner := newReattachUser(t, authService, adminID, "delete-survival-owner")
	value, err := service.CreateWorkspace(ctx, owner.ID, CreateWorkspaceInput{Name: "delete survival workspace", TemplateID: template.ID})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := sqliteStore.UpdateWorkspaceObservedState(ctx, value.ID, "stopped", "runtime-delete-survival", "", "", value.CreatedAt, value.CreatedAt); err != nil {
		t.Fatalf("set stopped state: %v", err)
	}
	mountDir := filepath.Join(mountRoot, "cows-"+value.ID, "designs")
	if err := os.WriteFile(filepath.Join(mountDir, "notes.txt"), []byte("data that must survive"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	failing.failRetainingStorage = true
	if err := service.DeleteWorkspace(ctx, owner.ID, value.ID); err == nil {
		t.Fatalf("expected DeleteWorkspace to fail when DeleteWorkspaceRetainingStorage is injected to fail")
	}

	// The workspace row must still exist (nothing rolled it back), and the
	// archived file must be sitting at the archive path (archiveMountDirectories
	// already ran and is not undone), not lost.
	if _, err := service.GetWorkspace(ctx, owner.ID, value.ID); err != nil {
		t.Fatalf("workspace row should still exist after the failed delete for a retry: %v", err)
	}
	archivedFile := filepath.Join(archiveRoot, "cows-"+value.ID, "designs", "notes.txt")
	content, err := os.ReadFile(archivedFile)
	if err != nil {
		t.Fatalf("archived file should exist after the failed delete: %v", err)
	}
	if string(content) != "data that must survive" {
		t.Fatalf("archived content = %q, want original content", content)
	}

	// Retrying must succeed and produce the expected tombstone: proves the
	// partial failure self-heals rather than wedging the workspace.
	failing.failRetainingStorage = false
	if err := service.DeleteWorkspace(ctx, owner.ID, value.ID); err != nil {
		t.Fatalf("retry delete: %v", err)
	}
	if _, err := service.GetWorkspace(ctx, owner.ID, value.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("workspace should be gone after the successful retry, err = %v", err)
	}
	directories, err := sqliteStore.ListRetainedWorkspaceDirectoriesForOwner(ctx, owner.ID)
	if err != nil || len(directories) != 1 {
		t.Fatalf("retained directories after successful retry = %d, err=%v, want 1", len(directories), err)
	}
	restoredContent, err := os.ReadFile(filepath.Join(directories[0].ArchivePath, "designs", "notes.txt"))
	if err != nil || string(restoredContent) != "data that must survive" {
		t.Fatalf("final archived content = %q, err=%v, want original content", restoredContent, err)
	}
}
```

Check `lifecycle_test.go`'s existing imports (`context`, `errors`, `os`, `path/filepath`, `testing`, `repository`, `domain`) and add any missing ones; check whether `directoryTemplateInput` and `newReattachUser` (defined in `reattach_test.go`) are visible here — they are, since both files share `package workspace`.

- [ ] **Step 2: Run the test**

Run: `go test ./internal/workspace/... -run TestDeleteWorkspaceSurvivesRetainingStorageFailureAndSelfHealsOnRetry -v`
Expected: PASS on the first attempt, since (per the audit) `DeleteWorkspace`'s design is already correct — this step locks that guarantee in with a test rather than fixing a bug. **If it fails**, do not patch around it — stop, re-read `DeleteWorkspace` (`internal/workspace/workspace.go:786-918`) and `DeleteWorkspaceRetainingStorage` (`internal/repository/sqlite/store.go:770-812`) end to end, and report exactly what broke the audit's "already atomic and idempotent" conclusion before writing any fix.

- [ ] **Step 3: Run full build, vet, and test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add internal/workspace/lifecycle_test.go
git commit -m "$(cat <<'EOF'
Add regression test for DeleteWorkspace surviving a late DB failure

DeleteWorkspace's container-present branch archives mount directories
to disk, then performs its only fallible-after-irreversible-step DB
write (DeleteWorkspaceRetainingStorage) as a single transaction. The
audit that found the reattachment data-loss bug (6cb4195) concluded
this path was already safe and retry-idempotent, but nothing proved
it. Lock the guarantee in with a fault-injecting store wrapper.
EOF
)"
```

---

## Self-review notes (for the plan author, not the implementer)

- **Spec coverage**: all 6 audit findings have a task — #1 (multi-mount restore) → Task 1, #2 (orphaned directories after user delete) → Task 5, #3 (volume delete leak) → Task 3, #4 (directory delete leak) → Task 4, #5 (no `DeleteUser` guard) → Task 2, #6 (untested but sound `DeleteWorkspace`) → Task 6.
- **Task ordering**: Task 2 (small, fully independent) is placed early as a quick win; Tasks 3 and 4 (independent of each other and of Task 1/2's files) come next; Task 5 is the largest and touches the most files, placed after the smaller wins are banked; Task 6 is pure verification and has no risk of breaking anything, placed last.
- **File-conflict risk**: Tasks 1, 2, 3, 6 touch disjoint file sets. Task 4 and Task 5 both touch `internal/workspace/mounts.go` (Task 5 adds `OpenRetainedDirectoryZipByID`/`DeleteRetainedDirectoryByID` next to Task 4's already-fixed `DeleteRetainedDirectory`) — this is why Task 5 is sequenced strictly after Task 4, not run concurrently with it. If using `subagent-driven-development`, dispatch tasks in the numbered order given here, one at a time, not in parallel.
