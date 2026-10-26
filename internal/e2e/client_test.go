//go:build e2e

package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type httpResult struct {
	Status int
	Body   string
	Header http.Header
	URL    *url.URL
}

type client struct {
	t        *testing.T
	baseURL  string
	base     *url.URL
	http     *http.Client
	jar      http.CookieJar
	username string
	password string
	csrf     string
}

func newClient(t *testing.T, baseURL, username, password string) *client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse e2e base URL: %v", err)
	}
	return &client{
		t: t, baseURL: strings.TrimRight(baseURL, "/"), base: base,
		http: &http.Client{Jar: jar, Timeout: 5 * time.Minute}, jar: jar,
		username: username, password: password,
	}
}

func newLoggedInClient(t *testing.T, baseURL, username, password string) *client {
	t.Helper()
	value := newClient(t, baseURL, username, password)
	value.login()
	return value
}

func (c *client) login() {
	c.t.Helper()
	status, body, err := c.loginAttempt()
	if err != nil {
		c.t.Fatalf("login %s: %v", c.username, err)
	}
	if status != http.StatusOK {
		c.t.Fatalf("login %s status = %d body=%s", c.username, status, body)
	}
	page := c.get("/workspaces")
	if page.Status != http.StatusOK {
		c.t.Fatalf("post-login workspaces status = %d body=%s", page.Status, page.Body)
	}
	if strings.Contains(page.Body, `action="/login"`) {
		c.t.Fatalf("login %s did not establish a session: %s", c.username, page.Body)
	}
	c.csrf = extractCSRF(page.Body)
	if c.csrf == "" {
		c.t.Fatalf("post-login workspaces page has no CSRF token")
	}
}

func (c *client) loginAttempt() (int, string, error) {
	c.t.Helper()
	loginPage := c.get("/login")
	if loginPage.Status != http.StatusOK {
		return loginPage.Status, loginPage.Body, fmt.Errorf("login page status %d", loginPage.Status)
	}
	token := extractCSRF(loginPage.Body)
	if token == "" {
		return 0, "", fmt.Errorf("login page has no CSRF token")
	}
	result := c.postForm("/login", url.Values{
		"csrf_token": {token},
		"username":   {c.username},
		"password":   {c.password},
	})
	return result.Status, result.Body, nil
}

func (c *client) csrfToken() string {
	c.t.Helper()
	page := c.get("/workspaces")
	if page.Status != http.StatusOK {
		c.t.Fatalf("load CSRF page: status=%d body=%s", page.Status, page.Body)
	}
	token := extractCSRF(page.Body)
	if token == "" {
		c.t.Fatalf("page has no CSRF token")
	}
	c.csrf = token
	return token
}

func (c *client) do(request *http.Request) httpResult {
	c.t.Helper()
	request.Header.Set("User-Agent", "cows-e2e/1")
	response, err := c.http.Do(request)
	if err != nil {
		c.t.Fatalf("%s %s: %v", request.Method, request.URL, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 128<<20))
	if err != nil {
		c.t.Fatalf("read %s %s: %v", request.Method, request.URL, err)
	}
	return httpResult{Status: response.StatusCode, Body: string(body), Header: response.Header.Clone(), URL: response.Request.URL}
}

func (c *client) get(path string) httpResult {
	c.t.Helper()
	request, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		c.t.Fatalf("build GET %s: %v", path, err)
	}
	return c.do(request)
}

