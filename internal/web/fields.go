package web

import (
	"fmt"
	"html/template"

	"github.com/cows-project/cows/internal/domain"
	"github.com/cows-project/cows/internal/workspace"
)

// This file holds the one place a rejected submission turns into something a
// form can render: the per-field marks that point at the offending controls,
// and the single alert above the form. Validation attributes its rejections to
// form field names (see the Field* constants in internal/auth,
// internal/quota, and internal/workspace), and the templates render controls
// under those same names, so a rule and the input it marks stay together.

// fieldMark is an attribution the service layer cannot make on its own,
// because the rule is about uniqueness across stored records rather than about
// the submitted value.
type fieldMark struct {
	when    bool
	field   string
	message string
}

func markIf(when bool, field, message string) fieldMark {
	return fieldMark{when: when, field: field, message: message}
}

// fieldFeedback splits err into the alert shown above the form and the marks
// shown at each offending control. When at least one control is marked, the
// alert only points at the marks, so the summary can never contradict them;
// otherwise it carries the full explanation in fallback.
func fieldFeedback(err error, fallback string, extra ...fieldMark) (string, map[string]string) {
	return markedFeedback(domain.FieldMessages(err), fallback, extra...)
}

// markedFeedback is fieldFeedback for marks already collected, for a form
// that shows several attributed fields on one control.
func markedFeedback(fields map[string]string, fallback string, extra ...fieldMark) (string, map[string]string) {
	for _, mark := range extra {
		if !mark.when {
			continue
		}
		if fields == nil {
			fields = map[string]string{}
		}
		if _, exists := fields[mark.field]; !exists {
			fields[mark.field] = mark.message
		}
	}
	if len(fields) == 0 {
		return fallback, nil
	}
	if len(fields) == 1 {
		return "One field needs attention. It is marked below.", fields
	}
	return fmt.Sprintf("%d fields need attention. They are marked below.", len(fields)), fields
}

// unreadableAndInvalid merges the values this package could not read with what
// the workspace service makes of the ones it could. Parsing has to stop a
// submission on its own, but stopping there would hide every other problem
// until the unreadable fields were fixed, so both verdicts are reported at
// once. The parse error comes first, so a field that failed both ways shows
// the more specific "this is not a number" message.
func unreadableAndInvalid(parseErr error, input workspace.TemplateInput) error {
	return domain.InvalidFields(parseErr, workspace.ValidateTemplateInput(input))
}

// fieldErrorView is the rendered form of one mark: the message and the id the
// control refers to through aria-describedby.
type fieldErrorView struct {
	ID      string
	Message string
}

// fieldErrorFor is the "fieldError" template function. It returns nil when the
// field was not rejected, so a template can use it with "with".
func fieldErrorFor(fields map[string]string, name string) *fieldErrorView {
	message := fields[name]
	if message == "" || !safeFieldName(name) {
		return nil
	}
	return &fieldErrorView{ID: fieldErrorID(name), Message: message}
}

// invalidFieldAttrs is the "invalidField" template function. aria-invalid both
// tells assistive technology the value was rejected and is what the stylesheet
// selects on to outline the control, so one attribute covers both and no
// template has to repeat the conditional.
func invalidFieldAttrs(fields map[string]string, name string) template.HTMLAttr {
	if fields[name] == "" || !safeFieldName(name) {
		return ""
	}
	return template.HTMLAttr(` aria-invalid="true" aria-describedby="` + fieldErrorID(name) + `"`)
}

func fieldErrorID(name string) string {
	return "field-error-" + name
}

// safeFieldName keeps the generated attributes unforgeable. Field names are
// compile-time constants today; this makes it a checked property rather than a
// convention, because these two functions write unescaped markup.
func safeFieldName(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' {
			continue
		}
		return false
	}
	return true
}
