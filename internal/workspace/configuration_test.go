package workspace

import (
	"testing"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/runtime"
)

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
	if err := validateTemplateConfiguration(domain.TemplateConfiguration{ContainerUser: &domain.TemplateContainerUser{}}); err != ErrInvalidTemplate {
		t.Fatalf("missing container IDs error = %v", err)
	}
}

func TestTemplateConfigurationValidatesTerminalUIDs(t *testing.T) {
	if err := validateTemplateConfiguration(domain.TemplateConfiguration{TerminalUIDs: []int64{1000, 0}}); err != nil {
		t.Fatalf("valid terminal UIDs rejected: %v", err)
	}
	for _, value := range [][]int64{{-1}, {2147483648}, {1000, 1000}} {
		if err := validateTemplateConfiguration(domain.TemplateConfiguration{TerminalUIDs: value}); err != ErrInvalidTemplate {
			t.Fatalf("terminal UIDs %v error = %v, want %v", value, err, ErrInvalidTemplate)
		}
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
