//go:build e2e

package e2e

import (
	"archive/zip"
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceLifecycleFilesAndTerminal(t *testing.T) {
	image := e2eImage(t)
	inst := newInstance(t, instanceOptions{SeedUsers: 1})
	rec := newRecorder(t.Name(), image, 1)
	t.Cleanup(func() { rec.write(t) })
	user := inst.userClient(t, 0)
	name := "lifecycle-" + randomSuffix(t)

	started := time.Now()
	creation := user.createWorkspace(name, inst.templateID, nil)
	rec.record("workspace.create", user.username, started, statusError(creation, 200), false, name)
	expectStatus(t, creation, 200, "create workspace")
	id := user.workspaceID(name)

	started = time.Now()
	startedResult := user.startWorkspace(id)
	rec.record("workspace.start", user.username, started, statusError(startedResult, 200), false, id)
	expectStatus(t, startedResult, 200, "start workspace")
	user.waitWorkspaceStatus(id, "running", 2*time.Minute)

	// The same logical file name is uploaded through both a named volume and
	// a COWS-managed directory mount, then read back through the browser
	// download route. This exercises the rootless file helper, not a test-only
	// filesystem shim.
	volumeContent := []byte("volume content " + id)
	directoryContent := []byte("directory content " + id)

	started = time.Now()
	volumeUpload := user.uploadFile(id, "data", ".", "note.txt", volumeContent)
	rec.record("file.upload.volume", user.username, started, statusError(volumeUpload, 200), false, "data/note.txt")
	expectStatus(t, volumeUpload, 200, "upload volume file")

	started = time.Now()
	directoryUpload := user.uploadFile(id, "home", ".", "note.txt", directoryContent)
	rec.record("file.upload.directory", user.username, started, statusError(directoryUpload, 200), false, "home/note.txt")
	expectStatus(t, directoryUpload, 200, "upload directory file")

	listing := user.fileFragment(id, "data", ".")
	expectStatus(t, listing, 200, "list volume mount")
	expectContains(t, listing.Body, "note.txt", "volume listing")
	listing = user.fileFragment(id, "home", ".")
	expectStatus(t, listing, 200, "list directory mount")
	expectContains(t, listing.Body, "note.txt", "directory listing")

	started = time.Now()
	gotVolume, volumeDownload := user.downloadFile(id, "data", "note.txt")
	rec.record("file.download.volume", user.username, started, statusError(volumeDownload, 200), false, "data/note.txt")
	expectStatus(t, volumeDownload, 200, "download volume file")
	if string(gotVolume) != string(volumeContent) {
		t.Fatalf("volume download = %q, want %q", gotVolume, volumeContent)
	}

	started = time.Now()
	gotDirectory, directoryDownload := user.downloadFile(id, "home", "note.txt")
	rec.record("file.download.directory", user.username, started, statusError(directoryDownload, 200), false, "home/note.txt")
	expectStatus(t, directoryDownload, 200, "download directory file")
	if string(gotDirectory) != string(directoryContent) {
		t.Fatalf("directory download = %q, want %q", gotDirectory, directoryContent)
	}

	// Directory, nested upload, rename, ZIP, and delete.
	started = time.Now()
	mkdir := user.makeDirectory(id, "data", ".", "nested")
	rec.record("file.mkdir", user.username, started, statusError(mkdir, 200), false, "data/nested")
	expectStatus(t, mkdir, 200, "create directory")

	started = time.Now()
	nestedUpload := user.uploadFile(id, "data", "nested", "inner.txt", []byte("nested content"))
	rec.record("file.upload.nested", user.username, started, statusError(nestedUpload, 200), false, "data/nested/inner.txt")
	expectStatus(t, nestedUpload, 200, "upload nested file")

	started = time.Now()
	renamed := user.renamePath(id, "data", "nested", "inner.txt", "renamed.txt")
	rec.record("file.rename", user.username, started, statusError(renamed, 200), false, "data/nested/renamed.txt")
	expectStatus(t, renamed, 200, "rename nested file")

	started = time.Now()
	renamedContent, renamedDownload := user.downloadFile(id, "data", "nested/renamed.txt")
	rec.record("file.download.renamed", user.username, started, statusError(renamedDownload, 200), false, "data/nested/renamed.txt")
	expectStatus(t, renamedDownload, 200, "download renamed file")
	if string(renamedContent) != "nested content" {
		t.Fatalf("renamed file content = %q", renamedContent)
	}

	started = time.Now()
	archive, zipResult := user.downloadZip(id, "data", ".")
	rec.record("file.download.zip", user.username, started, statusError(zipResult, 200), false, "data/")
	expectStatus(t, zipResult, 200, "download ZIP")
	if !zipContains(archive, "note.txt") || !zipContains(archive, "nested/renamed.txt") {
		t.Fatalf("ZIP does not contain uploaded entries: %v", zipNames(archive))
	}

	started = time.Now()
	deleted := user.deletePath(id, "data", "nested/renamed.txt")
	rec.record("file.delete", user.username, started, statusError(deleted, 200), false, "data/nested/renamed.txt")
	expectStatus(t, deleted, 200, "delete renamed file")
	nestedListing := user.fileFragment(id, "data", "nested")
	expectStatus(t, nestedListing, 200, "list nested directory")
	if strings.Contains(nestedListing.Body, "renamed.txt") {
		t.Fatalf("deleted nested file is still listed")
	}

	// Terminal connect, command execution, clean disconnect, and reconnect.
	marker := "e2e-terminal-" + randomSuffix(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	started = time.Now()
	terminalErr := user.terminalExec(ctx, id, marker)
	rec.record("terminal.exec", user.username, started, terminalErr, false, marker)
	if terminalErr != nil {
		t.Fatalf("terminal exec: %v", terminalErr)
	}
	runtimeID := user.runtimeID(id)
	if leftover := user.waitTerminalCleanup(runtimeID, 15*time.Second); len(leftover) > 0 {
		t.Fatalf("terminal disconnect left login shell processes: %v", leftover)
	}

	reconnectMarker := "e2e-terminal-reconnect-" + randomSuffix(t)
	started = time.Now()
	reconnectErr := user.terminalExec(ctx, id, reconnectMarker)
	rec.record("terminal.reconnect", user.username, started, reconnectErr, false, reconnectMarker)
	if reconnectErr != nil {
		t.Fatalf("terminal reconnect: %v", reconnectErr)
	}
	if leftover := user.waitTerminalCleanup(runtimeID, 15*time.Second); len(leftover) > 0 {
		t.Fatalf("terminal reconnect cleanup left login shell processes: %v", leftover)
	}

	// A fresh HTTP connection with the same session must still see the
	// workspace; this is the browser-reconnect half of the scenario.
	user.http.CloseIdleConnections()
	page := user.get("/workspaces")
	expectStatus(t, page, 200, "reload workspaces after reconnecting")
	expectContains(t, page.Body, name, "workspaces after reconnect")

	started = time.Now()
	stopResult := user.stopWorkspace(id)
	rec.record("workspace.stop", user.username, started, statusError(stopResult, 200), false, id)
	expectStatus(t, stopResult, 200, "stop workspace")
	user.waitWorkspaceStatus(id, "stopped", time.Minute)

	started = time.Now()
	restartResult := user.restartWorkspace(id)
	rec.record("workspace.restart", user.username, started, statusError(restartResult, 200), false, id)
	expectStatus(t, restartResult, 200, "restart workspace")
	user.waitWorkspaceStatus(id, "running", 2*time.Minute)
	restartedContent, restartedDownload := user.downloadFile(id, "data", "note.txt")
	expectStatus(t, restartedDownload, 200, "download after restart")
	if string(restartedContent) != string(volumeContent) {
		t.Fatalf("volume content changed across restart: %q", restartedContent)
	}

	stopResult = user.stopWorkspace(id)
	expectStatus(t, stopResult, 200, "stop before delete")
	user.waitWorkspaceStatus(id, "stopped", time.Minute)
	started = time.Now()
	deleteResult := user.deleteWorkspace(id)
	rec.record("workspace.delete", user.username, started, statusError(deleteResult, 200), false, id)
	expectStatus(t, deleteResult, 200, "delete workspace")

	// Explicit deletion retains the named volume and archives the managed
	// directory. Both must be visible to their owner and discardable through
	// the ordinary storage page.
	storage := user.storagePage()
	expectStatus(t, storage, 200, "storage page")
	if !strings.Contains(storage.Body, "/storage/volumes/"+id+"/") {
		t.Fatalf("retained volume is not visible on /storage")
	}
	if !strings.Contains(storage.Body, "/storage/directories/"+id+"/") {
		t.Fatalf("retained directory is not visible on /storage")
	}
	started = time.Now()
	discardRetainedStorage(t, user, id, "data")
	rec.record("storage.discard", user.username, started, nil, false, id)
	storage = user.storagePage()
	if strings.Contains(storage.Body, id) {
		t.Fatalf("discarded retained storage is still listed")
	}

	rec.requireNoUnexpectedErrors(t)
}

func TestAdminQuotaExhaustion(t *testing.T) {
	image := e2eImage(t)
	inst := newInstance(t, instanceOptions{SeedUsers: 1})
	rec := newRecorder(t.Name(), image, 1)
	t.Cleanup(func() { rec.write(t) })
	admin := inst.adminClient(t)
	user := inst.userClient(t, 0)

	started := time.Now()
	quota := admin.updateUserQuota(inst.userIDs[0], 1000, 1024, 10, 2, 1)
	rec.record("admin.quota.update", admin.username, started, statusError(quota, 200), false, inst.usernames[0])
	expectStatus(t, quota, 200, "update user quota")

	firstName := "quota-a-" + randomSuffix(t)
	secondName := "quota-b-" + randomSuffix(t)
	thirdName := "quota-c-" + randomSuffix(t)
	started = time.Now()
	first := user.createWorkspace(firstName, inst.templateID, nil)
	rec.record("quota.create.first", user.username, started, statusError(first, 200), false, firstName)
	expectStatus(t, first, 200, "create first quota workspace")
	firstID := user.workspaceID(firstName)

	// Both workspaces are intentionally created before either is started.
	// Their named volumes do not exist yet; storage measurement must treat
	// that as zero rather than failing the second create.
	started = time.Now()
	second := user.createWorkspace(secondName, inst.templateID, nil)
	rec.record("quota.create.second", user.username, started, statusError(second, 200), false, secondName)
	expectStatus(t, second, 200, "create second quota workspace")
	secondID := user.workspaceID(secondName)

	started = time.Now()
	third := user.createWorkspace(thirdName, inst.templateID, nil)
	thirdErr := statusError(third, 200)
	rec.record("quota.create.over-total", user.username, started, thirdErr, true, thirdName)
	if third.Status != 400 || !strings.Contains(third.Body, "exceed your assigned quota") {
		t.Fatalf("third workspace status=%d body=%s, want total-quota rejection", third.Status, third.Body)
	}
	if page := user.get("/workspaces"); page.Status == 200 {
		if _, found := findWorkspaceRowByName(page.Body, thirdName); found {
			t.Fatalf("rejected over-quota workspace %q was still recorded", thirdName)
		}
	}

	started = time.Now()
	startFirst := user.startWorkspace(firstID)
	rec.record("quota.start.first", user.username, started, statusError(startFirst, 200), false, firstID)
	expectStatus(t, startFirst, 200, "start first quota workspace")
	user.waitWorkspaceStatus(firstID, "running", 2*time.Minute)

	started = time.Now()
	startSecond := user.startWorkspace(secondID)
	secondErr := statusError(startSecond, 200)
	rec.record("quota.start.over-running", user.username, started, secondErr, true, secondID)
	if startSecond.Status != 400 || !strings.Contains(startSecond.Body, "running-workspace quota is full") {
		t.Fatalf("second start status=%d body=%s, want running-quota rejection", startSecond.Status, startSecond.Body)
	}
	if row := user.workspaceRow(secondID); row.RuntimeID != "" {
		t.Fatalf("rejected over-quota start created a runtime container: %+v", row)
	}

	stopFirst := user.stopWorkspace(firstID)
	expectStatus(t, stopFirst, 200, "stop first quota workspace")
	user.waitWorkspaceStatus(firstID, "stopped", time.Minute)
	started = time.Now()
	startSecond = user.startWorkspace(secondID)
	rec.record("quota.start.after-stop", user.username, started, statusError(startSecond, 200), false, secondID)
	expectStatus(t, startSecond, 200, "start second after stopping first")
	user.waitWorkspaceStatus(secondID, "running", 2*time.Minute)
	stopSecond := user.stopWorkspace(secondID)
	expectStatus(t, stopSecond, 200, "stop second quota workspace")
	user.waitWorkspaceStatus(secondID, "stopped", time.Minute)

	for _, id := range []string{firstID, secondID} {
		deleted := user.deleteWorkspace(id)
		expectStatus(t, deleted, 200, "delete quota workspace")
	}
	for _, pair := range [][2]string{{firstID, "data"}, {secondID, "data"}} {
		discardRetainedStorage(t, user, pair[0], pair[1])
	}

	rec.requireNoUnexpectedErrors(t)
}

func TestRegistrationAndAdminTemplateUI(t *testing.T) {
	image := e2eImage(t)
	inst := newInstance(t, instanceOptions{
		SeedUsers:           0,
		RegistrationEnabled: true,
		RegistrationQuota:   defaultUserQuota(),
	})
	rec := newRecorder(t.Name(), image, 1)
	t.Cleanup(func() { rec.write(t) })
	admin := inst.adminClient(t)
	suffix := randomSuffix(t)
	templateName := "UI Template " + suffix
	configuration := `{"command":["/bin/sh","-c","trap 'exit 0' TERM; while :; do sleep 1; done"],"mounts":[{"name":"data","type":"volume","container_path":"/data","file_manager":true},{"name":"home","type":"directory","container_path":"/home/cows","file_manager":true}]}`

	started := time.Now()
	created := admin.postForm("/admin/templates", url.Values{
		"name":                     {templateName},
		"description":              {"Created through the browser form by the e2e suite"},
		"image_reference":          {image},
		"default_cpu_millis":       {"100"},
		"max_cpu_millis":           {"100"},
		"default_memory_mib":       {"256"},
		"max_memory_mib":           {"256"},
		"initial_connection_hours": {"1"},
		"stopped_retention_hours":  {"1"},
		"configuration_mode":       {"json"},
		"configuration_json":       {configuration},
		"access_methods":           {"terminal", "files"},
		"allowed_roles":            {"user"},
		"group_access_mode":        {"exclude"},
		"enabled":                  {"on"},
	})
	rec.record("admin.template.create", admin.username, started, statusError(created, 200), false, templateName)
	expectStatus(t, created, 200, "create template through admin UI")
	expectContains(t, created.Body, templateName, "new template in admin list")
	templateID, found := templateIDByName(created.Body, templateName)
	if !found {
		t.Fatalf("new template %q has no admin row", templateName)
	}

	username := "registered-" + suffix
	email := username + "@example.test"
	password := "registered user password " + suffix
	guest := newClient(t, inst.baseURL, username, password)
	started = time.Now()
	registered := guest.register(username, email, password)
	rec.record("account.register", username, started, statusError(registered, 200), false, email)
	expectStatus(t, registered, 200, "self-registration")
	expectContains(t, registered.Body, "account was created", "registration notice")
	guest.login()

	name := "registered-workspace-" + suffix
	started = time.Now()
	creation := guest.createWorkspace(name, templateID, nil)
	rec.record("registered.workspace.create", username, started, statusError(creation, 200), false, name)
	expectStatus(t, creation, 200, "create workspace from admin-created template")
	id := guest.workspaceID(name)
	startResult := guest.startWorkspace(id)
	expectStatus(t, startResult, 200, "start registered user's workspace")
	guest.waitWorkspaceStatus(id, "running", 2*time.Minute)

	content := []byte("registered user file " + id)
	started = time.Now()
	upload := guest.uploadFile(id, "data", ".", "registered.txt", content)
	rec.record("registered.file.upload", username, started, statusError(upload, 200), false, "data/registered.txt")
	expectStatus(t, upload, 200, "upload as registered user")
	started = time.Now()
	got, download := guest.downloadFile(id, "data", "registered.txt")
	rec.record("registered.file.download", username, started, statusError(download, 200), false, "data/registered.txt")
	expectStatus(t, download, 200, "download as registered user")
	if string(got) != string(content) {
		t.Fatalf("registered user download = %q, want %q", got, content)
	}

	stop := guest.stopWorkspace(id)
	expectStatus(t, stop, 200, "stop registered user's workspace")
	guest.waitWorkspaceStatus(id, "stopped", time.Minute)
	deleted := guest.deleteWorkspace(id)
	expectStatus(t, deleted, 200, "delete registered user's workspace")
	discardRetainedStorage(t, guest, id, "data")

	rec.requireNoUnexpectedErrors(t)
}

func TestStorageReattachmentOwnershipAndSingleUse(t *testing.T) {
	image := e2eImage(t)
	inst := newInstance(t, instanceOptions{SeedUsers: 2})
	rec := newRecorder(t.Name(), image, 2)
	t.Cleanup(func() { rec.write(t) })
	owner := inst.userClient(t, 0)
	other := inst.userClient(t, 1)

	oldName := "reattach-old-" + randomSuffix(t)
	started := time.Now()
	created := owner.createWorkspace(oldName, inst.templateID, nil)
	rec.record("reattach.create-old", owner.username, started, statusError(created, 200), false, oldName)
	expectStatus(t, created, 200, "create old workspace")
	oldID := owner.workspaceID(oldName)
	startOld := owner.startWorkspace(oldID)
	expectStatus(t, startOld, 200, "start old workspace")
	owner.waitWorkspaceStatus(oldID, "running", 2*time.Minute)

	volumeContent := []byte("reattach volume " + oldID)
	directoryContent := []byte("reattach directory " + oldID)
	expectStatus(t, owner.uploadFile(oldID, "data", ".", "keep.txt", volumeContent), 200, "upload old volume file")
	expectStatus(t, owner.uploadFile(oldID, "home", ".", "keep.txt", directoryContent), 200, "upload old directory file")
	stopOld := owner.stopWorkspace(oldID)
	expectStatus(t, stopOld, 200, "stop old workspace")
	owner.waitWorkspaceStatus(oldID, "stopped", time.Minute)
	started = time.Now()
	deleteOld := owner.deleteWorkspace(oldID)
	rec.record("reattach.delete-old", owner.username, started, statusError(deleteOld, 200), false, oldID)
	expectStatus(t, deleteOld, 200, "delete old workspace")

	storage := owner.storagePage()
	expectStatus(t, storage, 200, "old storage page")
	if !strings.Contains(storage.Body, "/storage/volumes/"+oldID+"/") || !strings.Contains(storage.Body, "/storage/directories/"+oldID+"/") {
		t.Fatalf("old retained storage is not listed for its owner")
	}

	reattachFields := url.Values{
		"reattach_volume__data": {oldID},
		"reattach_directories":  {oldID},
	}
	otherName := "reattach-other-" + randomSuffix(t)
	started = time.Now()
	otherAttempt := other.createWorkspace(otherName, inst.templateID, reattachFields)
	otherErr := statusError(otherAttempt, 200)
	rec.record("reattach.foreign-owner", other.username, started, otherErr, true, oldID)
	if otherAttempt.Status != 400 {
		t.Fatalf("other user's reattachment status=%d body=%s, want 400", otherAttempt.Status, otherAttempt.Body)
	}

	newName := "reattach-new-" + randomSuffix(t)
	started = time.Now()
	reattached := owner.createWorkspace(newName, inst.templateID, reattachFields)
	rec.record("reattach.owner", owner.username, started, statusError(reattached, 200), false, oldID)
	expectStatus(t, reattached, 200, "reattach as owner")
	newID := owner.workspaceID(newName)
	owner.waitWorkspaceStatus(newID, "running", 2*time.Minute)
	gotVolume, volumeResult := owner.downloadFile(newID, "data", "keep.txt")
	expectStatus(t, volumeResult, 200, "download reattached volume file")
	if string(gotVolume) != string(volumeContent) {
		t.Fatalf("reattached volume content = %q, want %q", gotVolume, volumeContent)
	}
	gotDirectory, directoryResult := owner.downloadFile(newID, "home", "keep.txt")
	expectStatus(t, directoryResult, 200, "download reattached directory file")
	if string(gotDirectory) != string(directoryContent) {
		t.Fatalf("reattached directory content = %q, want %q", gotDirectory, directoryContent)
	}
	storage = owner.storagePage()
	if strings.Contains(storage.Body, "/storage/volumes/"+oldID+"/") || strings.Contains(storage.Body, "/storage/directories/"+oldID+"/") {
		t.Fatalf("consumed tombstones are still listed")
	}

	againName := "reattach-again-" + randomSuffix(t)
	started = time.Now()
	again := owner.createWorkspace(againName, inst.templateID, reattachFields)
	againErr := statusError(again, 200)
	rec.record("reattach.single-use", owner.username, started, againErr, true, oldID)
	if again.Status != 400 {
		t.Fatalf("second reattachment status=%d body=%s, want 400", again.Status, again.Body)
	}

	stopNew := owner.stopWorkspace(newID)
	expectStatus(t, stopNew, 200, "stop reattached workspace")
	owner.waitWorkspaceStatus(newID, "stopped", time.Minute)
	deleteNew := owner.deleteWorkspace(newID)
	expectStatus(t, deleteNew, 200, "delete reattached workspace")
	discardRetainedStorage(t, owner, newID, "data")

	rec.requireNoUnexpectedErrors(t)
}

func TestServerRestartPreservesSessionAndData(t *testing.T) {
	image := e2eImage(t)
	inst := newInstance(t, instanceOptions{SeedUsers: 1})
	rec := newRecorder(t.Name(), image, 1)
	t.Cleanup(func() { rec.write(t) })
	user := inst.userClient(t, 0)
	name := "restart-" + randomSuffix(t)

	started := time.Now()
	creation := user.createWorkspace(name, inst.templateID, nil)
	rec.record("restart.create", user.username, started, statusError(creation, 200), false, name)
	expectStatus(t, creation, 200, "create restart workspace")
	id := user.workspaceID(name)
	startResult := user.startWorkspace(id)
	expectStatus(t, startResult, 200, "start restart workspace")
	user.waitWorkspaceStatus(id, "running", 2*time.Minute)
	content := []byte("survives a server restart " + id)
	expectStatus(t, user.uploadFile(id, "data", ".", "keep.txt", content), 200, "upload before restart")

	// Reuse the same browser session and the same persistent workspace data
	// across a real process restart. Session rows, workspace rows, mounts, and
	// the container all live outside the HTTP process.
	user.http.CloseIdleConnections()
	inst.restart()
	started = time.Now()
	page := user.get("/workspaces")
	rec.record("restart.reconnect", user.username, started, statusError(page, 200), false, name)
	expectStatus(t, page, 200, "workspaces after server restart")
	expectContains(t, page.Body, name, "workspace after server restart")
	user.csrf = extractCSRF(page.Body)
	if user.csrf == "" {
		user.csrf = user.csrfToken()
	}
	user.waitWorkspaceStatus(id, "running", time.Minute)
	started = time.Now()
	got, download := user.downloadFile(id, "data", "keep.txt")
	rec.record("restart.download", user.username, started, statusError(download, 200), false, "data/keep.txt")
	expectStatus(t, download, 200, "download after server restart")
	if string(got) != string(content) {
		t.Fatalf("content after restart = %q, want %q", got, content)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	marker := "e2e-restart-terminal-" + randomSuffix(t)
	started = time.Now()
	terminalErr := user.terminalExec(ctx, id, marker)
	rec.record("restart.terminal", user.username, started, terminalErr, false, marker)
	if terminalErr != nil {
		t.Fatalf("terminal after restart: %v", terminalErr)
	}
	if leftover := user.waitTerminalCleanup(user.runtimeID(id), 15*time.Second); len(leftover) > 0 {
		t.Fatalf("terminal cleanup after restart left processes: %v", leftover)
	}

	stopResult := user.stopWorkspace(id)
	expectStatus(t, stopResult, 200, "stop after restart")
	user.waitWorkspaceStatus(id, "stopped", time.Minute)
	deleteResult := user.deleteWorkspace(id)
	expectStatus(t, deleteResult, 200, "delete after restart")
	discardRetainedStorage(t, user, id, "data")

	rec.requireNoUnexpectedErrors(t)
}

func expectStatus(t *testing.T, result httpResult, want int, label string) {
	t.Helper()
	if result.Status != want {
		t.Fatalf("%s: status=%d want=%d body=%s", label, result.Status, want, result.Body)
	}
}

func expectContains(t *testing.T, body, needle, label string) {
	t.Helper()
	if !strings.Contains(body, needle) {
		t.Fatalf("%s: %q not found in body=%s", label, needle, body)
	}
}

func statusError(result httpResult, want int) error {
	if result.Status == want {
		return nil
	}
	return fmt.Errorf("status %d, want %d", result.Status, want)
}

func discardRetainedStorage(t *testing.T, c *client, workspaceID, volumeMount string) {
	t.Helper()
	storage := c.storagePage()
	expectStatus(t, storage, 200, "storage page before discard")
	if strings.Contains(storage.Body, "/storage/volumes/"+workspaceID+"/") {
		result := c.postForm("/storage/volumes/"+workspaceID+"/"+url.PathEscape(volumeMount)+"/delete", nil)
		expectStatus(t, result, 200, "discard retained volume")
	}
	if strings.Contains(storage.Body, "/storage/directories/"+workspaceID+"/") {
		result := c.postForm("/storage/directories/"+workspaceID+"/delete", nil)
		expectStatus(t, result, 200, "discard retained directory")
	}
}

func zipContains(archive *zip.Reader, name string) bool {
	for _, file := range archive.File {
		if file.Name == name {
			return true
		}
	}
	return false
}

func zipNames(archive *zip.Reader) []string {
	names := make([]string, 0, len(archive.File))
	for _, file := range archive.File {
		names = append(names, file.Name)
	}
	return names
}
