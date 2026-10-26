//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// operationRecord is one timed user-visible action. ExpectedFailure is used
// for probes that intentionally hit a quota or abuse boundary; those are
// interesting data, not test failures.
type operationRecord struct {
	Name            string    `json:"name"`
	User            string    `json:"user,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	DurationMS      int64     `json:"duration_ms"`
	OK              bool      `json:"ok"`
	ExpectedFailure bool      `json:"expected_failure,omitempty"`
	Detail          string    `json:"detail,omitempty"`
	Error           string    `json:"error,omitempty"`
}

type operationSummary struct {
	Count            int   `json:"count"`
	Errors           int   `json:"errors"`
	ExpectedFailures int   `json:"expected_failures"`
	P50MS            int64 `json:"p50_ms"`
	P95MS            int64 `json:"p95_ms"`
	MaxMS            int64 `json:"max_ms"`
}

type report struct {
	Test       string                      `json:"test"`
	StartedAt  time.Time                   `json:"started_at"`
	Image      string                      `json:"image"`
	Users      int                         `json:"users"`
	Operations []operationRecord           `json:"operations"`
	Summary    map[string]operationSummary `json:"summary"`
}

type recorder struct {
	mu         sync.Mutex
	test       string
	started    time.Time
	image      string
	users      int
	operations []operationRecord
}

func newRecorder(test, image string, users int) *recorder {
	return &recorder{test: test, started: time.Now().UTC(), image: image, users: users}
}

func (r *recorder) record(name, user string, started time.Time, err error, expected bool, detail string) {
	record := operationRecord{
		Name:            name,
		User:            user,
		StartedAt:       started.UTC(),
		DurationMS:      time.Since(started).Milliseconds(),
		OK:              err == nil,
		ExpectedFailure: expected,
		Detail:          detail,
	}
	if err != nil {
		record.Error = err.Error()
	}
	r.mu.Lock()
	r.operations = append(r.operations, record)
	r.mu.Unlock()
}

func (r *recorder) countUnexpectedErrors() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, operation := range r.operations {
		if !operation.OK && !operation.ExpectedFailure {
			count++
		}
	}
	return count
}

func (r *recorder) write(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	operations := append([]operationRecord(nil), r.operations...)
	r.mu.Unlock()

	value := report{
		Test:       r.test,
		StartedAt:  r.started,
		Image:      r.image,
		Users:      r.users,
		Operations: operations,
		Summary:    summarizeOperations(operations),
	}
	dir := filepath.Join(repoRoot, ".cows-test", "e2e-reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Logf("e2e report directory unavailable: %v", err)
		return
	}
	name := sanitizeReportName(r.test) + "-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".json"
	path := filepath.Join(dir, name)
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Logf("encode e2e report: %v", err)
		return
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Logf("write e2e report: %v", err)
		return
	}
	t.Logf("e2e report: %s", path)
	logOperationSummary(t, value.Summary)
}

func sanitizeReportName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("/", "-", "\\", "-", " ", "-", ":", "-").Replace(value)
	if value == "" {
		return "e2e"
	}
	return value
}

func summarizeOperations(operations []operationRecord) map[string]operationSummary {
	grouped := make(map[string][]operationRecord)
	for _, operation := range operations {
		grouped[operation.Name] = append(grouped[operation.Name], operation)
	}
	result := make(map[string]operationSummary, len(grouped))
	for name, values := range grouped {
		durations := make([]int64, 0, len(values))
		summary := operationSummary{Count: len(values)}
		for _, value := range values {
			durations = append(durations, value.DurationMS)
			if !value.OK && !value.ExpectedFailure {
				summary.Errors++
			}
			if !value.OK && value.ExpectedFailure {
				summary.ExpectedFailures++
			}
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		summary.P50MS = percentile(durations, 50)
		summary.P95MS = percentile(durations, 95)
		summary.MaxMS = durations[len(durations)-1]
		result[name] = summary
	}
	return result
}

func percentile(sorted []int64, percent int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := (len(sorted) - 1) * percent / 100
	return sorted[index]
}

func logOperationSummary(t *testing.T, summary map[string]operationSummary) {
	t.Helper()
	names := make([]string, 0, len(summary))
	for name := range summary {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := summary[name]
		t.Logf("%-32s n=%-3d errors=%-2d expected=%-2d p50=%-6dms p95=%-6dms max=%dms",
			name, value.Count, value.Errors, value.ExpectedFailures, value.P50MS, value.P95MS, value.MaxMS)
	}
}

func (r *recorder) requireNoUnexpectedErrors(t *testing.T) {
	t.Helper()
	if count := r.countUnexpectedErrors(); count > 0 {
		t.Fatalf("%d user-visible operation(s) failed unexpectedly; see the e2e report for details", count)
	}
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("%s: %d operations", r.test, len(r.operations))
}
