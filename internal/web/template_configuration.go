package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/workspace"
)

// This file holds the container-configuration part of the workspace-template
// form. The one typed domain.TemplateConfiguration has two editors: a form with
// a control per value and a row per repeated entry, and the JSON document the
// form was added beside. Only one of them is rendered at a time, and switching
// converts through the typed value on the server, so the two can never hold
// different configurations and no knowledge of the schema lives in the browser.

const (
	configurationModeForm = "form"
	configurationModeJSON = "json"

	fieldConfigurationMode   = "configuration_mode"
	fieldConfigurationAction = "configuration_action"
	fieldUserEnabled         = "config_user_enabled"
	fieldDesktopEnabled      = "config_desktop_enabled"

	// maxEntryIndex bounds the row numbers read back from a submission. The
	// editor numbers rows from zero and never offers more than a list's cap,
	// so a larger number can only come from a hand-made request.
	maxEntryIndex = 9999
)

// configurationEditor is what the configuration editor shows. Form-mode values
// are kept as the text that was submitted, so a rejected value is shown back as
// typed instead of as whatever it could be read as.
type configurationEditor struct {
	Mode         string
	JSON         string
	Command      string
	TerminalUIDs string
	Environment  []environmentRow
	Secrets      []secretRow
	Mounts       []mountRow
	User         containerUserBlock
	Desktop      desktopBlock
	// Notice says why a stored configuration opened as JSON.
	Notice string
	// Focus is the id of the control to focus after an editor action, so a
	// keyboard user lands on the row they just added.
	Focus string
}

type environmentRow struct {
	Index     int
	Name      string
	Value     string
	Sensitive bool
}

type secretRow struct {
	Index    int
	Name     string
	Value    string
	Generate bool
	Length   string
}

type mountRow struct {
	Index         int
	Name          string
	Type          string
	ContainerPath string
	ReadOnly      bool
	FileManager   bool
	NamePrefix    string
	NameSuffix    string
}

type containerUserBlock struct {
	Enabled     bool
	UID         string
	GID         string
	DisplayName string
	Home        string
	Shell       string
}

type desktopBlock struct {
	Enabled        bool
	ContainerPort  string
	PortPool       string
	HostPortStart  string
	HostPortEnd    string
	PasswordSecret string
}

// Field names a row's control for attribute; the same name the validator
// attributes a rejection of that value to.
func (r environmentRow) Field(attribute string) string {
	return workspace.ConfigurationEntryField(workspace.FieldConfigurationEnvironment, r.Index, attribute)
}

func (r secretRow) Field(attribute string) string {
	return workspace.ConfigurationEntryField(workspace.FieldConfigurationSecrets, r.Index, attribute)
}

func (r mountRow) Field(attribute string) string {
	return workspace.ConfigurationEntryField(workspace.FieldConfigurationMounts, r.Index, attribute)
}

func (e configurationEditor) IsForm() bool { return e.Mode == configurationModeForm }

func (e configurationEditor) CanAddEnvironment() bool {
	return len(e.Environment) < workspace.MaxConfigurationEnvironment
}

func (e configurationEditor) CanAddSecret() bool {
	return len(e.Secrets) < workspace.MaxConfigurationSecrets
}

func (e configurationEditor) CanAddMount() bool {
	return len(e.Mounts) < workspace.MaxConfigurationMounts
}

func (e configurationEditor) MaxEnvironment() int { return workspace.MaxConfigurationEnvironment }
func (e configurationEditor) MaxSecrets() int     { return workspace.MaxConfigurationSecrets }
func (e configurationEditor) MaxMounts() int      { return workspace.MaxConfigurationMounts }

