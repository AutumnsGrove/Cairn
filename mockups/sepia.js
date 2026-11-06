/* Sepia mockups: just enough behaviour to feel the interactions. No framework, no network. */

const STAR = '<svg aria-hidden="true"><use href="#i-star"/></svg>';

/* Quarter-star rating: drag across the stars, snaps to 0.25; the +/- buttons nudge by 0.25. */
function mountRating(root) {
  const stars = root.querySelector(".stars");
  const num = root.querySelector(".num");
  let value = parseFloat(root.dataset.value || "0");

  function paint() {
    stars.innerHTML = "";
    for (let n = 1; n <= 5; n++) {
      const pct = Math.max(0, Math.min(1, value - (n - 1))) * 100;
      const s = document.createElement("span");
      s.className = "star";
      s.innerHTML = '<span class="base">' + STAR + '</span><span class="fillbox" style="width:' + pct + '%">' + STAR + "</span>";
      stars.appendChild(s);
    }
    num.textContent = value ? value.toFixed(2).replace(/0$/, "") : "Not rated";
    num.classList.toggle("dim", !value);
    stars.setAttribute("aria-label", value + " out of 5");
  }
  function set(v) { value = Math.max(0.25, Math.min(5, Math.round(v * 4) / 4)); paint(); }
  function fromPointer(e) {
    const r = stars.getBoundingClientRect();
    set(((e.clientX - r.left) / r.width) * 5 + 0.12);
  }
  stars.addEventListener("pointerdown", (e) => { stars.setPointerCapture(e.pointerId); fromPointer(e); });
  stars.addEventListener("pointermove", (e) => { if (stars.hasPointerCapture(e.pointerId)) fromPointer(e); });
  root.querySelectorAll("[data-step]").forEach((b) => b.addEventListener("click", () => set(value + parseFloat(b.dataset.step))));
  paint();
}
document.querySelectorAll("[data-rating]").forEach(mountRating);

/* Single-choice groups and toggles */
document.querySelectorAll("[data-choice]").forEach((group) => {
  group.addEventListener("click", (e) => {
    const b = e.target.closest("button");
    if (!b || !group.contains(b)) return;
    group.querySelectorAll("button").forEach((x) => { x.setAttribute("aria-checked", "false"); x.classList.remove("sel"); });
    b.setAttribute("aria-checked", "true");
  });
});
document.querySelectorAll("[data-pills]").forEach((group) => {
  group.addEventListener("click", (e) => {
    const b = e.target.closest(".pill");
    if (!b) return;
    group.querySelectorAll(".pill").forEach((x) => x.setAttribute("aria-pressed", "false"));
    b.setAttribute("aria-pressed", "true");
    b.classList.remove("hint");
  });
});
document.querySelectorAll("[data-toggle]").forEach((b) =>
  b.addEventListener("click", () => b.setAttribute("aria-pressed", String(b.getAttribute("aria-pressed") !== "true")))
);

/* Describe it: category tabs and multi-select chip clouds */
document.querySelectorAll("[data-tabs]").forEach((root) => {
  const tabs = root.querySelectorAll("[role=tab]");
  const panels = root.parentElement.querySelectorAll("[role=tabpanel]");
  tabs.forEach((t) => t.addEventListener("click", () => {
    tabs.forEach((x) => x.setAttribute("aria-selected", String(x === t)));
    panels.forEach((p) => { p.hidden = p.id !== t.getAttribute("aria-controls"); });
  }));
});
document.querySelectorAll(".cloud").forEach((c) => c.addEventListener("click", (e) => {
  const b = e.target.closest(".pick");
  if (!b) return;
  b.setAttribute("aria-pressed", String(b.getAttribute("aria-pressed") !== "true"));
  b.classList.remove("hint");
  const panel = c.closest("[role=tabpanel]");
  const n = panel.querySelectorAll(".pick[aria-pressed=true]").length;
  const tc = document.querySelector('[aria-controls="' + panel.id + '"] .tc');
  if (tc) tc.textContent = n || "";
}));
