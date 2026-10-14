package web

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/workspace"
)

var (
	formTagPattern      = regexp.MustCompile(`(?s)<form class="form-panel".*?</form>`)
	inputTagPattern     = regexp.MustCompile(`<input\b[^>]*>`)
	textareaTagPattern  = regexp.MustCompile(`(?s)<textarea\b([^>]*)>(.*?)</textarea>`)
	selectTagPattern    = regexp.MustCompile(`(?s)<select\b([^>]*)>(.*?)</select>`)
	optionTagPattern    = regexp.MustCompile(`<option\b[^>]*>`)
	tagAttributePattern = regexp.MustCompile(`([a-z][a-z0-9:_.-]*)(?:="([^"]*)")?`)
)

// browserFormValues collects what a browser would submit from the controls in
// markup: named text, hidden, and number inputs, checked checkboxes, textareas,
// and the selected option of each select. It understands only the markup COWS
// renders, which is enough to post a rendered form back without restating its
// control names, so a test fails when the template and the parser disagree.
func browserFormValues(markup string) url.Values {
	values := url.Values{}
	attributes := func(tag string) map[string]string {
		found := map[string]string{}
		for _, match := range tagAttributePattern.FindAllStringSubmatch(tag, -1) {
			found[match[1]] = html.UnescapeString(match[2])
		}
		return found
	}
	for _, tag := range inputTagPattern.FindAllString(markup, -1) {
		attrs := attributes(strings.TrimPrefix(tag, "<input"))
		name, present := attrs["name"]
		if !present || name == "" {
			continue
		}
		if attrs["type"] == "checkbox" {
			if _, checked := attrs["checked"]; !checked {
				continue
			}
			value, hasValue := attrs["value"]
			if !hasValue {
				value = "on"
			}
			values.Add(name, value)
			continue
		}
		values.Add(name, attrs["value"])
	}
	for _, match := range textareaTagPattern.FindAllStringSubmatch(markup, -1) {
		if name := attributes(match[1])["name"]; name != "" {
			values.Add(name, html.UnescapeString(match[2]))
		}
	}
	for _, match := range selectTagPattern.FindAllStringSubmatch(markup, -1) {
		name := attributes(match[1])["name"]
		options := optionTagPattern.FindAllString(match[2], -1)
		if name == "" || len(options) == 0 {
			continue
		}
		chosen := attributes(strings.TrimPrefix(options[0], "<option"))["value"]
		for _, option := range options {
			attrs := attributes(strings.TrimPrefix(option, "<option"))
			if _, selected := attrs["selected"]; selected {
				chosen = attrs["value"]
			}
		}
		values.Add(name, chosen)
	}
	return values
}

func templateEditorTestConfiguration() domain.TemplateConfiguration {
	uid, gid := int64(1000), int64(1001)
	return domain.TemplateConfiguration{
		Command: []string{"/bin/bash", "-l"},
		Environment: []domain.TemplateEnvironment{
			{Name: "LANG", Value: "en_US.UTF-8"},
			{Name: "API_TOKEN", Value: "Bearer {{cows.secret.api}} & <more>", Sensitive: true},
		},
		Secrets: []domain.TemplateSecret{
			{Name: "api", Value: `fixed "quoted" value`},
			{Name: "vnc", Generate: true, Length: 12},
		},
		Mounts: []domain.TemplateMount{
			// No type: the form shows it as the volume it is and saves it so.
			{Name: "home", ContainerPath: "/home/user"},
			{Name: "designs", Type: domain.TemplateMountDirectory, ContainerPath: "/foss/designs", ReadOnly: true, FileManager: true, NamePrefix: "workspace-", NameSuffix: "-data"},
		},
		Services: []domain.TemplateService{
			{Name: "desktop", Protocol: "tcp", ContainerPort: 5901, PortPool: "vnc", HostPortStart: 10000, HostPortEnd: 10099, PasswordSecret: "vnc"},
		},
		ContainerUser: &domain.TemplateContainerUser{UID: &uid, GID: &gid, Name: "{{cows.user.display_name}}", Home: "/home/{{cows.user.username}}", Shell: "/bin/bash"},
		TerminalUIDs:  []int64{1000, 0},
	}
}

