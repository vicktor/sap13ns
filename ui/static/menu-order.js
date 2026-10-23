// Menus (Appearance → Menus): the items of a menu are reordered by dragging
// their grip, or with the arrow keys on it, and the new order is saved at
// once (POST data-menu-order, ids in order). An item's label and link are
// saved as soon as one of them changes.
(() => {
  const lists = document.querySelectorAll("ol[data-menu-order]");
  if (!lists.length) return;
  const csrf = () => document.querySelector('input[name="csrf"]')?.value || "";
  const esc = (t) => String(t).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
  const toast = (category, title) =>
    document.dispatchEvent(new CustomEvent("basecoat:toast", { detail: { config: { category, title: esc(title) } } }));

  const reduced = () => matchMedia("(prefers-reduced-motion: reduce)").matches;
  // flip runs change, which moves items of list, and slides every item
  // (but skip) from where it was to where it is now.
  const flip = (list, change, skip = []) => {
    const items = [...list.children].filter((el) => !skip.includes(el));
    const before = new Map(items.map((el) => [el, el.getBoundingClientRect().top]));
    change();
    if (reduced()) return;
    for (const el of items) {
      const d = before.get(el) - el.getBoundingClientRect().top;
      if (!d) continue;
      el.animate([{ transform: `translateY(${d}px)` }, { transform: "none" }], { duration: 180, easing: "cubic-bezier(.2,.8,.2,1)" });
    }
  };

  lists.forEach((list) => {
    const ids = () => [...list.children].filter((li) => li.dataset.id).map((li) => li.dataset.id);
    let saved = ids().join();
    const save = async () => {
      const order = ids();
      if (order.join() === saved) return;
      const body = new URLSearchParams();
      order.forEach((id) => body.append("ids", id));
      try {
        const res = await fetch(list.dataset.menuOrder, {
          method: "POST",
          headers: { "X-CSRF-Token": csrf() },
          body,
          credentials: "same-origin",
        });
        if (!res.ok) throw new Error(String(res.status));
        saved = order.join();
        toast("success", list.dataset.msgMoved);
      } catch {
        toast("error", list.dataset.msgFailed);
      }
    };

    list.addEventListener("change", async (e) => {
      const form = e.target.closest("form.menu-item-form");
      if (!form || !form.reportValidity()) return;
      try {
        const res = await fetch(form.action, {
          method: "POST",
          headers: { Accept: "application/json" },
          body: new URLSearchParams(new FormData(form)),
          credentials: "same-origin",
        });
        const data = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(data.error || list.dataset.msgFailed);
        toast("success", list.dataset.msgSaved);
      } catch (err) {
        toast("error", err.message);
      }
    });

    list.addEventListener("keydown", (e) => {
      const grip = e.target.closest(".menu-grip");
      if (!grip || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
      e.preventDefault();
      const li = grip.closest("li");
      const prev = li.previousElementSibling, next = li.nextElementSibling;
      if (e.key === "ArrowUp" && prev) flip(list, () => prev.before(li));
      else if (e.key === "ArrowDown" && next) flip(list, () => next.after(li));
      else return;
      grip.focus();
      save();
    });

    // A drag lifts the item: it follows the pointer above the page, and a
    // gap of its size opens where it would land, the others sliding aside.
    list.addEventListener("pointerdown", (e) => {
      const grip = e.target.closest(".menu-grip");
      if (!grip || e.button !== 0) return;
      e.preventDefault();
      const li = grip.closest("li");
      const box = li.getBoundingClientRect();
      const dy = e.clientY - box.top, dx = e.clientX - box.left;
      const gap = document.createElement("li");
      gap.className = "menu-gap";
      gap.setAttribute("aria-hidden", "true");
      gap.style.height = `${box.height}px`;
      li.before(gap);
      // CSSOM (allowed by the CSP): the lifted item is placed by script.
      li.classList.add("dragging");
      Object.assign(li.style, { width: `${box.width}px`, left: `${box.left}px`, top: `${box.top}px` });
      document.documentElement.classList.add("menu-dragging");
      const move = (ev) => {
        li.style.left = `${ev.clientX - dx}px`;
        li.style.top = `${ev.clientY - dy}px`;
        const others = [...list.children].filter((o) => o !== li && o !== gap);
        let before = null;
        for (const o of others) {
          const r = o.getBoundingClientRect();
          if (ev.clientY < r.top + r.height / 2) { before = o; break; }
        }
        if (before === gap.nextElementSibling || (!before && gap === list.lastElementChild)) return;
        flip(list, () => (before ? before.before(gap) : list.append(gap)), [li]);
      };
      const up = (ev) => {
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
        window.removeEventListener("pointercancel", up);
        const cancel = ev.type === "pointercancel";
        const to = gap.getBoundingClientRect();
        // The item glides into its gap, then takes its place.
        li.classList.add("dropping");
        Object.assign(li.style, cancel ? { left: `${box.left}px`, top: `${box.top}px` } : { left: `${to.left}px`, top: `${to.top}px` });
        const settle = () => {
          if (!cancel) gap.replaceWith(li);
          else gap.remove();
          li.classList.remove("dragging", "dropping");
          li.removeAttribute("style");
          document.documentElement.classList.remove("menu-dragging");
          grip.focus();
          save();
        };
        if (reduced()) settle();
        else setTimeout(settle, 160);
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
      window.addEventListener("pointercancel", up);
    });
  });
})();