// configurationEditorFromForm reads the editor back from a submission. A
// submission without a mode comes from a client that predates the form editor
// and posts the JSON control only, so it is read as JSON.
func configurationEditorFromForm(values url.Values) configurationEditor {
	editor := configurationEditor{Mode: configurationModeJSON, JSON: values.Get(workspace.FieldTemplateConfiguration)}
	if values.Get(fieldConfigurationMode) != configurationModeForm {
		return editor
	}
	editor = configurationEditor{
		Mode:         configurationModeForm,
		Command:      values.Get(workspace.FieldConfigurationCommand),
		TerminalUIDs: values.Get(workspace.FieldConfigurationTerminalUIDs),
		User: containerUserBlock{
			Enabled:     values.Get(fieldUserEnabled) == "on",
			UID:         values.Get(workspace.FieldConfigurationUserUID),
			GID:         values.Get(workspace.FieldConfigurationUserGID),
			DisplayName: values.Get(workspace.FieldConfigurationUserDisplayName),
			Home:        values.Get(workspace.FieldConfigurationUserHome),
			Shell:       values.Get(workspace.FieldConfigurationUserShell),
		},
		Desktop: desktopBlock{
			Enabled:        values.Get(fieldDesktopEnabled) == "on",
			ContainerPort:  values.Get(workspace.FieldConfigurationDesktopContainerPort),
			PortPool:       values.Get(workspace.FieldConfigurationDesktopPortPool),
			HostPortStart:  values.Get(workspace.FieldConfigurationDesktopHostPortStart),
			HostPortEnd:    values.Get(workspace.FieldConfigurationDesktopHostPortEnd),
			PasswordSecret: values.Get(workspace.FieldConfigurationDesktopPasswordSecret),
		},
	}
	// Rows are renumbered in submitted order, so the validator's position for
	// an entry is also the number its re-rendered controls carry.
	for position, index := range entryIndexes(values, workspace.FieldConfigurationEnvironment, workspace.MaxConfigurationEnvironment) {
		submitted := environmentRow{Index: index}
		editor.Environment = append(editor.Environment, environmentRow{
			Index:     position,
			Name:      values.Get(submitted.Field(workspace.EntryName)),
			Value:     values.Get(submitted.Field(workspace.EntryValue)),
			Sensitive: values.Get(submitted.Field(workspace.EntrySensitive)) == "on",
		})
	}
	for position, index := range entryIndexes(values, workspace.FieldConfigurationSecrets, workspace.MaxConfigurationSecrets) {
		submitted := secretRow{Index: index}
		editor.Secrets = append(editor.Secrets, secretRow{
			Index:    position,
			Name:     values.Get(submitted.Field(workspace.EntryName)),
			Value:    values.Get(submitted.Field(workspace.EntryValue)),
			Generate: values.Get(submitted.Field(workspace.EntryGenerate)) == "on",
			Length:   values.Get(submitted.Field(workspace.EntryLength)),
		})
	}
	for position, index := range entryIndexes(values, workspace.FieldConfigurationMounts, workspace.MaxConfigurationMounts) {
		submitted := mountRow{Index: index}
		editor.Mounts = append(editor.Mounts, mountRow{
			Index:         position,
			Name:          values.Get(submitted.Field(workspace.EntryName)),
			Type:          values.Get(submitted.Field(workspace.EntryType)),
			ContainerPath: values.Get(submitted.Field(workspace.EntryContainerPath)),
			ReadOnly:      values.Get(submitted.Field(workspace.EntryReadOnly)) == "on",
			FileManager:   values.Get(submitted.Field(workspace.EntryFileManager)) == "on",
			NamePrefix:    values.Get(submitted.Field(workspace.EntryNamePrefix)),
			NameSuffix:    values.Get(submitted.Field(workspace.EntryNameSuffix)),
		})
	}
	return editor
}

