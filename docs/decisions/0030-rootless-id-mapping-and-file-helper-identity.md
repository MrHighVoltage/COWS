# ADR 0030: Rootless ID mapping validation and file-helper identity

## Status

Accepted.

## Decision

COWS accepts Podman's reported rootless UID/GID maps only after validating
their structure. Container ID ranges must start at zero and remain contiguous;
host ranges must be positive, non-overlapping, and free of arithmetic overflow.
Namespace ID zero is reserved for the invoking account, and no subordinate
range may map the COWS service UID or GID. These checks run before the explicit
`{container 0 -> namespace 1, size total-1}` mapping is built, so a malformed
`/etc/subuid`, `/etc/subgid`, or unexpected Podman report fails closed instead
of mapping container root back to the COWS account.

The file helper has two explicit identity modes:

- **Configured container user.** The adapter translates the template UID/GID
  through the same explicit mapping used for container creation. The helper
  must drop to the resulting nonzero mapped identity and must never retain
  namespace-root access.
- **Image identity or administrator recovery.** The container runs in Podman's
  default rootless namespace, or an administrator is reading a retained volume
  that may carry any mapped identity. The adapter explicitly authorizes
  namespace-root access; the helper still verifies the mapping and operates
  through a rooted, server-selected path.

The helper refuses namespace-root access unless the adapter supplies the
explicit authorization flag. In both modes it verifies `/proc/self/uid_map`
and `/proc/self/gid_map` against the host IDs the adapter derived from Podman's
mapping, rejects a symlinked or non-directory root, resets supplementary groups,
and verifies its final real UID/GID. It receives only the environment variables
needed by `podman unshare`; COWS configuration and credential-shaped variables
are removed. These changes close the latent path where a future bug or a
mismatched namespace could leave the helper acting as the COWS host account,
without rejecting the image-identity and administrator-recovery flows that
legitimately need namespace-root access.

## Consequences

Configured-user file access always runs as a subordinate host ID. Image-identity
file access and administrator retained-volume recovery keep their existing
privilege level but now have an explicit, auditable authorization flag and a
mapping check. A `podman unshare` namespace that differs from the Podman service
namespace fails closed rather than silently dropping to a different host
account. Deployments with non-contiguous or service-account-containing
subordinate ranges must correct `/etc/subuid` and `/etc/subgid` before file or
workspace operations can proceed.

Templates without `container_user` still use Podman's default rootless
namespace, where container root is the COWS service account. Changing that
default, resolving the image's effective user for file access, per-workspace
subordinate ranges, and post-start verification of the container's own
namespace remain future work. The first private-namespace container per image
also retains the `storage-chown-by-maps` cost documented in
`docs/implementation/audit-findings.md`; that operational work is separate.

## References

- [ADR 0009: Template-controlled container identity](0009-template-container-identity.md)
- [ADR 0011: Runtime-backed file access](0011-runtime-file-access.md)
- [ADR 0029: Security audit hardening](0029-security-audit-hardening.md)
- `docs/implementation/audit-findings.md`, entry F11 and the 2026-10-14/15
  private-namespace notes.