func (c *client) postForm(path string, values url.Values) httpResult {
	c.t.Helper()
	if values == nil {
		values = url.Values{}
	}
	if values.Get("csrf_token") == "" {
		token := c.csrf
		if token == "" {
			token = c.csrfToken()
		}
		values.Set("csrf_token", token)
	}
	request, err := http.NewRequest(http.MethodPost, c.baseURL+path, strings.NewReader(values.Encode()))
	if err != nil {
		c.t.Fatalf("build POST %s: %v", path, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(request)
}

func (c *client) postMultipart(path string, fields map[string]string, filename string, data []byte) httpResult {
	c.t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if _, ok := fields["csrf_token"]; !ok {
		token := c.csrf
		if token == "" {
			token = c.csrfToken()
		}
		fields["csrf_token"] = token
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			c.t.Fatalf("write multipart field %s: %v", key, err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		c.t.Fatalf("create multipart file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		c.t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.Close(); err != nil {
		c.t.Fatalf("close multipart body: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, c.baseURL+path, &body)
	if err != nil {
		c.t.Fatalf("build multipart POST %s: %v", path, err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return c.do(request)
}

func (c *client) createWorkspace(name, templateID string, extra url.Values) httpResult {
	c.t.Helper()
	values := url.Values{"name": {name}, "template_id": {templateID}}
	for key, list := range extra {
		for _, value := range list {
			values.Add(key, value)
		}
	}
	return c.postForm("/workspaces", values)
}

func (c *client) workspaceID(name string) string {
	c.t.Helper()
	page := c.get("/workspaces")
	if page.Status != http.StatusOK {
		c.t.Fatalf("workspaces page status=%d body=%s", page.Status, page.Body)
	}
	for _, row := range parseWorkspaceRows(page.Body) {
		if row.Name == name || strings.HasPrefix(row.Name, name+" ") {
			return row.ID
		}
	}
	c.t.Fatalf("workspace %q not present on /workspaces; body=%s", name, page.Body)
	return ""
}

func (c *client) workspaceRow(id string) workspaceRow {
	c.t.Helper()
	page := c.get("/workspaces")
	if page.Status != http.StatusOK {
		c.t.Fatalf("workspaces page status=%d body=%s", page.Status, page.Body)
	}
	row, ok := findWorkspaceRow(page.Body, id)
	if !ok {
		c.t.Fatalf("workspace row %s not present on /workspaces; body=%s", id, page.Body)
	}
	return row
}

func (c *client) waitWorkspaceStatus(id, want string, timeout time.Duration) workspaceRow {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	var last workspaceRow
	var lastBody string
	for {
		page := c.get("/workspaces")
		lastBody = page.Body
		if page.Status == http.StatusOK {
			if row, ok := findWorkspaceRow(page.Body, id); ok {
				last = row
				if row.Status == want {
					return row
				}
			}
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("workspace %s did not reach status %q; last=%+v body=%s", id, want, last, lastBody)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (c *client) workspaceAction(id, action string) httpResult {
	c.t.Helper()
	return c.postForm("/workspaces/"+id+"/"+action, nil)
}

func (c *client) startWorkspace(id string) httpResult {
	c.t.Helper()
	return c.workspaceAction(id, "start")
}

func (c *client) stopWorkspace(id string) httpResult {
	c.t.Helper()
	return c.workspaceAction(id, "stop")
}

func (c *client) restartWorkspace(id string) httpResult {
	c.t.Helper()
	return c.workspaceAction(id, "restart")
}

func (c *client) deleteWorkspace(id string) httpResult {
	c.t.Helper()
	return c.workspaceAction(id, "delete")
}

func (c *client) fileFragment(workspaceID, mount, relativePath string) httpResult {
	c.t.Helper()
	query := url.Values{"mount": {mount}, "path": {relativePath}}
	return c.get("/workspaces/" + workspaceID + "/access/files?" + query.Encode())
}

func (c *client) uploadFile(workspaceID, mount, relativePath, filename string, data []byte) httpResult {
	c.t.Helper()
	return c.postMultipart("/workspaces/"+workspaceID+"/files/upload", map[string]string{
		"mount": mount, "path": relativePath,
	}, filename, data)
}

func (c *client) makeDirectory(workspaceID, mount, relativePath, name string) httpResult {
	c.t.Helper()
	return c.postForm("/workspaces/"+workspaceID+"/files/mkdir", url.Values{
		"mount": {mount}, "path": {relativePath}, "name": {name},
	})
}

func (c *client) renamePath(workspaceID, mount, relativePath, oldName, newName string) httpResult {
	c.t.Helper()
	return c.postForm("/workspaces/"+workspaceID+"/files/rename", url.Values{
		"mount": {mount}, "path": {relativePath}, "old_name": {oldName}, "new_name": {newName},
	})
}

func (c *client) deletePath(workspaceID, mount, relativePath string) httpResult {
	c.t.Helper()
	return c.postForm("/workspaces/"+workspaceID+"/files/delete", url.Values{
		"mount": {mount}, "path": {relativePath},
	})
}

func (c *client) downloadFile(workspaceID, mount, relativePath string) ([]byte, httpResult) {
	c.t.Helper()
	query := url.Values{"mount": {mount}, "path": {relativePath}}
	result := c.get("/workspaces/" + workspaceID + "/files/download?" + query.Encode())
	return []byte(result.Body), result
}

func (c *client) downloadZip(workspaceID, mount, relativePath string) (*zip.Reader, httpResult) {
	c.t.Helper()
	query := url.Values{"mount": {mount}, "path": {relativePath}}
	result := c.get("/workspaces/" + workspaceID + "/files/download.zip?" + query.Encode())
	if result.Status != http.StatusOK {
		return nil, result
	}
	reader, err := zip.NewReader(bytes.NewReader([]byte(result.Body)), int64(len(result.Body)))
	if err != nil {
		c.t.Fatalf("read ZIP from %s/%s: %v", mount, relativePath, err)
	}
	return reader, result
}

func (c *client) storagePage() httpResult {
	c.t.Helper()
	return c.get("/storage")
}

func (c *client) updateUserQuota(userID string, maxCPU, maxMemoryMiB, maxStorageGiB, maxWorkspaces, maxRunning int64) httpResult {
	c.t.Helper()
	return c.postForm("/admin/users/"+userID+"/quota", url.Values{
		"max_cpu_millis":         {fmt.Sprintf("%d", maxCPU)},
		"max_memory_mib":         {fmt.Sprintf("%d", maxMemoryMiB)},
		"max_storage_gib":        {fmt.Sprintf("%d", maxStorageGiB)},
		"max_workspaces":         {fmt.Sprintf("%d", maxWorkspaces)},
		"max_running_workspaces": {fmt.Sprintf("%d", maxRunning)},
	})
}

func (c *client) register(username, email, password string) httpResult {
	c.t.Helper()
	page := c.get("/register")
	if page.Status != http.StatusOK {
		c.t.Fatalf("registration page status=%d body=%s", page.Status, page.Body)
	}
	token := extractCSRF(page.Body)
	if token == "" {
		c.t.Fatalf("registration page has no CSRF token")
	}
	return c.postForm("/register", url.Values{
		"csrf_token":       {token},
		"username":         {username},
		"email":            {email},
		"display_name":     {strings.ReplaceAll(username, "-", " ")},
		"password":         {password},
		"confirm_password": {password},
	})
}

func (c *client) sessionCookie() string {
	c.t.Helper()
	for _, cookie := range c.jar.Cookies(c.base) {
		if cookie.Name == "cows_session" {
			return cookie.Value
		}
	}
	c.t.Fatalf("no cows_session cookie in client jar")
	return ""
}

func (c *client) terminal(ctx context.Context, workspaceID string) (*websocket.Conn, error) {
	c.t.Helper()
	wsURL := "ws" + strings.TrimPrefix(c.baseURL, "http") + "/workspaces/" + workspaceID + "/terminal/ws"
	connection, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {"cows_session=" + c.sessionCookie()}},
	})
	return connection, err
}

func (c *client) terminalExec(ctx context.Context, workspaceID, marker string) error {
	c.t.Helper()
	connection, err := c.terminal(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("dial terminal: %w", err)
	}
	defer connection.CloseNow()
	if err := connection.Write(ctx, websocket.MessageText, []byte("echo "+marker+"\n")); err != nil {
		return fmt.Errorf("write terminal input: %w", err)
	}
	var output bytes.Buffer
	for {
		_, data, err := connection.Read(ctx)
		if err != nil {
			return fmt.Errorf("read terminal output (got %q): %w", output.String(), err)
		}
		output.Write(data)
		if strings.Contains(output.String(), marker) {
			break
		}
	}
	if err := connection.Close(websocket.StatusNormalClosure, "e2e disconnect"); err != nil {
		return fmt.Errorf("close terminal websocket: %w", err)
	}
	return nil
}

func (c *client) runtimeID(workspaceID string) string {
	c.t.Helper()
	row := c.workspaceRow(workspaceID)
	if row.RuntimeID == "" {
		c.t.Fatalf("workspace %s has no runtime ID; row=%+v", workspaceID, row)
	}
	return row.RuntimeID
}

func (c *client) waitTerminalCleanup(runtimeID string, timeout time.Duration) []string {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	var last []string
	for {
		last = terminalShellProcesses(c.t, runtimeID)
		if len(last) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func terminalShellProcesses(t *testing.T, runtimeID string) []string {
	t.Helper()
	output, err := exec.Command("podman", "top", runtimeID, "args").CombinedOutput()
	if err != nil {
		return nil
	}
	lines := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "COMMAND") || strings.HasPrefix(strings.ToUpper(line), "COMMAND ") {
			continue
		}
		// Only a terminal login shell matters here. The workspace's own PID 1
		// command may also be a shell (the test fixture uses one to handle
		// SIGTERM), so matching every /bin/sh would create false positives.
		// A killed orphan may remain as a zombie in a container whose PID 1 is
		// not a full init; that is not a live terminal session.
		if strings.Contains(line, "defunct") {
			continue
		}
		if strings.Contains(line, "sh -l") {
			lines = append(lines, line)
		}
	}
	return lines
}

type workspaceRow struct {
	ID        string
	Name      string
	Status    string
	RuntimeID string
}

var (
	csrfPattern         = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)
	workspaceRowPattern = regexp.MustCompile(`(?s)<tr id="workspace-row-([^"]+)"([^>]*)>(.*?)</tr>`)
	dataNamePattern     = regexp.MustCompile(`data-name="([^"]*)"`)
	dataStatusPattern   = regexp.MustCompile(`data-status="([^"]*)"`)
	runtimeIDPattern    = regexp.MustCompile(`Runtime ID</dt><dd><code>([^<]+)</code>`)
	adminTemplateRow    = regexp.MustCompile(`(?s)<tr id="admin-template-row-([^"]+)".*?</tr>`)
)

func extractCSRF(body string) string {
	match := csrfPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		return ""
	}
	return html.UnescapeString(match[1])
}

func parseWorkspaceRows(body string) []workspaceRow {
	matches := workspaceRowPattern.FindAllStringSubmatch(body, -1)
	rows := make([]workspaceRow, 0, len(matches))
	for _, match := range matches {
		row := workspaceRow{ID: match[1]}
		if name := dataNamePattern.FindStringSubmatch(match[2]); len(name) == 2 {
			row.Name = html.UnescapeString(name[1])
		}
		if status := dataStatusPattern.FindStringSubmatch(match[2]); len(status) == 2 {
			row.Status = status[1]
		}
		if runtimeID := runtimeIDPattern.FindStringSubmatch(match[3]); len(runtimeID) == 2 {
			row.RuntimeID = html.UnescapeString(runtimeID[1])
		}
		rows = append(rows, row)
	}
	return rows
}

func findWorkspaceRow(body, id string) (workspaceRow, bool) {
	for _, row := range parseWorkspaceRows(body) {
		if row.ID == id {
			return row, true
		}
	}
	return workspaceRow{}, false
}

func findWorkspaceRowByName(body, name string) (workspaceRow, bool) {
	for _, row := range parseWorkspaceRows(body) {
		if row.Name == name || strings.HasPrefix(row.Name, name+" ") {
			return row, true
		}
	}
	return workspaceRow{}, false
}

func templateIDByName(body, name string) (string, bool) {
	for _, match := range adminTemplateRow.FindAllStringSubmatch(body, -1) {
		if len(match) == 2 && strings.Contains(match[0], "<strong>"+html.EscapeString(name)+"</strong>") {
			return match[1], true
		}
	}
	return "", false
}