// entryIndexes finds the submitted rows of one list by their always-submitted
// name control and returns their numbers in order. One row past the cap is
// kept so validation still sees, and rejects, an over-long list.
func entryIndexes(values url.Values, list string, limit int) []int {
	prefix, suffix := list+"_", "_"+workspace.EntryName
	indexes := make([]int, 0)
	for key := range values {
		digits, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		digits, ok = strings.CutSuffix(digits, suffix)
		if !ok {
			continue
		}
		index, err := strconv.Atoi(digits)
		// Only the canonical spelling counts, so "01" cannot read row 1 twice.
		if err != nil || index < 0 || index > maxEntryIndex || strconv.Itoa(index) != digits {
			continue
		}
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	if len(indexes) > limit+1 {
		indexes = indexes[:limit+1]
	}
	return indexes
}

// configuration reads the typed configuration the editor describes. A value
// that cannot be read is reported on its own control, and the configuration
// returned alongside still holds everything else, so the caller can validate
// it and report every problem from one submission together.
func (e configurationEditor) configuration() (domain.TemplateConfiguration, error) {
	configuration := domain.TemplateConfiguration{}
	if e.Mode != configurationModeForm {
		if strings.TrimSpace(e.JSON) == "" {
			return configuration, nil
		}
		if err := decodeTemplateConfiguration(e.JSON, &configuration); err != nil {
			// The decoder's own message names the offending position or key,
			// which is the only way to locate a mistake inside a document this
			// large. It describes the administrator's own submitted text, not
			// a runtime or storage failure.
			return domain.TemplateConfiguration{}, domain.InvalidField(workspace.ErrInvalidTemplate, workspace.FieldTemplateConfiguration, "This is not valid template JSON: "+err.Error())
		}
		return configuration, nil
	}
	problems := make([]error, 0, 2)
	rejected := func(field, message string) {
		problems = append(problems, domain.InvalidField(workspace.ErrInvalidTemplate, field, message))
	}
	configuration.Command = commandArguments(e.Command)
	uids, err := terminalUIDs(e.TerminalUIDs)
	if err != nil {
		rejected(workspace.FieldConfigurationTerminalUIDs, "Enter whole-number UIDs separated by commas, such as 1000, 0.")
	}
	configuration.TerminalUIDs = uids
	for _, row := range e.Environment {
		configuration.Environment = append(configuration.Environment, domain.TemplateEnvironment{Name: row.Name, Value: row.Value, Sensitive: row.Sensitive})
	}
	for _, row := range e.Secrets {
		length, err := optionalWholeNumber(row.Length)
		if err != nil {
			rejected(row.Field(workspace.EntryLength), "Enter a whole number, or leave this empty.")
		}
		configuration.Secrets = append(configuration.Secrets, domain.TemplateSecret{Name: row.Name, Value: row.Value, Generate: row.Generate, Length: length})
	}
	for _, row := range e.Mounts {
		configuration.Mounts = append(configuration.Mounts, domain.TemplateMount{Name: row.Name, Type: row.Type, ContainerPath: row.ContainerPath, ReadOnly: row.ReadOnly, FileManager: row.FileManager, NamePrefix: row.NamePrefix, NameSuffix: row.NameSuffix})
	}
	if e.User.Enabled {
		user := &domain.TemplateContainerUser{Name: e.User.DisplayName, Home: e.User.Home, Shell: e.User.Shell}
		for _, id := range []struct {
			field  string
			value  string
			target **int64
		}{
			{workspace.FieldConfigurationUserUID, e.User.UID, &user.UID},
			{workspace.FieldConfigurationUserGID, e.User.GID, &user.GID},
		} {
			if strings.TrimSpace(id.value) == "" {
				continue
			}
			parsed, err := strconv.ParseInt(strings.TrimSpace(id.value), 10, 64)
			if err != nil {
				rejected(id.field, "Enter a whole number.")
				continue
			}
			*id.target = &parsed
		}
		configuration.ContainerUser = user
	}
	if e.Desktop.Enabled {
		service := domain.TemplateService{Name: "desktop", Protocol: "tcp", PortPool: e.Desktop.PortPool, PasswordSecret: e.Desktop.PasswordSecret}
		for _, port := range []struct {
			field  string
			value  string
			target *int
		}{
			{workspace.FieldConfigurationDesktopContainerPort, e.Desktop.ContainerPort, &service.ContainerPort},
			{workspace.FieldConfigurationDesktopHostPortStart, e.Desktop.HostPortStart, &service.HostPortStart},
			{workspace.FieldConfigurationDesktopHostPortEnd, e.Desktop.HostPortEnd, &service.HostPortEnd},
		} {
			parsed, err := optionalWholeNumber(port.value)
			if err != nil {
				rejected(port.field, "Enter a whole number.")
			}
			*port.target = parsed
		}
		configuration.Services = []domain.TemplateService{service}
	}
	return configuration, domain.InvalidFields(problems...)
}

// commandArguments splits the one-argument-per-line command control. Empty
// lines are skipped, so a trailing newline adds no empty argument. Every other
// line is kept exactly, spaces included, as the JSON editor would keep it.
func commandArguments(text string) []string {
	var arguments []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line != "" {
			arguments = append(arguments, line)
		}
	}
	return arguments
}

