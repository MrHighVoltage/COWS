//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParallelUsers(t *testing.T) {
	image := e2eImage(t)
	users := envInt("COWS_E2E_USERS", 4)
	inst := newInstance(t, instanceOptions{SeedUsers: users})
	rec := newRecorder(t.Name(), image, users)
	t.Cleanup(func() {
		rec.write(t)
		if count := rec.countUnexpectedErrors(); count > 0 {
			t.Errorf("%d parallel user operation(s) failed unexpectedly; see the e2e report", count)
		}
	})

	clients := make([]*client, users)
	for index := range clients {
		clients[index] = inst.userClient(t, index)
	}
	start := make(chan struct{})
	for index := range clients {
		index := index
		t.Run(fmt.Sprintf("user-%02d", index+1), func(t *testing.T) {
			t.Parallel()
			<-start
			value := *clients[index]
			value.t = t
			runParallelUser(t, &value, inst, index, rec)
		})
	}
	close(start)
}

func runParallelUser(t *testing.T, c *client, inst *instance, index int, rec *recorder) {
	t.Helper()
	name := fmt.Sprintf("parallel-%02d-%s", index+1, randomSuffix(t))
	started := time.Now()
	creation := c.createWorkspace(name, inst.templateID, nil)
	rec.record("parallel.workspace.create", c.username, started, statusError(creation, 200), false, name)
	if creation.Status != 200 {
		t.Fatalf("create workspace: status=%d body=%s", creation.Status, creation.Body)
	}
	id := c.workspaceID(name)

	started = time.Now()
	startResult := c.startWorkspace(id)
	rec.record("parallel.workspace.start", c.username, started, statusError(startResult, 200), false, id)
	if startResult.Status != 200 {
		t.Fatalf("start workspace: status=%d body=%s", startResult.Status, startResult.Body)
	}
	c.waitWorkspaceStatus(id, "running", 2*time.Minute)

	volumeContent := []byte("parallel volume " + id)
	directoryContent := []byte("parallel directory " + id)
	started = time.Now()
	volumeUpload := c.uploadFile(id, "data", ".", "note.txt", volumeContent)
	rec.record("parallel.file.upload.volume", c.username, started, statusError(volumeUpload, 200), false, "data/note.txt")
	if volumeUpload.Status != 200 {
		t.Fatalf("upload volume file: status=%d body=%s", volumeUpload.Status, volumeUpload.Body)
	}
	started = time.Now()
	directoryUpload := c.uploadFile(id, "home", ".", "note.txt", directoryContent)
	rec.record("parallel.file.upload.directory", c.username, started, statusError(directoryUpload, 200), false, "home/note.txt")
	if directoryUpload.Status != 200 {
		t.Fatalf("upload directory file: status=%d body=%s", directoryUpload.Status, directoryUpload.Body)
	}

	started = time.Now()
	gotVolume, volumeDownload := c.downloadFile(id, "data", "note.txt")
	rec.record("parallel.file.download.volume", c.username, started, statusError(volumeDownload, 200), false, "data/note.txt")
	if volumeDownload.Status != 200 || string(gotVolume) != string(volumeContent) {
		t.Fatalf("volume download status=%d content=%q", volumeDownload.Status, gotVolume)
	}
	started = time.Now()
	gotDirectory, directoryDownload := c.downloadFile(id, "home", "note.txt")
	rec.record("parallel.file.download.directory", c.username, started, statusError(directoryDownload, 200), false, "home/note.txt")
	if directoryDownload.Status != 200 || string(gotDirectory) != string(directoryContent) {
		t.Fatalf("directory download status=%d content=%q", directoryDownload.Status, gotDirectory)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	marker := "parallel-terminal-" + randomSuffix(t)
	started = time.Now()
	terminalErr := c.terminalExec(ctx, id, marker)
	rec.record("parallel.terminal", c.username, started, terminalErr, false, marker)
	if terminalErr != nil {
		t.Fatalf("terminal: %v", terminalErr)
	}
	if leftover := c.waitTerminalCleanup(c.runtimeID(id), 20*time.Second); len(leftover) > 0 {
		t.Fatalf("terminal cleanup left processes: %v", leftover)
	}

	started = time.Now()
	stopResult := c.stopWorkspace(id)
	rec.record("parallel.workspace.stop", c.username, started, statusError(stopResult, 200), false, id)
	if stopResult.Status != 200 {
		t.Fatalf("stop workspace: status=%d body=%s", stopResult.Status, stopResult.Body)
	}
	c.waitWorkspaceStatus(id, "stopped", time.Minute)

	started = time.Now()
	deleteResult := c.deleteWorkspace(id)
	rec.record("parallel.workspace.delete", c.username, started, statusError(deleteResult, 200), false, id)
	if deleteResult.Status != 200 {
		t.Fatalf("delete workspace: status=%d body=%s", deleteResult.Status, deleteResult.Body)
	}
}

func TestParallelQuotaExhaustion(t *testing.T) {
	image := e2eImage(t)
	users := envInt("COWS_E2E_QUOTA_USERS", 2)
	inst := newInstance(t, instanceOptions{SeedUsers: users})
	rec := newRecorder(t.Name(), image, users)
	t.Cleanup(func() {
		rec.write(t)
		if count := rec.countUnexpectedErrors(); count > 0 {
			t.Errorf("%d parallel quota operation(s) failed unexpectedly; see the e2e report", count)
		}
	})

	clients := make([]*client, users)
	for index := range clients {
		clients[index] = inst.userClient(t, index)
	}
	start := make(chan struct{})
	for index := range clients {
		index := index
		t.Run(fmt.Sprintf("user-%02d", index+1), func(t *testing.T) {
			t.Parallel()
			<-start
			value := *clients[index]
			value.t = t
			runParallelQuotaUser(t, &value, inst, index, rec)
		})
	}
	close(start)
}

func runParallelQuotaUser(t *testing.T, c *client, inst *instance, index int, rec *recorder) {
	t.Helper()
	suffix := randomSuffix(t)
	firstName := fmt.Sprintf("quota-%02d-a-%s", index+1, suffix)
	secondName := fmt.Sprintf("quota-%02d-b-%s", index+1, suffix)
	thirdName := fmt.Sprintf("quota-%02d-c-%s", index+1, suffix)

	started := time.Now()
	first := c.createWorkspace(firstName, inst.templateID, nil)
	rec.record("parallel.quota.create.first", c.username, started, statusError(first, 200), false, firstName)
	if first.Status != 200 {
		t.Fatalf("create first: status=%d body=%s", first.Status, first.Body)
	}
	firstID := c.workspaceID(firstName)

	started = time.Now()
	second := c.createWorkspace(secondName, inst.templateID, nil)
	rec.record("parallel.quota.create.second", c.username, started, statusError(second, 200), false, secondName)
	if second.Status != 200 {
		t.Fatalf("create second: status=%d body=%s", second.Status, second.Body)
	}
	secondID := c.workspaceID(secondName)

	started = time.Now()
	third := c.createWorkspace(thirdName, inst.templateID, nil)
	rec.record("parallel.quota.create.over-total", c.username, started, statusError(third, 200), true, thirdName)
	if third.Status != 400 || !strings.Contains(third.Body, "exceed your assigned quota") {
		t.Fatalf("third create status=%d body=%s, want total-quota rejection", third.Status, third.Body)
	}
	if page := c.get("/workspaces"); page.Status == 200 {
		if _, found := findWorkspaceRowByName(page.Body, thirdName); found {
			t.Fatalf("rejected over-quota workspace %q was still recorded", thirdName)
		}
	}

	started = time.Now()
	startFirst := c.startWorkspace(firstID)
	rec.record("parallel.quota.start.first", c.username, started, statusError(startFirst, 200), false, firstID)
	if startFirst.Status != 200 {
		t.Fatalf("start first: status=%d body=%s", startFirst.Status, startFirst.Body)
	}
	c.waitWorkspaceStatus(firstID, "running", 2*time.Minute)

	started = time.Now()
	startSecond := c.startWorkspace(secondID)
	rec.record("parallel.quota.start.over-running", c.username, started, statusError(startSecond, 200), true, secondID)
	if startSecond.Status != 400 || !strings.Contains(startSecond.Body, "running-workspace quota is full") {
		t.Fatalf("second start status=%d body=%s, want running-quota rejection", startSecond.Status, startSecond.Body)
	}
	if row := c.workspaceRow(secondID); row.RuntimeID != "" {
		t.Fatalf("rejected over-quota start created a runtime container: %+v", row)
	}

	stopFirst := c.stopWorkspace(firstID)
	if stopFirst.Status != 200 {
		t.Fatalf("stop first: status=%d body=%s", stopFirst.Status, stopFirst.Body)
	}
	c.waitWorkspaceStatus(firstID, "stopped", time.Minute)

	started = time.Now()
	startSecond = c.startWorkspace(secondID)
	rec.record("parallel.quota.start.after-stop", c.username, started, statusError(startSecond, 200), false, secondID)
	if startSecond.Status != 200 {
		t.Fatalf("start second after stop: status=%d body=%s", startSecond.Status, startSecond.Body)
	}
	c.waitWorkspaceStatus(secondID, "running", 2*time.Minute)
	stopSecond := c.stopWorkspace(secondID)
	if stopSecond.Status != 200 {
		t.Fatalf("stop second: status=%d body=%s", stopSecond.Status, stopSecond.Body)
	}
	c.waitWorkspaceStatus(secondID, "stopped", time.Minute)

	for _, id := range []string{firstID, secondID} {
		deleted := c.deleteWorkspace(id)
		if deleted.Status != 200 {
			t.Fatalf("delete %s: status=%d body=%s", id, deleted.Status, deleted.Body)
		}
	}
}

func TestParallelLoginBurstDiagnostic(t *testing.T) {
	image := e2eImage(t)
	users := envInt("COWS_E2E_LOGIN_USERS", 8)
	inst := newInstance(t, instanceOptions{SeedUsers: users})
	rec := newRecorder(t.Name(), image, users)
	t.Cleanup(func() { rec.write(t) })

	type loginResult struct {
		username string
		status   int
		err      error
		started  time.Time
	}
	results := make(chan loginResult, users)
	var wait sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < users; index++ {
		username := inst.usernames[index]
		password := inst.passwords[index]
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			started := time.Now()
			status, err := rawLogin(inst.baseURL, username, password)
			results <- loginResult{username: username, status: status, err: err, started: started}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	throttled := 0
	for result := range results {
		expected := result.status == http.StatusTooManyRequests
		if expected {
			throttled++
		}
		recordErr := result.err
		if recordErr == nil && result.status != http.StatusOK {
			recordErr = fmt.Errorf("status %d", result.status)
		}
		rec.record("login.parallel", result.username, result.started, recordErr, expected, fmt.Sprintf("status=%d", result.status))
	}
	t.Logf("parallel login burst: %d/%d requests were throttled by the direct-peer login limiter", throttled, users)
	if count := rec.countUnexpectedErrors(); count > 0 {
		t.Fatalf("%d parallel login request(s) failed outside the documented throttle", count)
	}
}

func rawLogin(baseURL, username, password string) (int, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	response, err := client.Get(strings.TrimRight(baseURL, "/") + "/login")
	if err != nil {
		return 0, err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	response.Body.Close()
	if readErr != nil {
		return 0, readErr
	}
	token := extractCSRF(string(body))
	if token == "" {
		return 0, fmt.Errorf("login page has no CSRF token")
	}
	response, err = client.PostForm(strings.TrimRight(baseURL, "/")+"/login", url.Values{
		"csrf_token": {token},
		"username":   {username},
		"password":   {password},
	})
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode, nil
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}
