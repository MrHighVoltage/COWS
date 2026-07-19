import RFB from "/static/vendor/novnc/core/rfb.js";

export function initDesktop(root) {
  if (!root || root.dataset.desktopInitialized === "true") return;
  root.dataset.desktopInitialized = "true";

  const display = root.querySelector("#desktop-display");
  const status = root.querySelector("#desktop-status");
  const error = root.querySelector("#desktop-error");
  const connectButton = root.querySelector("[data-desktop-connect]");
  const fitButton = root.querySelector("[data-desktop-fit]");
  const fullscreenButton = root.querySelector("[data-desktop-fullscreen]");
  const fullscreenRoot = document.getElementById(root.dataset.fullscreenTarget) || root;
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  const url = scheme + "//" + window.location.host + root.dataset.desktopUrl;
  let rfb = null;
  let userDisconnect = false;

  function setStatus(value) {
    status.textContent = value;
    status.classList.remove("connection-status-connected", "connection-status-connecting", "connection-status-disconnected", "connection-status-unavailable");
    const className = {
      Connected: "connection-status-connected",
      Connecting: "connection-status-connecting",
      Authenticating: "connection-status-connecting",
      Disconnected: "connection-status-disconnected",
      Unavailable: "connection-status-unavailable"
    }[value];
    if (className) status.classList.add(className);
    if (connectButton) connectButton.textContent = rfb ? "Disconnect" : "Connect";
  }

  function fitDesktop() {
    if (!rfb) return;
    window.requestAnimationFrame(function () {
      if (!rfb) return;
      rfb.scaleViewport = true;
      rfb.resizeSession = true;
      window.dispatchEvent(new Event("resize"));
    });
  }

  function updateFullscreenLabel() {
    if (fullscreenButton) fullscreenButton.textContent = document.fullscreenElement === fullscreenRoot ? "Exit full screen" : "Full screen";
  }

  async function toggleFullscreen() {
    try {
      if (document.fullscreenElement === fullscreenRoot) {
        await document.exitFullscreen();
      } else {
        await fullscreenRoot.requestFullscreen();
      }
    } catch (requestError) {
      error.hidden = false;
      error.textContent = "Full screen mode is not available in this browser.";
    }
  }

  function disconnect() {
    if (!rfb) {
      setStatus("Disconnected");
      return;
    }
    const instance = rfb;
    rfb = null;
    userDisconnect = true;
    setStatus("Disconnected");
    instance.disconnect();
  }

  function connect() {
    if (rfb) return;
    userDisconnect = false;
    error.hidden = true;
    setStatus("Connecting");
    const instance = new RFB(display, url);
    rfb = instance;
    instance.scaleViewport = true;
    instance.resizeSession = true;
    instance.addEventListener("connect", function () {
      if (rfb !== instance) return;
      setStatus("Connected");
      fitDesktop();
    });
    instance.addEventListener("disconnect", function (event) {
      if (rfb !== instance) return;
      rfb = null;
      setStatus("Disconnected");
      if (!event.detail.clean && !userDisconnect) {
        error.hidden = false;
        error.textContent = "The graphical desktop connection was interrupted.";
      }
    });
    instance.addEventListener("credentialsrequired", async function () {
      if (rfb !== instance) return;
      setStatus("Authenticating");
      try {
        const response = await fetch(root.dataset.credentialsUrl, {
          credentials: "same-origin",
          headers: { "Accept": "application/json" }
        });
        if (!response.ok) throw new Error("credentials request failed");
        const credentials = await response.json();
        if (typeof credentials.password !== "string" || credentials.password.length === 0) throw new Error("credentials response was invalid");
        if (rfb === instance) instance.sendCredentials({ password: credentials.password });
      } catch (requestError) {
        if (rfb !== instance) return;
        setStatus("Unavailable");
        error.hidden = false;
        error.textContent = "COWS could not authorize the graphical desktop session.";
      }
    });
    instance.addEventListener("securityfailure", function () {
      if (rfb !== instance) return;
      setStatus("Unavailable");
      error.hidden = false;
      error.textContent = "The VNC server rejected the desktop session.";
    });
  }

  if (connectButton) connectButton.addEventListener("click", function () {
    if (rfb) disconnect();
    else connect();
  });
  if (fitButton) fitButton.addEventListener("click", fitDesktop);
  if (fullscreenButton) fullscreenButton.addEventListener("click", toggleFullscreen);
  document.addEventListener("fullscreenchange", function () {
    updateFullscreenLabel();
    fitDesktop();
  });
  connect();
}

const standaloneRoot = document.querySelector("[data-access]") ? null : document.querySelector("[data-desktop]");
if (standaloneRoot) initDesktop(standaloneRoot);