func terminalUIDs(text string) ([]int64, error) {
	var uids []int64
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		uid, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, nil
}

// optionalWholeNumber reads a number control where empty means zero, which is
// what leaving the key out of the JSON means.
func optionalWholeNumber(text string) (int, error) {
	if strings.TrimSpace(text) == "" {
		return 0, nil
	}
	return strconv.Atoi(strings.TrimSpace(text))
}

// formConfigurationEditor lays a configuration out as the form editor. It
// refuses, with the reason, a configuration the form cannot hold exactly:
// otherwise merely opening and saving a template could rewrite part of its
// configuration without the administrator having changed it. The two values
// the form does not keep, an explicit "{{cows.user.username}}" username and a
// blank mount type, mean exactly what the form saves instead.
func formConfigurationEditor(configuration domain.TemplateConfiguration) (configurationEditor, error) {
	if err := formCanHold(configuration); err != nil {
		return configurationEditor{}, err
	}
	editor := configurationEditor{Mode: configurationModeForm, Command: strings.Join(configuration.Command, "\n")}
	uids := make([]string, 0, len(configuration.TerminalUIDs))
	for _, uid := range configuration.TerminalUIDs {
		uids = append(uids, strconv.FormatInt(uid, 10))
	}
	editor.TerminalUIDs = strings.Join(uids, ", ")
	for index, value := range configuration.Environment {
		editor.Environment = append(editor.Environment, environmentRow{Index: index, Name: value.Name, Value: value.Value, Sensitive: value.Sensitive})
	}
	for index, value := range configuration.Secrets {
		length := ""
		if value.Length != 0 {
			length = strconv.Itoa(value.Length)
		}
		editor.Secrets = append(editor.Secrets, secretRow{Index: index, Name: value.Name, Value: value.Value, Generate: value.Generate, Length: length})
	}
	for index, value := range configuration.Mounts {
		mountType := value.Type
		if mountType == "" {
			mountType = domain.TemplateMountVolume
		}
		editor.Mounts = append(editor.Mounts, mountRow{Index: index, Name: value.Name, Type: mountType, ContainerPath: value.ContainerPath, ReadOnly: value.ReadOnly, FileManager: value.FileManager, NamePrefix: value.NamePrefix, NameSuffix: value.NameSuffix})
	}
	if user := configuration.ContainerUser; user != nil {
		editor.User = containerUserBlock{Enabled: true, UID: optionalID(user.UID), GID: optionalID(user.GID), DisplayName: user.Name, Home: user.Home, Shell: user.Shell}
	}
	if len(configuration.Services) == 1 {
		service := configuration.Services[0]
		editor.Desktop = desktopBlock{Enabled: true, ContainerPort: strconv.Itoa(service.ContainerPort), PortPool: service.PortPool, HostPortStart: strconv.Itoa(service.HostPortStart), HostPortEnd: strconv.Itoa(service.HostPortEnd), PasswordSecret: service.PasswordSecret}
	}
	return editor, nil
}

func optionalID(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

// formCanHold explains what in configuration the form editor has no control
// for. Every reason describes the administrator's own configuration.
func formCanHold(configuration domain.TemplateConfiguration) error {
	if len(configuration.Services) > 1 {
		return errors.New("it defines more than one service, and the form offers only the desktop service")
	}
	for _, service := range configuration.Services {
		if service.Name != "desktop" || service.Protocol != "tcp" {
			return fmt.Errorf("it defines service %q over %q, and the form offers only the TCP desktop service", service.Name, service.Protocol)
		}
	}
	if user := configuration.ContainerUser; user != nil && user.Username != "" && user.Username != "{{cows.user.username}}" {
		return errors.New("its container user sets a username other than {{cows.user.username}}")
	}
	for _, mount := range configuration.Mounts {
		if mount.Type != "" && mount.Type != domain.TemplateMountVolume && mount.Type != domain.TemplateMountDirectory {
			return fmt.Errorf("mount %q has the type %q, and the form offers only volume and directory", mount.Name, mount.Type)
		}
	}
	for index, argument := range configuration.Command {
		if argument == "" {
			return fmt.Errorf("command argument %d is empty, and the form shows one argument per line", index+1)
		}
	}
	// A form control cannot hold a line break, and the browser rewrites other
	// control characters; none of them is valid in any configuration value.
	for _, value := range configurationText(configuration) {
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return errors.New("one of its values contains a line break or another control character")
		}
	}
	return nil
}

