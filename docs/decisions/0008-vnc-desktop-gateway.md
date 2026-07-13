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

## Consequences

- VNC-enabled images must listen on the template-configured internal TCP port.
- The template must define a `desktop` service and enable desktop access.
- VNC authentication credentials are not provisioned by this milestone; images
  requiring credentials report that the desktop session is unavailable.
- The runtime adapter remains the only component that knows the local service
  forwarding details.
- A future host agent can replace local loopback dialing without changing the
  browser or workspace authorization contract.
