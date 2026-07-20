(function () {
  "use strict";

  function initTerminal(root) {
    if (!root || root.dataset.terminalInitialized === "true" || typeof Terminal === "undefined") return;
    root.dataset.terminalInitialized = "true";

    const target = root.querySelector("#terminal");
    const controls = document.querySelector(root.dataset.controlsTarget) || root;
    const status = controls.querySelector("#terminal-status");
    const error = root.querySelector("#terminal-error");
    const connectButton = controls.querySelector("[data-terminal-connect]");
    const fitButton = controls.querySelector("[data-terminal-fit]");
    const fullscreenButton = controls.querySelector("[data-terminal-fullscreen]");
    const fullscreenRoot = document.getElementById(root.dataset.fullscreenTarget) || root;
    const terminal = new Terminal({
      convertEol: true,
      cursorBlink: true,
      scrollback: 2000,
      customGlyphs: true,
      fontFamily: "'JetBrainsMono Nerd Font', 'FiraCode Nerd Font', 'MesloLGS NF', 'JetBrains Mono', 'Fira Code', 'Symbols Nerd Font Mono', 'Cascadia Mono', 'DejaVu Sans Mono', monospace",
      fontSize: 14,
      fontWeight: "400",
      fontWeightBold: "700",
      theme: { background: "#101820", foreground: "#f4f6f8" }
    });
    terminal.open(target);
    const fitAddon = typeof FitAddon !== "undefined" ? new FitAddon.FitAddon() : null;
    const terminalUID = controls.querySelector("[data-terminal-uid]");
    if (fitAddon) terminal.loadAddon(fitAddon);

    let socket = null;
    let userDisconnect = false;

    function setStatus(value) {
      status.textContent = value;
      status.classList.remove("connection-status-connected", "connection-status-connecting", "connection-status-disconnected", "connection-status-unavailable");
      const className = {
        Connected: "connection-status-connected",
        Connecting: "connection-status-connecting",
        Disconnected: "connection-status-disconnected",
        Unavailable: "connection-status-unavailable"
      }[value];
      if (className) status.classList.add(className);
      if (connectButton) connectButton.textContent = value === "Connected" || value === "Connecting" ? "Disconnect" : "Connect";
    }

    function sendResize() {
      if (!socket || socket.readyState !== WebSocket.OPEN) return;
      socket.send(JSON.stringify({ type: "resize", cols: terminal.cols, rows: terminal.rows }));
    }

    function fitTerminal() {
      if (!fitAddon) return;
      window.requestAnimationFrame(function () {
        fitAddon.fit();
        sendResize();
      });
    }

    function terminalURL() {
      const endpoint = new URL(root.dataset.terminalUrl, window.location.origin);
      if (terminalUID) endpoint.searchParams.set("uid", terminalUID.value);
      return endpoint.pathname + endpoint.search;
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
      userDisconnect = true;
      if (socket && socket.readyState !== WebSocket.CLOSED) {
        setStatus("Disconnected");
        socket.close(1000, "user disconnected");
      } else {
        socket = null;
        setStatus("Disconnected");
      }
    }

    function connect() {
      if (socket && (socket.readyState === WebSocket.CONNECTING || socket.readyState === WebSocket.OPEN)) return;
      const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
      userDisconnect = false;
      error.hidden = true;
      setStatus("Connecting");
      const connection = new WebSocket(scheme + "//" + window.location.host + terminalURL());
      connection.binaryType = "arraybuffer";
      socket = connection;
      connection.addEventListener("open", function () {
        if (socket !== connection) return;
        setStatus("Connected");
        fitTerminal();
        terminal.focus();
      });
      connection.addEventListener("message", function (event) {
        if (socket !== connection) return;
        if (typeof event.data === "string") {
          terminal.write(event.data);
          return;
        }
        terminal.write(new Uint8Array(event.data));
      });
      connection.addEventListener("close", function () {
        if (socket !== connection) return;
        socket = null;
        setStatus("Disconnected");
        if (!userDisconnect) terminal.write("\r\n\r\n[Terminal session closed]\r\n");
      });
      connection.addEventListener("error", function () {
        if (socket !== connection) return;
        setStatus("Unavailable");
        error.hidden = false;
        error.textContent = "The terminal connection could not be established or was closed.";
      });
    }

    terminal.onData(function (value) {
      if (socket && socket.readyState === WebSocket.OPEN) socket.send(value);
    });
    terminal.onResize(sendResize);
    if (connectButton) connectButton.addEventListener("click", function () {
      if (socket && (socket.readyState === WebSocket.CONNECTING || socket.readyState === WebSocket.OPEN)) disconnect();
      else connect();
    });
    if (fitButton) fitButton.addEventListener("click", fitTerminal);
    if (fullscreenButton) fullscreenButton.addEventListener("click", toggleFullscreen);
    if (terminalUID) terminalUID.addEventListener("change", function () {
      if (socket) disconnect();
    });
    document.addEventListener("fullscreenchange", function () {
      updateFullscreenLabel();
      fitTerminal();
    });
    if (typeof ResizeObserver !== "undefined") new ResizeObserver(fitTerminal).observe(target);
    window.addEventListener("beforeunload", function () {
      if (socket) socket.close();
    });
    connect();
  }

  window.CowsTerminal = { init: initTerminal };
  const standaloneRoot = document.querySelector("[data-access]") ? null : document.querySelector("[data-terminal]");
  if (standaloneRoot) initTerminal(standaloneRoot);
})();
