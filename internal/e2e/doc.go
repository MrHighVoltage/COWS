// Package e2e holds opt-in end-to-end tests for the user-facing COWS web
// application. The tests start a real cows process against a real rootless
// Podman service and drive the same HTTP forms and WebSockets a browser uses.
//
// The suite is excluded from the ordinary unit-test run. See
// docs/implementation/end-to-end-test-plan.md and run:
//
//	COWS_E2E_IMAGE=docker.io/library/ubuntu:26.04 \
//	  go test -tags e2e -count=1 ./internal/e2e
package e2e
