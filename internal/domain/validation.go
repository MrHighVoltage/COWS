package domain

import (
	"errors"
	"sort"
	"strings"
)

// FieldError attributes a rejected value to the single input that caused it,
// so a server-rendered form can mark that control instead of only showing one
// message for the whole form. Field is the name of the form control the web
// layer renders, which keeps the rule and the highlighted input from drifting
// apart; Err stays the package-level sentinel the validator already returned,
// so every existing errors.Is check keeps matching.
type FieldError struct {
	Field   string
	Message string
	Err     error
}

func (e *FieldError) Error() string {
	if e == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if e.Field != "" {
		parts = append(parts, e.Field)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	return strings.Join(parts, ": ")
}

func (e *FieldError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// InvalidField attributes sentinel err to one named field with a message that
// is safe to show the person who submitted the form. Validators call it in
// place of returning the bare sentinel.
func InvalidField(err error, field, message string) error {
	return &FieldError{Field: field, Message: message, Err: err}
}

// InvalidFields joins several field attributions into one error so a single
// submission can mark every input it got wrong at once. It returns nil when
// no field failed and the lone error when exactly one did, so callers can
// keep returning it directly.
func InvalidFields(errs ...error) error {
	kept := make([]error, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			kept = append(kept, err)
		}
	}
	switch len(kept) {
	case 0:
		return nil
	case 1:
		return kept[0]
	default:
		return errors.Join(kept...)
	}
}

// FieldMessages collects every field attribution reachable from err, walking
// into the members of an errors.Join tree as well as a single wrapped chain.
// errors.As would stop at the first match, which would lose all but one mark
// on a form that failed several checks. The first message wins for a field, so
// the earliest, most specific rule is the one shown.
func FieldMessages(err error) map[string]string {
	return FieldMessagesOn(err, func(field string) string { return field })
}

// FieldMessagesOn is FieldMessages for a form that shows several attributed
// fields on one control: control names the control each field is shown on.
// The earliest attribution still wins on a shared control, so it shows the
// same message a form with one control per field would have shown first.
func FieldMessagesOn(err error, control func(field string) string) map[string]string {
	messages := map[string]string{}
	collectFieldMessages(err, control, messages)
	if len(messages) == 0 {
		return nil
	}
	return messages
}

func collectFieldMessages(err error, control func(string) string, into map[string]string) {
	for err != nil {
		if fieldErr, ok := err.(*FieldError); ok {
			if fieldErr.Field != "" {
				name := control(fieldErr.Field)
				if _, exists := into[name]; !exists {
					into[name] = fieldErr.Message
				}
			}
		}
		switch unwrapped := err.(type) {
		case interface{ Unwrap() []error }:
			for _, member := range unwrapped.Unwrap() {
				collectFieldMessages(member, control, into)
			}
			return
		case interface{ Unwrap() error }:
			err = unwrapped.Unwrap()
		default:
			return
		}
	}
}

// FieldNames lists the attributed fields in a stable order, which keeps test
// expectations and log lines deterministic.
func FieldNames(messages map[string]string) []string {
	names := make([]string, 0, len(messages))
	for name := range messages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
