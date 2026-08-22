package auth

import (
	"context"
	"testing"
	"time"

	"github.com/cows-project/cows/internal/domain"
)

func TestUserImportPreviewsAndCommitsNewAndExistingUsers(t *testing.T) {
	service := testService(t)
	ctx := context.Background()
	if _, err := service.BootstrapAdministrator(ctx, CreateUserInput{Username: "admin", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("bootstrap administrator: %v", err)
	}
	admin, _, err := service.Authenticate(ctx, "admin", "correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate administrator: %v", err)
	}
	if err := service.ChangePassword(ctx, admin.ID, "correct horse battery staple", "changed correct horse battery staple"); err != nil {
		t.Fatalf("change administrator password: %v", err)
	}
	admin, _, err = service.Authenticate(ctx, "admin", "changed correct horse battery staple")
	if err != nil {
		t.Fatalf("authenticate changed administrator: %v", err)
	}
	group, err := service.CreateGroup(ctx, admin.ID, "research", "Research users")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	existing, err := service.CreateUser(ctx, admin.ID, CreateUserInput{Username: "existing", Email: "old@example.test", DisplayName: "Existing", Password: "existing password", Role: domain.RoleUser})
	if err != nil {
		t.Fatalf("create existing user: %v", err)
	}
	inputs := []ImportUserInput{{Username: "existing", Email: "new@example.test", DisplayName: "Ignored"}, {Username: "new-user", Email: "new@example.test", DisplayName: "New User"}}
	preview, err := service.PreviewUserImport(ctx, admin.ID, inputs, []string{group.ID})
	if err != nil || len(preview) != 2 || !preview[0].Existing || preview[0].GroupsToAdd[0] != group.ID || preview[1].Existing {
		t.Fatalf("import preview = %+v err=%v", preview, err)
	}
	results, err := service.ImportUsers(ctx, admin.ID, inputs, []string{group.ID})
	if err != nil || len(results) != 2 {
		t.Fatalf("import results = %+v err=%v", results, err)
	}
	if !results[0].Existing || results[0].Password != "" || results[1].Existing || len(results[1].Password) < 12 {
		t.Fatalf("unexpected import results: %+v", results)
	}
	updated, err := service.FindUserForAdmin(ctx, admin.ID, existing.ID)
	if err != nil || updated.Email != "new@example.test" {
		t.Fatalf("updated existing user = %+v err=%v", updated, err)
	}
	groups, err := service.UserGroupIDs(ctx, admin.ID, existing.ID)
	if err != nil || len(groups) != 1 || groups[0] != group.ID {
		t.Fatalf("existing user groups = %v err=%v", groups, err)
	}
	if _, _, err := service.Authenticate(ctx, "new-user", results[1].Password); err != nil {
		t.Fatalf("authenticate imported user: %v", err)
	}
}

func TestImportIssuesInvitationsWhenAvailable(t *testing.T) {
	ctx := context.Background()
	service, _, adminID := invitationTestService(t)
	service.SetInvitationPolicy(true, 24*time.Hour)

	results, err := service.ImportUsers(ctx, adminID, []ImportUserInput{
		{Username: "rowone", Email: "rowone@example.test", DisplayName: "Row One"},
		{Username: "rowtwo", DisplayName: "Row Two"},
	}, nil)
	if err != nil {
		t.Fatalf("import users: %v", err)
	}
	if results[0].Invitation.Token == "" {
		t.Fatal("a row with an address must get an invitation")
	}
	if results[0].Password != "" {
		t.Fatal("an invited row must not also carry a temporary password")
	}
	// A row without an address cannot be invited and keeps the temporary
	// password an administrator has to relay.
	if results[1].Invitation.Token != "" || results[1].Password == "" {
		t.Fatal("a row without an address must fall back to a temporary password")
	}
	// The invited account must not be openable with anything but its link.
	if _, _, err := service.Authenticate(ctx, "rowone", results[1].Password); err == nil {
		t.Fatal("an invited account accepted another row's password")
	}
	if _, err := service.AcceptInvitation(ctx, results[0].Invitation.Token, "chosen-password-1", "chosen-password-1"); err != nil {
		t.Fatalf("accept the imported invitation: %v", err)
	}
	if _, _, err := service.Authenticate(ctx, "rowone", "chosen-password-1"); err != nil {
		t.Fatalf("authenticate after accepting: %v", err)
	}
}

func TestImportFallsBackToPasswordsWhenInvitationsAreUnavailable(t *testing.T) {
	ctx := context.Background()
	service, _, adminID := invitationTestService(t)

	results, err := service.ImportUsers(ctx, adminID, []ImportUserInput{
		{Username: "rowone", Email: "rowone@example.test", DisplayName: "Row One"},
	}, nil)
	if err != nil {
		t.Fatalf("import users: %v", err)
	}
	if results[0].Invitation.Token != "" || results[0].Password == "" {
		t.Fatal("without invitations the import must keep issuing temporary passwords")
	}
	if _, _, err := service.Authenticate(ctx, "rowone", results[0].Password); err != nil {
		t.Fatalf("the temporary password must work: %v", err)
	}
}
