package web

import (
	"strings"
	"testing"
)

func TestParseUserImportCSV(t *testing.T) {
	inputs, err := parseUserImportCSV(strings.NewReader("\ufeffusername,email,display_name\nalice,alice@example.test,Alice\nbob,,Bob\n"))
	if err != nil || len(inputs) != 2 {
		t.Fatalf("parsed inputs = %+v err=%v", inputs, err)
	}
	if inputs[0].Username != "alice" || inputs[1].Email != "" {
		t.Fatalf("parsed input values = %+v", inputs)
	}
}

func TestParseUserImportCSVRejectsUnexpectedHeader(t *testing.T) {
	if _, err := parseUserImportCSV(strings.NewReader("username,email,role\nalice,alice@example.test,user\n")); err == nil {
		t.Fatal("unexpected CSV header was accepted")
	}
}
