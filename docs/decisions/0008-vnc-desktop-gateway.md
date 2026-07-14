# ADR 0008: Dedicated VNC desktop gateway

Status: accepted for Milestone 5

## Decision

COWS provides graphical desktop access through a dedicated authenticated WebSocket
gateway and the vendored noVNC core client. The browser connects to a COWS
workspace desktop URL. It never supplies a container ID, host, port, protocol,
or backend address.

The gateway accepts only a template service named `desktop` using TCP. The
workspace service resolves the persisted port allocation, authorizes the user,
requires a running workspace and desktop access permission, and asks the runtime
adapter to verify the exact loopback mapping before opening the connection.

The initial Docker-compatible adapter binds approved service ports to loopback
only. COWS bridges the resulting raw VNC stream to noVNC. It does not expose a
public VNC port and does not implement a generic application proxy in this
milestone.

Desktop-enabled workspaces receive an eight-character random `VNC_PW` at
workspace creation. COWS injects it into the container as a sensitive
environment variable and returns it only from an authorized, non-cacheable
credentials endpoint when noVNC requests it. Users do not enter a second
password. The secret is stored in the protected SQLite control-plane database;
it is excluded from ordinary page rendering, URLs, logs, and audit events.

## Consequences

- VNC-enabled images must listen on the template-configured internal TCP port.
- The template must define a `desktop` service and enable desktop access.
- The upstream image must honor the `VNC_PW` environment variable. COWS does
  not require an image rebuild or a user-supplied VNC password.
- The runtime adapter remains the only component that knows the local service
  forwarding details.
- A future host agent can replace local loopback dialing without changing the
  browser or workspace authorization contract.
