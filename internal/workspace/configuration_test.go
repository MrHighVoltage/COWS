package workspace

import (
	"errors"
	"testing"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/runtime"
)

// assertFieldMarked checks that a rejection names the form control the
// administration form should mark, which is the whole point of attributing
// validation failures to fields.
func assertFieldMarked(t *testing.T, err error, fields ...string) {
	t.Helper()
	messages := domain.FieldMessages(err)
	for _, field := range fields {
		message, marked := messages[field]
		if !marked {
			t.Fatalf("error %v marks %v, want a mark on %q", err, domain.FieldNames(messages), field)
		}
		if message == "" {
			t.Fatalf("field %q is marked without a message", field)
		}
	}
}

func TestResolveConfigurationBuildsContainerUserFromCOWSUser(t *testing.T) {
	uid, gid := int64(1000), int64(1001)
	configuration := domain.TemplateConfiguration{
		Command: []string{"{{cows.user.username}}", "{{cows.user.display_name}}"},
		ContainerUser: &domain.TemplateContainerUser{
			UID:   &uid,
			GID:   &gid,
			Home:  "/home/{{cows.user.username}}",
			Shell: "/bin/bash",
		},
	}
	user := domain.User{Username: "alice", DisplayName: "Alice Researcher"}
	resolved, err := resolveConfiguration(configuration, user, "workspace-1", "Desktop", nil, nil)
	if err != nil {
		t.Fatalf("resolve configuration: %v", err)
	}
	if resolved.User == nil {
		t.Fatal("resolved container user is nil")
	}
	if resolved.User.Username != "alice" || resolved.User.UID != 1000 || resolved.User.GID != 1001 || resolved.User.Home != "/home/alice" || resolved.User.Shell != "/bin/bash" {
		t.Fatalf("resolved container user = %+v", resolved.User)
	}
	if resolved.User.PasswdEntry != "alice:x:1000:1001:Alice Researcher:/home/alice:/bin/bash" {
		t.Fatalf("passwd entry = %q", resolved.User.PasswdEntry)
	}
	if len(resolved.Command) != 2 || resolved.Command[0] != "alice" || resolved.Command[1] != "Alice Researcher" {
		t.Fatalf("resolved command = %#v", resolved.Command)
	}
}

func TestTemplateConfigurationRequiresContainerIDs(t *testing.T) {
	err := validateTemplateConfiguration(domain.TemplateConfiguration{ContainerUser: &domain.TemplateContainerUser{}})
	if !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("missing container IDs error = %v", err)
	}
	assertFieldMarked(t, err, FieldConfigurationUserUID, FieldConfigurationUserGID)
}

func TestTemplateConfigurationValidatesTerminalUIDs(t *testing.T) {
	if err := validateTemplateConfiguration(domain.TemplateConfiguration{TerminalUIDs: []int64{1000, 0}}); err != nil {
		t.Fatalf("valid terminal UIDs rejected: %v", err)
	}
	for _, value := range [][]int64{{-1}, {2147483648}, {1000, 1000}} {
		err := validateTemplateConfiguration(domain.TemplateConfiguration{TerminalUIDs: value})
		if !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("terminal UIDs %v error = %v, want %v", value, err, ErrInvalidTemplate)
		}
		assertFieldMarked(t, err, FieldConfigurationTerminalUIDs)
	}
}

func TestTemplateConfigurationAcceptsSeparatorMountSuffix(t *testing.T) {
	err := validateTemplateConfiguration(domain.TemplateConfiguration{Mounts: []domain.TemplateMount{{
		Name: "designs", Type: domain.TemplateMountDirectory, ContainerPath: "/foss/designs",
		NamePrefix: "workspace-", NameSuffix: "-data", FileManager: true,
	}}})
	if err != nil {
		t.Fatalf("separator mount suffix error = %v", err)
	}
}

