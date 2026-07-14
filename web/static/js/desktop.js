import RFB from "/static/vendor/novnc/core/rfb.js";

const root = document.querySelector("[data-desktop]");
if (root) {
  const display = root.querySelector("#desktop-display");
  const status = root.querySelector("#desktop-status");
  const error = root.querySelector("#desktop-error");
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  const url = scheme + "//" + window.location.host + root.dataset.desktopUrl;
  const rfb = new RFB(display, url);

  rfb.scaleViewport = true;
  rfb.resizeSession = false;

  rfb.addEventListener("connect", function () {
    status.textContent = "Connected";
  });
  rfb.addEventListener("disconnect", function (event) {
    status.textContent = "Disconnected";
    if (!event.detail.clean) {
      error.hidden = false;
      error.textContent = "The graphical desktop connection was interrupted.";
    }
  });
  rfb.addEventListener("credentialsrequired", async function () {
    status.textContent = "Authenticating";
    try {
      const response = await fetch(root.dataset.credentialsUrl, {
        credentials: "same-origin",
        headers: { "Accept": "application/json" }
      });
      if (!response.ok) {
        throw new Error("credentials request failed");
      }
      const credentials = await response.json();
      if (typeof credentials.password !== "string" || credentials.password.length === 0) {
        throw new Error("credentials response was invalid");
      }
      rfb.sendCredentials({ password: credentials.password });
    } catch (requestError) {
      status.textContent = "Unavailable";
      error.hidden = false;
      error.textContent = "COWS could not authorize the graphical desktop session.";
    }
  });
  rfb.addEventListener("securityfailure", function () {
    status.textContent = "Unavailable";
    error.hidden = false;
    error.textContent = "The VNC server rejected the desktop session.";
  });
}
