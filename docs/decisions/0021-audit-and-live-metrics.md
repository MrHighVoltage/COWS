# ADR 0021: Rich Audit and Live Metrics

## Status

Accepted

COWS retains security-relevant audit events in SQLite and exposes a bounded,
administrator-only view with actor, event, target, metadata, and UTC time.
Metadata must not contain passwords, tokens, terminal contents, file contents,
or mail bodies.

CPU, memory, process, storage, and host-capacity values remain live runtime
observations. COWS aggregates them for an administrator metrics view but does
not write every sample to SQLite. Historical metrics storage is deferred until
a real retention requirement exists.
