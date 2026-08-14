# ADR 0022: Administrator Recovery of Retained Named Volumes

## Status

Accepted

Explicit workspace deletion retains named-volume tombstone metadata before the
workspace record is removed. Administrators can list those records, stream a
bounded ZIP download through the runtime file-access boundary, or permanently
remove a selected volume after confirmation.

The tombstone is metadata, not authorization by itself: a lookup must still
be scoped to the requesting party before it grants anything. Browsers never
provide a volume name or runtime path. Automatic timeout cleanup never
deletes or archives user data.

Restore and reattachment for administrators, on behalf of a user other than
themselves, remain deferred and out of scope for this ADR. User self-service
reattachment of their own retained volumes and archived directories is a
separate, later capability — see decision 0025 — with its own route
namespace and no administrator bypass; it does not change the administrator
recovery workflow described above.