func templateEditorTestInput(name string, configuration domain.TemplateConfiguration) workspace.TemplateInput {
	return workspace.TemplateInput{
		Name:                            name,
		ImageReference:                  "registry.example/research:1",
		DefaultCPUMillis:                1000,
		MaxCPUMillis:                    1000,
		DefaultMemoryBytes:              1024 << 20,
		MaxMemoryBytes:                  1024 << 20,
		InitialConnectionTimeoutSeconds: 3600,
		StoppedRetentionSeconds:         3600,
		Configuration:                   configuration,
		AccessMethods:                   []domain.AccessMethod{domain.AccessTerminal, domain.AccessDesktop, domain.AccessFiles},
		AllowedRoles:                    []domain.Role{domain.RoleUser},
		Enabled:                         true,
	}
}

func adminUser(t *testing.T, authService *auth.Service) domain.User {
	t.Helper()
	admin, _, err := authService.Authenticate(context.Background(), "admin", "changed correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	return admin
}

func adminRequest(t *testing.T, server *Server, session, csrf *http.Cookie, method, target string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	if values == nil {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	request.AddCookie(session)
	request.AddCookie(csrf)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// TestTemplateFormEditorRoundTripsAStoredConfiguration opens a stored template
// in the form editor and posts the rendered form back unchanged, as a browser
// would. The configuration must come back exactly as stored, apart from the
// one value the form spells differently with the same meaning: a mount without
// a type is saved as the volume it already was.
func TestTemplateFormEditorRoundTripsAStoredConfiguration(t *testing.T) {
	server, authService := testServer(t)
	session, csrf := adminSession(t, server, authService)
	ctx := context.Background()
	admin := adminUser(t, authService)
	created, err := server.workspace.CreateTemplate(ctx, admin.ID, templateEditorTestInput("Round trip", templateEditorTestConfiguration()))
	if err != nil {
		t.Fatalf("create template: %v", err)
	}

	page := adminRequest(t, server, session, csrf, http.MethodGet, "/admin/templates/"+created.ID+"/edit", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("edit page status = %d", page.Code)
	}
	form := formTagPattern.FindString(page.Body.String())
	values := browserFormValues(form)
	if values.Get("configuration_mode") != configurationModeForm {
		t.Fatalf("stored configuration did not open in the form editor: mode=%q", values.Get("configuration_mode"))
	}
	if strings.Contains(form, `name="configuration_json"`) {
		t.Fatal("the JSON control is rendered while the form editor is active")
	}
	if got := values.Get("config_mount_1_container_path"); got != "/foss/designs" {
		t.Fatalf("second mount path control = %q, want /foss/designs", got)
	}

	saved := adminRequest(t, server, session, csrf, http.MethodPost, "/admin/templates/"+created.ID, values)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("saving the unchanged form status = %d: %s", saved.Code, saved.Body.String())
	}
	stored, err := server.workspace.GetTemplate(ctx, admin.ID, created.ID)
	if err != nil {
		t.Fatalf("reload template: %v", err)
	}
	want := templateEditorTestConfiguration()
	want.Mounts[0].Type = domain.TemplateMountVolume
	if !reflect.DeepEqual(stored.Configuration, want) {
		t.Fatalf("configuration after a form round trip:\n got %s\nwant %s", configurationJSON(stored.Configuration), configurationJSON(want))
	}
}

// TestTemplateFormEditorMarksTheRowAtFault covers the reason the form editor
// exists: a rejected value is marked on its own row and control, and every
// broken one is marked from a single submission. The same submission made in
// the JSON editor lands on its one control.
func TestTemplateFormEditorMarksTheRowAtFault(t *testing.T) {
	server, authService := testServer(t)
	session, csrf := adminSession(t, server, authService)
	base := url.Values{
		"csrf_token":               {csrf.Value},
		"name":                     {"Broken rows"},
		"image_reference":          {"registry.example/research:1"},
		"default_cpu_millis":       {"1000"},
		"max_cpu_millis":           {"1000"},
		"default_memory_mib":       {"1024"},
		"max_memory_mib":           {"1024"},
		"initial_connection_hours": {"24"},
		"stopped_retention_hours":  {"24"},
		"access_methods":           {"terminal"},
		"allowed_roles":            {"user"},
	}
	form := url.Values{}
	for key, value := range base {
		form[key] = value
	}
	form.Set("configuration_mode", configurationModeForm)
	form.Set("config_env_0_name", "OK")
	form.Set("config_env_0_value", "1")
	form.Set("config_env_1_name", "1BAD")
	form.Set("config_env_1_value", "1")
	form.Set("config_mount_0_name", "home")
	form.Set("config_mount_0_type", "volume")
	form.Set("config_mount_0_container_path", "relative")
	form.Set("config_user_enabled", "on")
	form.Set("config_user_uid", "not a number")
	form.Set("config_user_gid", "1000")

	recorder := adminRequest(t, server, session, csrf, http.MethodPost, "/admin/templates", form)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	body := recorder.Body.String()
	for _, field := range []string{"config_env_1_name", "config_mount_0_container_path", "config_user_uid"} {
		if !strings.Contains(body, `aria-describedby="field-error-`+field+`"`) || !strings.Contains(body, `id="field-error-`+field+`"`) {
			t.Fatalf("control %q is not marked: %s", field, body)
		}
	}
	for _, field := range []string{"config_env_0_name", "config_env_0_value", "config_user_gid", "configuration_json"} {
		if strings.Contains(body, `id="field-error-`+field+`"`) {
			t.Fatalf("accepted control %q is marked: %s", field, body)
		}
	}
	if !strings.Contains(body, "3 fields need attention.") {
		t.Fatalf("summary does not count the three marks: %s", body)
	}
	if !strings.Contains(body, `name="config_user_uid" type="text" inputmode="numeric" value="not a number"`) {
		t.Fatalf("the unreadable value was not shown back as typed: %s", body)
	}

	jsonForm := url.Values{}
	for key, value := range base {
		jsonForm[key] = value
	}
	jsonForm.Set("configuration_mode", configurationModeJSON)
	jsonForm.Set("configuration_json", `{"environment":[{"name":"1BAD","value":"1"}],"mounts":[{"name":"home","container_path":"relative"}]}`)
	recorder = adminRequest(t, server, session, csrf, http.MethodPost, "/admin/templates", jsonForm)
	body = recorder.Body.String()
	if recorder.Code != http.StatusBadRequest || !strings.Contains(body, `id="field-error-configuration_json"`) {
		t.Fatalf("JSON editor rejection is not on its control: status=%d %s", recorder.Code, body)
	}
	if !strings.Contains(html.UnescapeString(body), `Environment name "1BAD"`) || strings.Contains(body, `field-error-config_env_0_name`) {
		t.Fatalf("JSON editor does not show the first configuration problem on its own control: %s", body)
	}
}

func postEditorAction(t *testing.T, server *Server, session, csrf *http.Cookie, values url.Values, action string) *httptest.ResponseRecorder {
	t.Helper()
	posted := url.Values{"csrf_token": {csrf.Value}, fieldConfigurationAction: {action}}
	for key, value := range values {
		posted[key] = value
	}
	return adminRequest(t, server, session, csrf, http.MethodPost, "/admin/templates/configuration-editor", posted)
}

// TestTemplateConfigurationEditorSwitchesThroughTheTypedValue switches a
// configuration from the form to JSON and back. Both directions go through
// the typed configuration on the server, so what comes back is the same
// configuration, and the editor that is not active is not rendered at all.
func TestTemplateConfigurationEditorSwitchesThroughTheTypedValue(t *testing.T) {
	server, authService := testServer(t)
	session, csrf := adminSession(t, server, authService)
	editor, err := formConfigurationEditor(templateEditorTestConfiguration())
	if err != nil {
		t.Fatalf("form editor: %v", err)
	}
	var markup strings.Builder
	if err := server.templates.ExecuteTemplate(&markup, "template-configuration-editor", pageData{Template: templateFormData{Configuration: editor}}); err != nil {
		t.Fatalf("render editor: %v", err)
	}

	toJSON := postEditorAction(t, server, session, csrf, browserFormValues(markup.String()), "mode:json")
	if toJSON.Code != http.StatusOK {
		t.Fatalf("switch to JSON status = %d: %s", toJSON.Code, toJSON.Body.String())
	}
	jsonValues := browserFormValues(toJSON.Body.String())
	if jsonValues.Get("configuration_mode") != configurationModeJSON || strings.Contains(toJSON.Body.String(), `name="config_`) {
		t.Fatalf("the JSON editor still renders form controls: %s", toJSON.Body.String())
	}
	var switched domain.TemplateConfiguration
	if err := decodeTemplateConfiguration(jsonValues.Get("configuration_json"), &switched); err != nil {
		t.Fatalf("decode switched JSON: %v", err)
	}
	want := templateEditorTestConfiguration()
	want.Mounts[0].Type = domain.TemplateMountVolume
	if !reflect.DeepEqual(switched, want) {
		t.Fatalf("configuration after switching to JSON:\n got %s\nwant %s", configurationJSON(switched), configurationJSON(want))
	}

	toForm := postEditorAction(t, server, session, csrf, jsonValues, "mode:form")
	formValues := browserFormValues(toForm.Body.String())
	if toForm.Code != http.StatusOK || formValues.Get("configuration_mode") != configurationModeForm {
		t.Fatalf("switch back to the form: status=%d %s", toForm.Code, toForm.Body.String())
	}
	back, err := configurationEditorFromForm(formValues).configuration()
	if err != nil || !reflect.DeepEqual(back, want) {
		t.Fatalf("configuration after switching back: err=%v\n got %s\nwant %s", err, configurationJSON(back), configurationJSON(want))
	}
}

// TestTemplateConfigurationEditorKeepsWhatItCannotConvert covers both refusals
// to switch: the current editor stays, with what was typed, and the reason is
// marked, so a switch can never silently drop part of a configuration.
func TestTemplateConfigurationEditorKeepsWhatItCannotConvert(t *testing.T) {
	server, authService := testServer(t)
	session, csrf := adminSession(t, server, authService)
	tests := []struct {
		name    string
		values  url.Values
		action  string
		mode    string
		field   string
		message string
	}{
		{"invalid JSON", url.Values{"configuration_mode": {"json"}, "configuration_json": {`{"mounts": [`}}, "mode:form", configurationModeJSON, "configuration_json", "not valid template JSON"},
		{"two services", url.Values{"configuration_mode": {"json"}, "configuration_json": {`{"services":[{"name":"desktop","protocol":"tcp"},{"name":"desktop","protocol":"tcp"}]}`}}, "mode:form", configurationModeJSON, "configuration_json", "more than one service"},
		{"line break in a value", url.Values{"configuration_mode": {"json"}, "configuration_json": {`{"environment":[{"name":"A","value":"one\ntwo"}]}`}}, "mode:form", configurationModeJSON, "configuration_json", "control character"},
		{"unreadable number", url.Values{"configuration_mode": {"form"}, "config_secret_0_name": {"vnc"}, "config_secret_0_length": {"twelve"}}, "mode:json", configurationModeForm, "config_secret_0_length", "whole number"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := postEditorAction(t, server, session, csrf, test.values, test.action)
			body := recorder.Body.String()
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", recorder.Code, body)
			}
			if got := browserFormValues(body).Get("configuration_mode"); got != test.mode {
				t.Fatalf("editor mode = %q, want %q", got, test.mode)
			}
			if !strings.Contains(body, `id="field-error-`+test.field+`"`) || !strings.Contains(body, test.message) {
				t.Fatalf("refusal is not marked on %q with %q: %s", test.field, test.message, body)
			}
		})
	}
}

