// Generic filter/search/pagination for the workspace-style data tables
// (per-user Workspaces overview, admin Runtime containers table). One
// toolbar (filter pills + search) plus one pager, both siblings of the
// panel they control rather than inside it - the panel's rows can be
// replaced wholesale (htmx poll, row-action swap), so control state lives
// in this closure, not the DOM, and every mutation re-reads live rows
// instead of caching a reference that would go stale after a swap.
document.querySelectorAll("[data-table-controls]").forEach((toolbar) => {
  const panelID = toolbar.dataset.panel;
  if (!panelID || !document.getElementById(panelID)) return;

  const filterButtons = Array.from(toolbar.querySelectorAll("[data-filter]"));
  const searchInput = toolbar.querySelector("[data-table-search]");
  const pager = document.querySelector(`[data-table-pager="${panelID}"]`);
  const pageSize = parseInt(toolbar.dataset.pageSize, 10) || 20;

  let activeFilter = "all";
  let searchText = "";
  let currentPage = 1;

  function rows() {
    const panel = document.getElementById(panelID);
    return panel ? Array.from(panel.querySelectorAll("tbody tr[data-status]")) : [];
  }

  function apply() {
    const liveRows = rows();
    const counts = { all: 0 };
    const matched = [];
    liveRows.forEach((row) => {
      const status = row.dataset.status;
      counts.all += 1;
      counts[status] = (counts[status] || 0) + 1;
      const matchesFilter = activeFilter === "all" || status === activeFilter;
      const matchesSearch = !searchText || (row.dataset.name || "").toLowerCase().includes(searchText);
      if (matchesFilter && matchesSearch) matched.push(row);
      else row.hidden = true;
    });
    filterButtons.forEach((button) => {
      const countEl = button.querySelector("[data-count]");
      if (countEl) countEl.textContent = String(counts[button.dataset.filter] || 0);
    });

    const totalPages = Math.max(1, Math.ceil(matched.length / pageSize));
    if (currentPage > totalPages) currentPage = totalPages;
    if (currentPage < 1) currentPage = 1;
    const start = (currentPage - 1) * pageSize;
    matched.forEach((row, index) => {
      row.hidden = index < start || index >= start + pageSize;
    });

    if (pager) {
      pager.hidden = matched.length <= pageSize;
      const status = pager.querySelector("[data-pager-status]");
      if (status) status.textContent = `Page ${currentPage} of ${totalPages}`;
      const prev = pager.querySelector("[data-pager-prev]");
      const next = pager.querySelector("[data-pager-next]");
      if (prev) prev.disabled = currentPage <= 1;
      if (next) next.disabled = currentPage >= totalPages;
    }
  }

  filterButtons.forEach((button) => {
    button.addEventListener("click", () => {
      activeFilter = button.dataset.filter;
      currentPage = 1;
      filterButtons.forEach((candidate) => candidate.classList.toggle("active", candidate === button));
      apply();
    });
  });

  if (searchInput) {
    searchInput.addEventListener("input", () => {
      searchText = searchInput.value.trim().toLowerCase();
      currentPage = 1;
      apply();
    });
  }

  if (pager) {
    pager.querySelector("[data-pager-prev]")?.addEventListener("click", () => {
      currentPage -= 1;
      apply();
    });
    pager.querySelector("[data-pager-next]")?.addEventListener("click", () => {
      currentPage += 1;
      apply();
    });
  }

  document.body.addEventListener("htmx:afterSwap", apply);
  apply();
});
