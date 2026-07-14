(function () {
  "use strict";

  const root = document.querySelector("[data-terminal]");
  if (!root || typeof Terminal === "undefined") return;

  const target = root.querySelector("#terminal");
  const status = root.querySelector("#terminal-status");
  const error = root.querySelector("#terminal-error");
  const fitButton = root.querySelector("[data-terminal-fit]");
  const fullscreenButton = root.querySelector("[data-terminal-fullscreen]");
  const terminal = new Terminal({
    convertEol: true,
    cursorBlink: true,
    scrollback: 2000,
    theme: { background: "#101820", foreground: "#f4f6f8" }
  });
  terminal.open(target);
  const fitAddon = typeof FitAddon !== "undefined" ? new FitAddon.FitAddon() : null;
  if (fitAddon) terminal.loadAddon(fitAddon);

  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  const socket = new WebSocket(scheme + "//" + window.location.host + root.dataset.terminalUrl);
  socket.binaryType = "arraybuffer";

  function setStatus(value) {
    status.textContent = value;
  }

  function sendResize() {
    if (socket.readyState !== WebSocket.OPEN) return;
    socket.send(JSON.stringify({ type: "resize", cols: terminal.cols, rows: terminal.rows }));
  }

  function fitTerminal() {
    if (!fitAddon) return;
    window.requestAnimationFrame(function () {
      fitAddon.fit();
      sendResize();
    });
  }

  function updateFullscreenLabel() {
    if (fullscreenButton) fullscreenButton.textContent = document.fullscreenElement === root ? "Exit full screen" : "Full screen";
  }

  async function toggleFullscreen() {
    try {
      if (document.fullscreenElement === root) {
        await document.exitFullscreen();
      } else {
        await root.requestFullscreen();
      }
    } catch (requestError) {
      error.hidden = false;
      error.textContent = "Full screen mode is not available in this browser.";
    }
  }

  terminal.onData(function (value) {
    if (socket.readyState === WebSocket.OPEN) socket.send(value);
  });
  terminal.onResize(sendResize);
  if (fitButton) fitButton.addEventListener("click", fitTerminal);
  if (fullscreenButton) fullscreenButton.addEventListener("click", toggleFullscreen);
  document.addEventListener("fullscreenchange", function () {
    updateFullscreenLabel();
    fitTerminal();
  });
  if (typeof ResizeObserver !== "undefined") new ResizeObserver(fitTerminal).observe(target);
  socket.addEventListener("open", function () {
    setStatus("Connected");
    fitTerminal();
    terminal.focus();
  });
  socket.addEventListener("message", function (event) {
    if (typeof event.data === "string") {
      terminal.write(event.data);
      return;
    }
    terminal.write(new Uint8Array(event.data));
  });
  socket.addEventListener("close", function () {
    setStatus("Disconnected");
    terminal.write("\r\n\r\n[Terminal session closed]\r\n");
  });
  socket.addEventListener("error", function () {
    setStatus("Unavailable");
    error.hidden = false;
    error.textContent = "The terminal connection could not be established or was closed.";
  });
  window.addEventListener("beforeunload", function () { socket.close(); });
})();