// TestTemplateConfigurationEditorAddsAndRemovesRows covers the repeatable
// entries: a new row arrives focused, a removed row takes only its own values
// with it, and the rows after it are renumbered so the next submission still
// lines up with the validator's positions.
func TestTemplateConfigurationEditorAddsAndRemovesRows(t *testing.T) {
	server, authService := testServer(t)
	session, csrf := adminSession(t, server, authService)
	values := url.Values{
		"configuration_mode":     {"form"},
		"config_env_0_name":      {"FIRST"},
		"config_env_0_value":     {"1"},
		"config_env_1_name":      {"SECOND"},
		"config_env_1_value":     {"2"},
		"config_env_1_sensitive": {"on"},
	}
	added := postEditorAction(t, server, session, csrf, values, "add:env")
	if added.Code != http.StatusOK || !strings.Contains(added.Body.String(), `name="config_env_2_name" type="text" value="" maxlength="128" placeholder="LANG" spellcheck="false" autocomplete="off" autofocus`) {
		t.Fatalf("added row is missing or not focused: status=%d %s", added.Code, added.Body.String())
	}

	removed := postEditorAction(t, server, session, csrf, values, "remove:env:0")
	after := browserFormValues(removed.Body.String())
	if removed.Code != http.StatusOK || after.Get("config_env_0_name") != "SECOND" || after.Get("config_env_0_sensitive") != "on" || after.Has("config_env_1_name") {
		t.Fatalf("removing the first row did not leave the second as row 0: %v", after)
	}
	if !strings.Contains(removed.Body.String(), `id="config-env-add"`) || !strings.Contains(removed.Body.String(), `autofocus>Add variable`) {
		t.Fatalf("focus did not move to the list's add button: %s", removed.Body.String())
	}

	full := url.Values{"configuration_mode": {"form"}}
	for index := 0; index < workspace.MaxConfigurationMounts; index++ {
		full.Set("config_mount_"+strconv.Itoa(index)+"_name", "m"+strconv.Itoa(index))
	}
	if atCap := postEditorAction(t, server, session, csrf, full, "add:mount"); atCap.Code != http.StatusBadRequest {
		t.Fatalf("adding past the mount cap status = %d, want 400", atCap.Code)
	}
	for _, action := range []string{"add:volume", "remove:env:9", "remove:env:x", "drop:everything"} {
		if recorder := postEditorAction(t, server, session, csrf, values, action); recorder.Code != http.StatusBadRequest {
			t.Fatalf("action %q status = %d, want 400", action, recorder.Code)
		}
	}
}

