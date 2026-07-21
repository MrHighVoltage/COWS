# ADR 0019: Explicit template image availability and pulls

Status: accepted

## Decision

COWS does not pull container images as a side effect of workspace creation or
startup. An administrator-controlled template image must be available in the
configured rootless Podman image store before a workspace can be created.

The administrator template list checks the exact configured image reference,
including an optional digest, and reports whether it is available locally. An
administrator may explicitly start a pull from that page. Pull progress is
held in process memory and returned through a small HTMX polling fragment.

The pull operation is intentionally not persisted. A process restart loses its
progress display, but does not corrupt the runtime operation; the next page
load checks the image store again. Only one pull operation per template is
started at a time. The runtime adapter parses Podman's streaming pull response
and keeps Podman-specific response types inside the adapter.

## Security and operational limits

- Only administrators may inspect image availability or start pulls.
- The browser supplies a template ID, never an image reference.
- The server reloads the template and resolves the image reference itself.
- COWS never accepts a pull policy, registry credential, or runtime argument
  from the browser.
- Pull failures show a stable administrator-facing message; raw runtime errors
  are not rendered in the page.
- Image updates remain explicit. Editing a template does not pull, retag, or
  remove any image.

## Deliberately deferred

- Persistent pull jobs and resumable downloads.
- Registry credential management.
- Scheduled image refresh or automatic update policies.
- Image garbage collection and disk-usage policy.
- Pull cancellation from the browser.