func TestTemplateConfigurationOnlyAcceptsDesktopService(t *testing.T) {
	valid := domain.TemplateService{Name: "desktop", Protocol: "tcp", ContainerPort: 5901, PortPool: "vnc", HostPortStart: 10000, HostPortEnd: 10099}
	if err := validateTemplateConfiguration(domain.TemplateConfiguration{Services: []domain.TemplateService{valid}}); err != nil {
		t.Fatalf("desktop service error = %v", err)
	}
	for _, service := range []domain.TemplateService{
		{Name: "web", Protocol: "tcp", ContainerPort: 8080, PortPool: "web", HostPortStart: 10000, HostPortEnd: 10099},
		{Name: "desktop", Protocol: "udp", ContainerPort: 5901, PortPool: "vnc", HostPortStart: 10000, HostPortEnd: 10099},
	} {
		err := validateTemplateConfiguration(domain.TemplateConfiguration{Services: []domain.TemplateService{service}})
		if !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("service %+v error = %v, want %v", service, err, ErrInvalidTemplate)
		}
		assertFieldMarked(t, err, FieldConfigurationDesktop)
	}
}

// TestTemplateConfigurationMarksTheEntryAtFault covers what the form editor
// relies on: a rejection names the control of the exact entry and attribute
// that is wrong, so the editor can mark that one input among many rows.
func TestTemplateConfigurationMarksTheEntryAtFault(t *testing.T) {
	uid, gid := int64(1000), int64(1000)
	desktop := domain.TemplateService{Name: "desktop", Protocol: "tcp", ContainerPort: 5901, PortPool: "vnc", HostPortStart: 10000, HostPortEnd: 10099}
	validMount := domain.TemplateMount{Name: "home", Type: domain.TemplateMountVolume, ContainerPath: "/home/user"}
	tests := []struct {
		name          string
		configuration domain.TemplateConfiguration
		field         string
	}{
		{"environment name", domain.TemplateConfiguration{Environment: []domain.TemplateEnvironment{{Name: "OK", Value: "1"}, {Name: "1BAD", Value: "1"}}}, "config_env_1_name"},
		{"duplicate environment name", domain.TemplateConfiguration{Environment: []domain.TemplateEnvironment{{Name: "SAME"}, {Name: "SAME"}}}, "config_env_1_name"},
		{"environment placeholder", domain.TemplateConfiguration{Environment: []domain.TemplateEnvironment{{Name: "PORT", Value: "{{cows.service.web.port}}"}}}, "config_env_0_value"},
		{"secret without a value", domain.TemplateConfiguration{Secrets: []domain.TemplateSecret{{Name: "token"}}}, "config_secret_0_value"},
		{"short generated secret", domain.TemplateConfiguration{Secrets: []domain.TemplateSecret{{Name: "vnc", Generate: true, Length: 4}}}, "config_secret_0_length"},
		{"fixed secret with a length", domain.TemplateConfiguration{Secrets: []domain.TemplateSecret{{Name: "vnc", Value: "fixed", Length: 8}}}, "config_secret_0_length"},
		{"relative mount path", domain.TemplateConfiguration{Mounts: []domain.TemplateMount{validMount, {Name: "data", ContainerPath: "data"}}}, "config_mount_1_container_path"},
		{"unknown mount type", domain.TemplateConfiguration{Mounts: []domain.TemplateMount{{Name: "data", Type: "bind", ContainerPath: "/data"}}}, "config_mount_0_type"},
		{"mount prefix", domain.TemplateConfiguration{Mounts: []domain.TemplateMount{{Name: "data", ContainerPath: "/data", NamePrefix: "Bad/"}}}, "config_mount_0_name_prefix"},
		{"mount suffix", domain.TemplateConfiguration{Mounts: []domain.TemplateMount{{Name: "data", ContainerPath: "/data", NameSuffix: ".."}}}, "config_mount_0_name_suffix"},
		{"duplicate mount", domain.TemplateConfiguration{Mounts: []domain.TemplateMount{validMount, validMount}}, "config_mount_1_name"},
		{"colliding storage name", domain.TemplateConfiguration{Mounts: []domain.TemplateMount{{Name: "a-data", ContainerPath: "/one"}, {Name: "a", NameSuffix: "-data", ContainerPath: "/two"}}}, "config_mount_1_name"},
		{"command argument", domain.TemplateConfiguration{Command: []string{"/bin/bash", ""}}, FieldConfigurationCommand},
		{"container user home", domain.TemplateConfiguration{ContainerUser: &domain.TemplateContainerUser{UID: &uid, GID: &gid, Home: "relative"}}, FieldConfigurationUserHome},
		{"container user shell", domain.TemplateConfiguration{ContainerUser: &domain.TemplateContainerUser{UID: &uid, GID: &gid, Shell: "/bin/../sh"}}, FieldConfigurationUserShell},
		{"container user display name", domain.TemplateConfiguration{ContainerUser: &domain.TemplateContainerUser{UID: &uid, GID: &gid, Name: "a:b"}}, FieldConfigurationUserDisplayName},
		{"container user username", domain.TemplateConfiguration{ContainerUser: &domain.TemplateContainerUser{UID: &uid, GID: &gid, Username: "root"}}, FieldConfigurationUser},
		{"desktop container port", domain.TemplateConfiguration{Services: []domain.TemplateService{{Name: "desktop", Protocol: "tcp", PortPool: "vnc", HostPortStart: 10000, HostPortEnd: 10099}}}, FieldConfigurationDesktopContainerPort},
		{"desktop first host port", domain.TemplateConfiguration{Services: []domain.TemplateService{{Name: "desktop", Protocol: "tcp", ContainerPort: 5901, PortPool: "vnc", HostPortStart: 80, HostPortEnd: 10099}}}, FieldConfigurationDesktopHostPortStart},
		{"desktop host range downwards", domain.TemplateConfiguration{Services: []domain.TemplateService{{Name: "desktop", Protocol: "tcp", ContainerPort: 5901, PortPool: "vnc", HostPortStart: 10099, HostPortEnd: 10000}}}, FieldConfigurationDesktopHostPortEnd},
		{"desktop port pool", domain.TemplateConfiguration{Services: []domain.TemplateService{{Name: "desktop", Protocol: "tcp", ContainerPort: 5901, PortPool: "VNC", HostPortStart: 10000, HostPortEnd: 10099}}}, FieldConfigurationDesktopPortPool},
		{"desktop password secret", domain.TemplateConfiguration{Services: []domain.TemplateService{{Name: "desktop", Protocol: "tcp", ContainerPort: 5901, PortPool: "vnc", HostPortStart: 10000, HostPortEnd: 10099, PasswordSecret: "missing"}}}, FieldConfigurationDesktopPasswordSecret},
		{"too many mounts", domain.TemplateConfiguration{Mounts: make([]domain.TemplateMount, 17)}, FieldConfigurationMounts},
		{"duplicate desktop", domain.TemplateConfiguration{Services: []domain.TemplateService{desktop, desktop}}, FieldConfigurationDesktop},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTemplateConfiguration(test.configuration)
			if !errors.Is(err, ErrInvalidTemplate) {
				t.Fatalf("error = %v, want %v", err, ErrInvalidTemplate)
			}
			assertFieldMarked(t, err, test.field)
		})
	}
}