// TestTemplateConfigurationEditorIsAdministratorOnly keeps the editor endpoint
// behind the same checks as every other administrator form post.
func TestTemplateConfigurationEditorIsAdministratorOnly(t *testing.T) {
	server, authService := testServer(t)
	session, csrf := adminSession(t, server, authService)
	ctx := context.Background()
	admin := adminUser(t, authService)

	missingToken := adminRequest(t, server, session, csrf, http.MethodPost, "/admin/templates/configuration-editor", url.Values{fieldConfigurationAction: {"mode:json"}})
	if missingToken.Code != http.StatusForbidden {
		t.Fatalf("editor action without a CSRF token = %d, want 403", missingToken.Code)
	}

	if _, err := authService.CreateUser(ctx, admin.ID, auth.CreateUserInput{Username: "student", Password: "another correct password", Role: domain.RoleUser}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	student, _, err := authService.Authenticate(ctx, "student", "another correct password")
	if err != nil {
		t.Fatalf("authenticate student: %v", err)
	}
	if err := authService.ChangePassword(ctx, student.ID, "another correct password", "changed student password"); err != nil {
		t.Fatalf("change student password: %v", err)
	}
	_, token, err := authService.Authenticate(ctx, "student", "changed student password")
	if err != nil {
		t.Fatalf("authenticate changed student: %v", err)
	}
	asStudent := adminRequest(t, server, &http.Cookie{Name: "cows_session", Value: token}, csrf, http.MethodPost, "/admin/templates/configuration-editor", url.Values{"csrf_token": {csrf.Value}, fieldConfigurationAction: {"mode:json"}})
	if asStudent.Code != http.StatusForbidden {
		t.Fatalf("non-administrator editor action = %d, want 403", asStudent.Code)
	}
}

// TestStoredConfigurationTheFormCannotHoldOpensAsJSON keeps a configuration
// the form has no controls for out of it, with the reason shown, instead of
// letting a save through the form rewrite it.
func TestStoredConfigurationTheFormCannotHoldOpensAsJSON(t *testing.T) {
	username := domain.TemplateConfiguration{ContainerUser: &domain.TemplateContainerUser{Username: "root"}}
	tests := []struct {
		name          string
		configuration domain.TemplateConfiguration
		reason        string
	}{
		{"second service", domain.TemplateConfiguration{Services: []domain.TemplateService{{Name: "desktop", Protocol: "tcp"}, {Name: "desktop", Protocol: "tcp"}}}, "more than one service"},
		{"other service", domain.TemplateConfiguration{Services: []domain.TemplateService{{Name: "web", Protocol: "tcp"}}}, `service "web"`},
		{"literal username", username, "username"},
		{"unknown mount type", domain.TemplateConfiguration{Mounts: []domain.TemplateMount{{Name: "data", Type: "bind"}}}, `type "bind"`},
		{"empty command argument", domain.TemplateConfiguration{Command: []string{"/bin/sh", ""}}, "command argument 2 is empty"},
		{"tab in a value", domain.TemplateConfiguration{Environment: []domain.TemplateEnvironment{{Name: "A", Value: "a\tb"}}}, "control character"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			editor := templateConfigurationEditorFor(test.configuration)
			if editor.IsForm() {
				t.Fatal("configuration opened in the form editor")
			}
			if !strings.Contains(editor.Notice, test.reason) {
				t.Fatalf("notice %q does not name %q", editor.Notice, test.reason)
			}
			var decoded domain.TemplateConfiguration
			if err := decodeTemplateConfiguration(editor.JSON, &decoded); err != nil || !reflect.DeepEqual(decoded, test.configuration) {
				t.Fatalf("JSON editor does not hold the stored configuration: err=%v %s", err, editor.JSON)
			}
		})
	}
	if editor := templateConfigurationEditorFor(domain.TemplateConfiguration{ContainerUser: &domain.TemplateContainerUser{Username: "{{cows.user.username}}"}}); !editor.IsForm() {
		t.Fatalf("the default username kept a configuration out of the form: %s", editor.Notice)
	}
}

