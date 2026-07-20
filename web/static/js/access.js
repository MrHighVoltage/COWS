const shell = document.querySelector("[data-access]");
if (shell) {
  const tabs = Array.from(shell.querySelectorAll("[data-access-tab]"));
  const panels = Array.from(shell.querySelectorAll("[data-access-panel]"));
  const controls = Array.from(shell.querySelectorAll("[data-access-controls]"));
  const loaded = new Set();

  function loadScript(source) {
    const existing = document.querySelector('script[data-cows-script="' + source + '"]');
    if (existing) return Promise.resolve();
    return new Promise((resolve, reject) => {
      const script = document.createElement("script");
      script.src = source;
      script.dataset.cowsScript = source;
      script.onload = resolve;
      script.onerror = reject;
      document.head.appendChild(script);
    });
  }

  async function activateService(tab) {
    if (loaded.has(tab)) return;
    try {
      if (tab === "terminal") {
        await loadScript("/static/vendor/xterm/xterm.js");
        await loadScript("/static/vendor/xterm/xterm-addon-fit.js");
        await loadScript("/static/js/terminal.js");
        window.CowsTerminal.init(shell.querySelector("[data-terminal]"));
      } else if (tab === "desktop") {
        const module = await import("/static/js/desktop.js");
        module.initDesktop(shell.querySelector("[data-desktop]"));
      } else if (tab === "files") {
        await loadScript("/static/js/files.js");
        const target = shell.querySelector("[data-access-files]");
        if (!target.querySelector(".file-panel")) {
          const params = new URLSearchParams(window.location.search);
          const query = new URLSearchParams();
          if (params.get("mount")) query.set("mount", params.get("mount"));
          if (params.get("path")) query.set("path", params.get("path"));
          const response = await fetch(shell.dataset.filesUrl + (query.toString() ? "?" + query.toString() : ""), { credentials: "same-origin", headers: { "Accept": "text/html" } });
          target.innerHTML = await response.text();
          if (window.Alpine) window.Alpine.initTree(target);
          if (!response.ok) throw new Error("file listing request failed");
        }
        window.CowsFiles.init(target);
      }
      loaded.add(tab);
    } catch (error) {
      const target = shell.querySelector('[data-access-panel="' + tab + '"]');
      if (target && !target.querySelector(".alert")) target.insertAdjacentHTML("beforeend", '<p class="alert alert-error" role="alert">This access method could not be initialized.</p>');
    }
  }

  function activate(tab) {
    const selected = shell.querySelector('[data-access-tab="' + tab + '"]');
    if (!selected) return;
    tabs.forEach((button) => {
      const active = button === selected;
      button.classList.toggle("active", active);
      button.setAttribute("aria-selected", active ? "true" : "false");
      button.tabIndex = active ? 0 : -1;
    });
    panels.forEach((panel) => {
      panel.hidden = panel.dataset.accessPanel !== tab;
    });
    controls.forEach((control) => {
      control.hidden = control.dataset.accessControls !== tab;
    });
    const url = new URL(window.location.href);
    url.searchParams.set("tab", tab);
    window.history.replaceState({}, "", url);
    activateService(tab);
  }

  tabs.forEach((button, index) => {
    button.addEventListener("click", () => activate(button.dataset.accessTab));
    button.addEventListener("keydown", (event) => {
      if (event.key !== "ArrowRight" && event.key !== "ArrowLeft") return;
      event.preventDefault();
      const next = tabs[(index + (event.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length];
      next.focus();
      activate(next.dataset.accessTab);
    });
  });

  activate(shell.dataset.initialTab || (tabs[0] && tabs[0].dataset.accessTab));
}
