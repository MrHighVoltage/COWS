# ADR 0022: Administrator Recovery of Retained Named Volumes

## Status

Accepted

Explicit workspace deletion retains named-volume tombstone metadata before the
workspace record is removed. Administrators can list those records, stream a
bounded ZIP download through the runtime file-access boundary, or permanently
remove a selected volume after confirmation.

The tombstone is metadata, not authorization. Users cannot mount or restore a
retained volume, and browsers never provide a volume name or runtime path.
Restore and reattachment are intentionally deferred. Automatic timeout cleanup
never deletes or archives user data.