// TestConfigurationEditorReadsRowsBackByPosition covers the parts of reading a
// submission that a browser never exercises: gaps, a non-canonical row number,
// and a list longer than its cap.
func TestConfigurationEditorReadsRowsBackByPosition(t *testing.T) {
	values := url.Values{
		"configuration_mode":    {"form"},
		"config_env_7_name":     {"SEVENTH"},
		"config_env_2_name":     {"SECOND"},
		"config_env_02_name":    {"NOT A ROW"},
		"config_env_-1_name":    {"NOT A ROW"},
		"config_env_99999_name": {"NOT A ROW"},
		"config_env_7_value":    {"7"},
	}
	editor := configurationEditorFromForm(values)
	if len(editor.Environment) != 2 || editor.Environment[0].Name != "SECOND" || editor.Environment[1].Name != "SEVENTH" || editor.Environment[1].Index != 1 || editor.Environment[1].Value != "7" {
		t.Fatalf("rows = %+v, want SECOND then SEVENTH renumbered 0 and 1", editor.Environment)
	}

	tooMany := url.Values{"configuration_mode": {"form"}}
	for index := 0; index < workspace.MaxConfigurationMounts+5; index++ {
		tooMany.Set("config_mount_"+strconv.Itoa(index)+"_name", "m"+strconv.Itoa(index))
	}
	editor = configurationEditorFromForm(tooMany)
	if len(editor.Mounts) != workspace.MaxConfigurationMounts+1 {
		t.Fatalf("read %d mount rows, want the cap plus one so validation rejects the list", len(editor.Mounts))
	}
	configuration, err := editor.configuration()
	if err != nil {
		t.Fatalf("read configuration: %v", err)
	}
	if err := workspace.ValidateTemplateInput(templateEditorTestInput("Too many", configuration)); domain.FieldMessages(err)[workspace.FieldConfigurationMounts] == "" {
		t.Fatalf("an over-long mount list was not rejected on the mount list: %v", err)
	}

	if got := commandArguments("/bin/bash\r\n-l\r\n\r\n  spaced  \n"); !reflect.DeepEqual(got, []string{"/bin/bash", "-l", "  spaced  "}) {
		t.Fatalf("command arguments = %q", got)
	}
	if uids, err := terminalUIDs(" 1000,0 ,  33 "); err != nil || !reflect.DeepEqual(uids, []int64{1000, 0, 33}) {
		t.Fatalf("terminal UIDs = %v err=%v", uids, err)
	}
	if _, err := terminalUIDs("1000, root"); err == nil {
		t.Fatal("a non-numeric terminal UID was accepted")
	}
}
