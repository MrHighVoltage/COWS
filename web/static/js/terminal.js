(function () {
  "use strict";

  const root = document.querySelector("[data-terminal]");
  if (!root || typeof Terminal === "undefined") return;

  const target = root.querySelector("#terminal");
  const status = root.querySelector("#terminal-status");
  const error = root.querySelector("#terminal-error");
  const terminal = new Terminal({
    convertEol: true,
    cursorBlink: true,
    scrollback: 2000,
    theme: { background: "#101820", foreground: "#f4f6f8" }
  });
  terminal.open(target);

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

  terminal.onData(function (value) {
    if (socket.readyState === WebSocket.OPEN) socket.send(value);
  });
  terminal.onResize(sendResize);
  socket.addEventListener("open", function () {
    setStatus("Connected");
    sendResize();
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
