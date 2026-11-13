// Menus and widgets (Appearance): the items of a list are reordered by dragging
// their grip, or with the arrow keys on it, and the new order is saved at
// once (POST data-menu-order, ids in order). An item's label and link are
// saved as soon as one of them changes (widgets keep their Save button).
// Lists with data-menu-move (the menus) also take the items of each other:
// an item dragged, or moved with the arrow keys past the end of its list,
// into another one is saved there (POST data-menu-move: id, the list's
// data-location and its ids in order).
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
  const flip = (lists, change, skip = []) => {
    const items = [].concat(lists).flatMap((l) => [...l.children]).filter((el) => !skip.includes(el));
    const before = new Map(items.map((el) => [el, el.getBoundingClientRect().top]));
    change();
    if (reduced()) return;
    for (const el of items) {
      const d = before.get(el) - el.getBoundingClientRect().top;
      if (!d) continue;
      el.animate([{ transform: `translateY(${d}px)` }, { transform: "none" }], { duration: 180, easing: "cubic-bezier(.2,.8,.2,1)" });
    }
  };

  const ids = (list) => [...list.children].filter((li) => li.dataset.id).map((li) => li.dataset.id);
  const saved = new Map([...lists].map((l) => [l, ids(l).join()]));
  const movable = [...lists].filter((l) => l.dataset.menuMove);
  const post = async (url, body) => {
    const res = await fetch(url, { method: "POST", headers: { "X-CSRF-Token": csrf() }, body, credentials: "same-origin" });
    if (!res.ok) throw new Error(String(res.status));
  };
  // One save at a time, in the order of the moves, so that the server
  // always ends with what is on screen.
  let queue = Promise.resolve();
  const save = (list) => {
    queue = queue.then(async () => {
      const order = ids(list);
      if (order.join() === saved.get(list)) return;
      const body = new URLSearchParams();
      order.forEach((id) => body.append("ids", id));
      try {
        await post(list.dataset.menuOrder, body);
        saved.set(list, order.join());
        toast("success", list.dataset.msgMoved);
      } catch {
        toast("error", list.dataset.msgFailed);
      }
    });
  };
  // moved saves li in to, the list it is in now, after it left from.
  const moved = (li, from, to) => {
    for (const l of [from, to]) {
      const empty = l.parentElement.querySelector("[data-menu-empty]");
      if (empty) empty.hidden = ids(l).length > 0;
    }
    queue = queue.then(async () => {
      const order = ids(to);
      const body = new URLSearchParams({ id: li.dataset.id, location: to.dataset.location });
      order.forEach((id) => body.append("ids", id));
      try {
        await post(to.dataset.menuMove, body);
        saved.set(to, order.join());
        saved.set(from, ids(from).join());
        toast("success", to.dataset.msgMovedMenu);
      } catch {
        toast("error", to.dataset.msgFailed);
      }
    });
  };
  const settled = (li, from) => {
    const to = li.parentElement;
    if (to === from) save(to);
    else moved(li, from, to);
  };
  // listAt is the list an item dragged from list to (x, y) would land in:
  // the one whose card is under the pointer, else the one it is over now.
  const listAt = (list, now, x, y) => {
    if (!list.dataset.menuMove) return list;
    for (const l of movable) {
      const r = (l.closest(".card") || l).getBoundingClientRect();
      if (x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) return l;
    }
    return now;
  };

  lists.forEach((list) => {
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
      // Past the end of its menu, the item goes to the one before or after.
      const other = movable[movable.indexOf(list) + (e.key === "ArrowUp" ? -1 : 1)];
      if (e.key === "ArrowUp" && prev) flip(list, () => prev.before(li));
      else if (e.key === "ArrowDown" && next) flip(list, () => next.after(li));
      else if (other && movable.includes(list)) flip([list, other], () => (e.key === "ArrowUp" ? other.append(li) : other.prepend(li)));
      else return;
      grip.focus();
      settled(li, list);
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
        const over = gap.parentElement;
        const to = listAt(list, over, ev.clientX, ev.clientY);
        const others = [...to.children].filter((o) => o !== li && o !== gap);
        let before = null;
        for (const o of others) {
          const r = o.getBoundingClientRect();
          if (ev.clientY < r.top + r.height / 2) { before = o; break; }
        }
        // The lifted item stays in its list until it is dropped, so the
        // gap's neighbour is looked for past it.
        let next = gap.nextElementSibling;
        if (next === li) next = li.nextElementSibling;
        if (to === over && before === next) return;
        flip(to === over ? to : [over, to], () => (before ? before.before(gap) : to.append(gap)), [li]);
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
          settled(li, list);
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
