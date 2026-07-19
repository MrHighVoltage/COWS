(function () {
  function formatBytes(value) {
    if (!Number.isFinite(value) || value < 0) return "0 B";
    if (value < 1024) return Math.round(value) + " B";
    const units = ["KiB", "MiB", "GiB"];
    let size = value;
    let unit = -1;
    do {
      size /= 1024;
      unit += 1;
    } while (size >= 1024 && unit < units.length - 1);
    return size.toFixed(size >= 100 ? 0 : size >= 10 ? 1 : 2) + " " + units[unit];
  }

  function formatDuration(seconds) {
    if (!Number.isFinite(seconds) || seconds < 0) return "";
    if (seconds < 60) return Math.ceil(seconds) + "s remaining";
    const minutes = Math.floor(seconds / 60);
    return minutes + "m " + Math.ceil(seconds % 60) + "s remaining";
  }

  function init(root) {
    if (!root) return;
    root.querySelectorAll("[data-file-upload]").forEach(function (form) {
      if (form.dataset.uploadInitialized) return;
      form.dataset.uploadInitialized = "true";

      const input = form.querySelector("[data-upload-input]");
      const submit = form.querySelector("[data-upload-submit]");
      const cancel = form.querySelector("[data-upload-cancel]");
      const status = form.parentElement.querySelector("[data-upload-status]");
      const progress = status.querySelector("[data-upload-progress]");
      const name = status.querySelector("[data-upload-name]");
      const percent = status.querySelector("[data-upload-percent]");
      const bytes = status.querySelector("[data-upload-bytes]");
      const rate = status.querySelector("[data-upload-rate]");
      const eta = status.querySelector("[data-upload-eta]");
      const message = status.querySelector("[data-upload-message]");
      let request;
      let startedAt;

      function showFile(file) {
        if (!file) {
          status.hidden = true;
          return;
        }
        status.hidden = false;
        name.textContent = file.name;
        percent.textContent = "0%";
        progress.value = 0;
        bytes.textContent = "0 B / " + formatBytes(file.size);
        rate.textContent = "Waiting";
        eta.textContent = "";
        message.textContent = "Ready to upload.";
      }

      input.addEventListener("change", function () {
        showFile(input.files[0]);
      });

      cancel.addEventListener("click", function () {
        if (request) request.abort();
      });

      form.addEventListener("submit", function (event) {
        event.preventDefault();
        const file = input.files[0];
        if (!file || request) return;

        request = new XMLHttpRequest();
        startedAt = performance.now();
        const payload = new FormData(form);
        status.hidden = false;
        submit.disabled = true;
        cancel.hidden = false;
        input.disabled = true;
        name.textContent = file.name;
        message.textContent = "Uploading...";
        rate.textContent = "Starting";
        eta.textContent = "";
        request.upload.addEventListener("progress", function (event) {
          if (!event.lengthComputable) {
            message.textContent = "Uploading...";
            return;
          }
          const elapsed = Math.max((performance.now() - startedAt) / 1000, 0.001);
          const currentRate = event.loaded / elapsed;
          const remaining = (event.total - event.loaded) / currentRate;
          const value = Math.round(event.loaded / event.total * 100);
          progress.value = value;
          percent.textContent = value + "%";
          bytes.textContent = formatBytes(event.loaded) + " / " + formatBytes(event.total);
          rate.textContent = formatBytes(currentRate) + "/s";
          eta.textContent = value < 100 ? formatDuration(remaining) : "";
        });
        request.addEventListener("load", function () {
          if (request.status >= 200 && request.status < 400) {
            progress.value = 100;
            percent.textContent = "100%";
            message.textContent = "Upload complete. Refreshing...";
            window.location.assign(request.responseURL || window.location.href);
            return;
          }
          message.textContent = "Upload failed. Please try again.";
          reset();
        });
        request.addEventListener("error", function () {
          message.textContent = "Upload failed. Check the connection and try again.";
          reset();
        });
        request.addEventListener("abort", function () {
          message.textContent = "Upload canceled.";
          reset();
        });
        request.open("POST", form.action, true);
        request.send(payload);
      });

      function reset() {
        request = null;
        submit.disabled = false;
        cancel.hidden = true;
        input.disabled = false;
      }
    });
  }

  window.CowsFiles = { init: init };
}());
