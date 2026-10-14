package domain

import (
	"errors"
	"fmt"
	"testing"
)

var errSentinel = errors.New("sentinel")

func TestFieldErrorKeepsTheSentinelMatchable(t *testing.T) {
	err := InvalidField(errSentinel, "name", "Enter a name.")
	if !errors.Is(err, errSentinel) {
		t.Fatalf("errors.Is(%v, sentinel) = false", err)
	}
}

func TestInvalidFieldsCollapsesToTheSingleCause(t *testing.T) {
	if err := InvalidFields(nil, nil); err != nil {
		t.Fatalf("InvalidFields of nothing = %v, want nil", err)
	}
	only := InvalidField(errSentinel, "name", "Enter a name.")
	if got := InvalidFields(nil, only, nil); got != only {
		t.Fatalf("InvalidFields of one error = %v, want it returned unchanged", got)
	}
}

func TestFieldMessagesWalksJoinedAndWrappedErrors(t *testing.T) {
	joined := InvalidFields(
		InvalidField(errSentinel, "name", "Enter a name."),
		InvalidField(errSentinel, "email", "Enter an address."),
	)
	// A join inside a join inside a wrap: errors.As would stop at the first
	// match and lose every other mark, which is why FieldMessages walks.
	nested := fmt.Errorf("saving the form: %w", InvalidFields(joined, InvalidField(errSentinel, "role", "Choose a role.")))
	messages := FieldMessages(nested)
	if len(messages) != 3 {
		t.Fatalf("FieldMessages = %v, want three marks", messages)
	}
	if !errors.Is(nested, errSentinel) {
		t.Fatalf("nested error no longer matches the sentinel")
	}
	for field, want := range map[string]string{"name": "Enter a name.", "email": "Enter an address.", "role": "Choose a role."} {
		if messages[field] != want {
			t.Fatalf("message for %q = %q, want %q", field, messages[field], want)
		}
	}
}

func TestFieldMessagesKeepsTheFirstMessagePerField(t *testing.T) {
	err := InvalidFields(
		InvalidField(errSentinel, "cpu", "This is not a number."),
		InvalidField(errSentinel, "cpu", "Enter at least 1."),
	)
	if got := FieldMessages(err)["cpu"]; got != "This is not a number." {
		t.Fatalf("message for cpu = %q, want the first one", got)
	}
}

func TestFieldMessagesIgnoresUnattributedErrors(t *testing.T) {
	if messages := FieldMessages(errSentinel); messages != nil {
		t.Fatalf("FieldMessages of a plain error = %v, want nil", messages)
	}
	if messages := FieldMessages(nil); messages != nil {
		t.Fatalf("FieldMessages(nil) = %v, want nil", messages)
	}
}

func TestFieldNamesAreSorted(t *testing.T) {
	names := FieldNames(map[string]string{"role": "a", "email": "b", "name": "c"})
	if len(names) != 3 || names[0] != "email" || names[1] != "name" || names[2] != "role" {
		t.Fatalf("FieldNames = %v, want a sorted list", names)
	}
}

func TestFieldMessagesOnSharesAControlInOrder(t *testing.T) {
	err := InvalidFields(
		InvalidField(errSentinel, "name", "Enter a name."),
		InvalidField(errSentinel, "config_mount_1_name", "Mount name is invalid."),
		InvalidField(errSentinel, "config_env_0_name", "Environment name is invalid."),
	)
	shared := func(field string) string {
		if field == "name" {
			return field
		}
		return "configuration_json"
	}
	messages := FieldMessagesOn(err, shared)
	if len(messages) != 2 || messages["name"] != "Enter a name." {
		t.Fatalf("FieldMessagesOn = %v, want the name mark and one shared mark", messages)
	}
	if got := messages["configuration_json"]; got != "Mount name is invalid." {
		t.Fatalf("shared control message = %q, want the earliest attribution", got)
	}
}
