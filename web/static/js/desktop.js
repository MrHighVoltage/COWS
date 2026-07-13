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
  rfb.addEventListener("credentialsrequired", function () {
    status.textContent = "Credentials required";
    error.hidden = false;
    error.textContent = "This workspace requires VNC credentials that are not configured in COWS yet.";
  });
  rfb.addEventListener("securityfailure", function () {
    status.textContent = "Unavailable";
    error.hidden = false;
    error.textContent = "The VNC server rejected the desktop session.";
  });
}