func configurationText(configuration domain.TemplateConfiguration) []string {
	text := append([]string(nil), configuration.Command...)
	for _, value := range configuration.Environment {
		text = append(text, value.Name, value.Value)
	}
	for _, value := range configuration.Secrets {
		text = append(text, value.Name, value.Value)
	}
	for _, value := range configuration.Mounts {
		text = append(text, value.Name, value.Type, value.ContainerPath, value.NamePrefix, value.NameSuffix)
	}
	for _, value := range configuration.Services {
		text = append(text, value.PortPool, value.PasswordSecret)
	}
	if user := configuration.ContainerUser; user != nil {
		text = append(text, user.Username, user.Name, user.Home, user.Shell)
	}
	return text
}

// templateConfigurationEditorFor is the editor a stored configuration opens
// in: the form when it can hold the configuration exactly, otherwise JSON with
// a notice saying why.
func templateConfigurationEditorFor(configuration domain.TemplateConfiguration) configurationEditor {
	editor, err := formConfigurationEditor(configuration)
	if err == nil {
		return editor
	}
	return configurationEditor{Mode: configurationModeJSON, JSON: configurationJSON(configuration), Notice: "This configuration opens as JSON because " + err.Error() + "."}
}

func configurationJSON(configuration domain.TemplateConfiguration) string {
	encoded, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil || len(encoded) == 0 {
		return "{}"
	}
	return string(encoded)
}

// control names the control that shows a rejection of field. The JSON editor
// has a single control for the whole configuration, so every configuration
// field is shown there. The form editor has one per value; a field none of
// its rendered controls carries is shown on the JSON control's mark at the top
// of the form editor, so a rejection is never counted without being shown.
func (e configurationEditor) control(field string) string {
	if !strings.HasPrefix(field, workspace.FieldConfigurationPrefix) {
		return field
	}
	if e.Mode != configurationModeForm || !e.renders(field) {
		return workspace.FieldTemplateConfiguration
	}
	return field
}

// renders reports whether the form editor marks field on one of its controls.
func (e configurationEditor) renders(field string) bool {
	switch field {
	case workspace.FieldConfigurationCommand, workspace.FieldConfigurationTerminalUIDs,
		workspace.FieldConfigurationEnvironment, workspace.FieldConfigurationSecrets, workspace.FieldConfigurationMounts:
		return true
	case workspace.FieldConfigurationUser, workspace.FieldConfigurationUserUID, workspace.FieldConfigurationUserGID,
		workspace.FieldConfigurationUserDisplayName, workspace.FieldConfigurationUserHome, workspace.FieldConfigurationUserShell:
		return e.User.Enabled
	case workspace.FieldConfigurationDesktop, workspace.FieldConfigurationDesktopContainerPort, workspace.FieldConfigurationDesktopPortPool,
		workspace.FieldConfigurationDesktopHostPortStart, workspace.FieldConfigurationDesktopHostPortEnd, workspace.FieldConfigurationDesktopPasswordSecret:
		return e.Desktop.Enabled
	}
	for _, row := range e.Environment {
		if field == row.Field(workspace.EntryName) || field == row.Field(workspace.EntryValue) {
			return true
		}
	}
	for _, row := range e.Secrets {
		if field == row.Field(workspace.EntryName) || field == row.Field(workspace.EntryValue) || field == row.Field(workspace.EntryLength) {
			return true
		}
	}
	for _, row := range e.Mounts {
		for _, attribute := range []string{workspace.EntryName, workspace.EntryType, workspace.EntryContainerPath, workspace.EntryNamePrefix, workspace.EntryNameSuffix} {
			if field == row.Field(attribute) {
				return true
			}
		}
	}
	return false
}

// errEditorAction rejects an action the editor never offers in its current
// state, which can only come from a hand-made request.
var errEditorAction = errors.New("unsupported configuration editor action")

