// Menus (Appearance → Menus): the items of a menu are reordered by dragging
// their grip, or with the arrow keys on it, and the new order is saved at
// once (POST data-menu-order, ids in order).
(() => {
  const lists = document.querySelectorAll("ol[data-menu-order]");
  if (!lists.length) return;
  const csrf = () => document.querySelector('input[name="csrf"]')?.value || "";
  const esc = (t) => String(t).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
  const toast = (category, title) =>
    document.dispatchEvent(new CustomEvent("basecoat:toast", { detail: { config: { category, title: esc(title) } } }));

  lists.forEach((list) => {
    const ids = () => [...list.children].map((li) => li.dataset.id);
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

    list.addEventListener("keydown", (e) => {
      const grip = e.target.closest(".menu-grip");
      if (!grip || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
      e.preventDefault();
      const li = grip.closest("li");
      if (e.key === "ArrowUp" && li.previousElementSibling) li.previousElementSibling.before(li);
      else if (e.key === "ArrowDown" && li.nextElementSibling) li.nextElementSibling.after(li);
      else return;
      grip.focus();
      save();
    });

    list.addEventListener("pointerdown", (e) => {
      const grip = e.target.closest(".menu-grip");
      if (!grip || e.button !== 0) return;
      e.preventDefault();
      const li = grip.closest("li");
      li.classList.add("dragging");
      // On the window: moving the item in the list loses a pointer capture.
      const move = (ev) => {
        const over = [...list.children].find((o) => {
          if (o === li) return false;
          const r = o.getBoundingClientRect();
          return ev.clientY >= r.top && ev.clientY <= r.bottom;
        });
        if (!over) return;
        const r = over.getBoundingClientRect();
        if (ev.clientY < r.top + r.height / 2) over.before(li);
        else over.after(li);
      };
      const up = () => {
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
        window.removeEventListener("pointercancel", up);
        li.classList.remove("dragging");
        grip.focus();
        save();
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
      window.addEventListener("pointercancel", up);
    });
  });
})();