// TestTemplateConfigurationReportsEveryEntryAtOnce keeps the form editor from
// revealing one broken row per submission.
func TestTemplateConfigurationReportsEveryEntryAtOnce(t *testing.T) {
	err := validateTemplateConfiguration(domain.TemplateConfiguration{
		Environment:  []domain.TemplateEnvironment{{Name: "1BAD"}},
		Secrets:      []domain.TemplateSecret{{Name: "token"}},
		Mounts:       []domain.TemplateMount{{Name: "data", ContainerPath: "relative"}},
		TerminalUIDs: []int64{-1},
	})
	assertFieldMarked(t, err, "config_env_0_name", "config_secret_0_value", "config_mount_0_container_path", FieldConfigurationTerminalUIDs)
	if marks := domain.FieldMessages(err); len(marks) != 4 {
		t.Fatalf("marks = %v, want exactly the four broken controls", domain.FieldNames(marks))
	}
}

func TestResolveConfigurationWithoutContainerUserLeavesRuntimeUserUnset(t *testing.T) {
	resolved, err := resolveConfiguration(domain.TemplateConfiguration{}, domain.User{Username: "alice"}, "workspace-1", "Desktop", nil, nil)
	if err != nil {
		t.Fatalf("resolve configuration: %v", err)
	}
	if resolved.User != nil {
		t.Fatalf("runtime user = %+v, want nil", resolved.User)
	}
	var _ runtime.WorkspaceConfiguration = resolved
}