// apply performs one editor action and returns the editor to render with the
// marks it produced. Switching to JSON needs every form value to be readable,
// and switching to the form needs JSON the form can hold; either refusal keeps
// the current editor and marks why, so nothing typed is lost.
func (e configurationEditor) apply(action string) (configurationEditor, map[string]string, error) {
	switch action {
	case "mode:json":
		if e.Mode != configurationModeForm {
			return e, nil, nil
		}
		configuration, err := e.configuration()
		if err != nil {
			return e, domain.FieldMessagesOn(err, e.control), nil
		}
		return configurationEditor{Mode: configurationModeJSON, JSON: configurationJSON(configuration)}, nil, nil
	case "mode:form":
		if e.Mode == configurationModeForm {
			return e, nil, nil
		}
		configuration, err := e.configuration()
		if err != nil {
			return e, domain.FieldMessages(err), nil
		}
		editor, err := formConfigurationEditor(configuration)
		if err != nil {
			return e, map[string]string{workspace.FieldTemplateConfiguration: "The form editor cannot show this configuration because " + err.Error() + ". Change that here first, or keep editing it as JSON."}, nil
		}
		return editor, nil, nil
	}
	if e.Mode != configurationModeForm {
		return e, nil, errEditorAction
	}
	switch action {
	case "add:env":
		if !e.CanAddEnvironment() {
			return e, nil, errEditorAction
		}
		row := environmentRow{Index: len(e.Environment)}
		e.Environment = append(e.Environment, row)
		e.Focus = row.Field(workspace.EntryName)
		return e, nil, nil
	case "add:secret":
		if !e.CanAddSecret() {
			return e, nil, errEditorAction
		}
		row := secretRow{Index: len(e.Secrets)}
		e.Secrets = append(e.Secrets, row)
		e.Focus = row.Field(workspace.EntryName)
		return e, nil, nil
	case "add:mount":
		if !e.CanAddMount() {
			return e, nil, errEditorAction
		}
		row := mountRow{Index: len(e.Mounts), Type: domain.TemplateMountVolume}
		e.Mounts = append(e.Mounts, row)
		e.Focus = row.Field(workspace.EntryName)
		return e, nil, nil
	}
	list, digits, ok := strings.Cut(strings.TrimPrefix(action, "remove:"), ":")
	index, err := strconv.Atoi(digits)
	if !strings.HasPrefix(action, "remove:") || !ok || err != nil || index < 0 {
		return e, nil, errEditorAction
	}
	switch list {
	case "env":
		if index >= len(e.Environment) {
			return e, nil, errEditorAction
		}
		e.Environment = append(e.Environment[:index:index], e.Environment[index+1:]...)
		for position := range e.Environment {
			e.Environment[position].Index = position
		}
	case "secret":
		if index >= len(e.Secrets) {
			return e, nil, errEditorAction
		}
		e.Secrets = append(e.Secrets[:index:index], e.Secrets[index+1:]...)
		for position := range e.Secrets {
			e.Secrets[position].Index = position
		}
	case "mount":
		if index >= len(e.Mounts) {
			return e, nil, errEditorAction
		}
		e.Mounts = append(e.Mounts[:index:index], e.Mounts[index+1:]...)
		for position := range e.Mounts {
			e.Mounts[position].Index = position
		}
	default:
		return e, nil, errEditorAction
	}
	// The removed row's controls are gone, so focus moves to the list's own
	// add button rather than falling back to the top of the page.
	e.Focus = "config-" + list + "-add"
	return e, nil, nil
}

// adminTemplateConfigurationEditor applies one editor action (switching
// between the form and JSON, or adding or removing a row) to the editor as
// submitted and answers with the re-rendered editor. It stores nothing: saving
// still goes through the template form and the template service, which
// validate the result as for any other submission.
func (s *Server) adminTemplateConfigurationEditor(w http.ResponseWriter, r *http.Request) {
	user, ok := s.adminForm(w, r)
	if !ok {
		return
	}
	editor, fields, err := configurationEditorFromForm(r.Form).apply(r.FormValue(fieldConfigurationAction))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	form := templateFormData{Configuration: editor, FieldErrors: fields}
	if configuration, err := editor.configuration(); err == nil {
		form.TerminalRootAllowed = hasTerminalUID(configuration, 0)
	}
	s.render(w, http.StatusOK, "template-configuration-editor", pageData{User: &user, Template: form})
}
