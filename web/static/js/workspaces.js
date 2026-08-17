const filterButtons = Array.from(document.querySelectorAll("[data-workspace-filters] [data-filter]"));
const searchInput = document.querySelector("[data-workspace-search]");

if (filterButtons.length) {
  let activeFilter = "all";
  let searchText = "";

  // The panel and its rows are periodically replaced wholesale by the
  // workspace list's htmx poll (and individual rows by row actions), so
  // this never caches a reference to either - it always re-queries the
  // live DOM, otherwise it would silently start operating on a detached
  // subtree after the next swap.
  function applyFilters() {
    const panel = document.getElementById("workspace-list-panel");
    if (!panel) return;
    const counts = { all: 0, running: 0, stopped: 0, error: 0 };
    panel.querySelectorAll("tbody tr[data-status]").forEach((row) => {
      const status = row.dataset.status;
      counts.all += 1;
      if (Object.prototype.hasOwnProperty.call(counts, status)) counts[status] += 1;
      const matchesFilter = activeFilter === "all" || status === activeFilter;
      const matchesSearch = !searchText || (row.dataset.name || "").toLowerCase().includes(searchText);
      row.hidden = !(matchesFilter && matchesSearch);
    });
    filterButtons.forEach((button) => {
      const countEl = button.querySelector("[data-count]");
      if (countEl) countEl.textContent = String(counts[button.dataset.filter] || 0);
    });
  }

  filterButtons.forEach((button) => {
    button.addEventListener("click", () => {
      activeFilter = button.dataset.filter;
      filterButtons.forEach((candidate) => candidate.classList.toggle("active", candidate === button));
      applyFilters();
    });
  });

  if (searchInput) {
    searchInput.addEventListener("input", () => {
      searchText = searchInput.value.trim().toLowerCase();
      applyFilters();
    });
  }

  document.body.addEventListener("htmx:afterSwap", applyFilters);
  applyFilters();
}
