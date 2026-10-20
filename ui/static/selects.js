// Glue between Basecoat's select and combobox components and the rest of the
// admin, which was written against native <select>s
// (templates: the "select" partial; markup: internal/web/selectbox.go).
//
// - The components announce a choice with a CustomEvent "change" on their
//   root. It is stopped here and replayed as native "input" and "change"
//   events from the hidden input, which has the field's name, value and
//   data-* attributes, so form listeners treat it like a <select>.
// - A select gets typeahead on its trigger, as a native select has.
// - A combobox with a search field keeps its value when the search is left
//   without a choice, does not submit the form on Enter, and gives the focus
//   back to its trigger on Escape.
// - Tabbing out of an open list closes it.
// - A list inside a box that scrolls or clips is placed fixed, so it shows
//   whole.
//
// The pure helpers are exported for ui/admin/test/selects.test.mjs.

const ROOTS = ".select:not(select), .combobox";

/** fold("Árbol") → "arbol": lower case without accents, for typeahead. */
export function fold(s) {
  return String(s ?? "").normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase();
}

/**
 * typeahead returns the index of the label that the typed buffer picks, or
 * -1. Like a native select: the first label that starts with the buffer;
 * one letter typed again moves on to the next label with that letter.
 */
export function typeahead(labels, buffer, current = -1) {
  const b = fold(buffer);
  if (!b) return -1;
  const folded = labels.map(fold);
  const same = [...b].every((c) => c === b[0]);
  if (same) {
    const n = folded.length;
    for (let k = 1; k <= n; k++) {
      const i = (current + k + n) % n;
      if (folded[i].startsWith(b[0])) return i;
    }
    return -1;
  }
  return folded.findIndex((l) => l.startsWith(b));
}

/**
 * leftFor says whether the focus, leaving a select or combobox for `to`,
 * went to something else on the page. A click on an option (a plain div)
 * moves it to nothing, or to the nearest focusable ancestor, such as the
 * admin's <main tabindex="-1">: the list must stay open for the click.
 */
export function leftFor(root, to) {
  return !!to && typeof to.contains === "function" && !root.contains(to) && !to.contains(root);
}

const hiddenOf = (root) => root.querySelector(":scope > input[type=hidden]");
const isPopup = (root) => root.matches(".combobox") && !!root.querySelector(':scope > button[aria-haspopup="listbox"]');

// The trigger label of a popup combobox is muted while it shows the
// placeholder (Basecoat's select does this itself).
function syncPlaceholder(root) {
  const label = root.matches(".combobox") ? root.querySelector(":scope > button [data-value]") : null;
  if (label) label.classList.toggle("text-muted-foreground", !hiddenOf(root)?.value);
}

const POPOVERS = ".select:not(select) > [data-popover], .combobox > [data-popover]";
const placed = new Map(); // fixed popover → its anchor

// clipper returns the nearest ancestor that would cut a popover off.
function clipper(el) {
  for (let p = el.parentElement; p && p !== el.ownerDocument.body; p = p.parentElement) {
    const s = getComputedStyle(p);
    if (s.overflowX !== "visible" || s.overflowY !== "visible") return p;
  }
  return null;
}

function place(pop, root) {
  if (!clipper(root)) return;
  const anchor = root.querySelector(':scope > button, :scope > input[role="combobox"]') || root;
  pop.classList.add("popover-fixed");
  placed.set(pop, anchor);
  position(pop, anchor);
}

function unplace(pop) {
  if (!placed.delete(pop)) return;
  pop.classList.remove("popover-fixed");
  for (const k of ["top", "left", "minWidth"]) pop.style[k] = "";
}

// position puts pop under its anchor, or above it when there is no room
// below, inside the viewport. A transformed ancestor (an opening dialog)
// moves the origin of position: fixed, so it is measured first.
function position(pop, anchor) {
  const r = anchor.getBoundingClientRect();
  pop.style.minWidth = Math.max(r.width, 144) + "px";
  pop.style.top = pop.style.left = "0px";
  const origin = pop.getBoundingClientRect();
  const h = pop.offsetHeight;
  const w = pop.offsetWidth;
  let top = r.bottom + 4;
  if (top + h > innerHeight - 8 && r.top - 4 - h >= 8) top = r.top - 4 - h;
  let left = pop.dataset.align === "end" ? r.right - w : r.left;
  left = Math.max(8, Math.min(left, innerWidth - w - 8));
  pop.style.top = top - origin.top + "px";
  pop.style.left = left - origin.left + "px";
}

export function wire(doc = document) {
  const committed = new WeakMap();

  doc.addEventListener("change", (e) => {
    const root = e.target;
    if (!(e instanceof CustomEvent) || !(root instanceof Element) || !root.matches(ROOTS)) return;
    e.stopPropagation();
    const input = hiddenOf(root);
    if (!input) return;
    committed.set(root, input.value);
    syncPlaceholder(root);
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  }, true);

  // When a list opens: inside a scrolling or clipping box (a table, a panel,
  // a dialog) it is placed fixed to the viewport so nothing cuts it, and it
  // shows the chosen option. A popup combobox clears its value while you
  // type a search; it is put back when the list closes without a choice.
  new MutationObserver((records) => {
    for (const r of records) {
      const pop = r.target;
      if (!(pop instanceof Element) || !pop.matches(POPOVERS)) continue;
      const root = pop.parentElement;
      const input = hiddenOf(root);
      if (pop.getAttribute("aria-hidden") === "false") {
        place(pop, root);
        if (!root.matches(".combobox")) continue;
        if (input) committed.set(root, input.value);
        const chosen = pop.querySelector('[role="option"][aria-selected="true"]');
        const lb = chosen?.closest('[role="listbox"]');
        if (chosen && lb) {
          chosen.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
          lb.scrollTop += chosen.getBoundingClientRect().top - lb.getBoundingClientRect().top - (lb.clientHeight - chosen.offsetHeight) / 2;
        }
      } else {
        unplace(pop);
        if (isPopup(root) && input && committed.has(root) && input.value !== committed.get(root)) {
          input.value = committed.get(root);
          root.refresh?.();
          syncPlaceholder(root);
        }
      }
    }
  }).observe(doc.documentElement, { subtree: true, attributes: true, attributeFilter: ["aria-hidden"] });

  const reposition = (e) => {
    for (const [pop, anchor] of placed) if (!(e?.target instanceof Node && pop.contains(e.target))) position(pop, anchor);
  };
  addEventListener("scroll", reposition, { capture: true, passive: true });
  addEventListener("resize", () => reposition());

  // An input combobox (the time zone) that is closed while focused, after
  // Escape or a choice, reopens on typing and then puts its stored label
  // back over what was typed; open it first and keep the text.
  doc.addEventListener("input", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLInputElement) || t.getAttribute("role") !== "combobox") return;
    const root = t.closest(".combobox");
    if (!root || isPopup(root) || root.querySelector(":scope > [data-popover]")?.getAttribute("aria-hidden") !== "true") return;
    const typed = t.value;
    t.click();
    t.value = typed;
  }, true);

  let buffer = "";
  let bufferTimer = 0;
  doc.addEventListener("keydown", (e) => {
    const t = e.target;
    if (!(t instanceof Element)) return;

    // Search field of a popup combobox.
    const search = t.closest('.combobox > [data-popover] input[role="combobox"]');
    if (search) {
      const root = search.closest(".combobox");
      if (!isPopup(root)) return;
      if (e.key === "Enter") e.preventDefault();
      if (e.key === "Escape") root.querySelector(":scope > button")?.focus();
      return;
    }

    // Typeahead on a select's trigger.
    const trigger = t.closest(".select:not(select) > button");
    if (!trigger || e.ctrlKey || e.metaKey || e.altKey || e.key.length !== 1) return;
    if (e.key === " " && !buffer) return;
    const root = trigger.parentElement;
    const options = [...root.querySelectorAll('[role="option"]:not([aria-disabled="true"])')];
    const open = trigger.getAttribute("aria-expanded") === "true";
    const current = options.findIndex((o) => (open ? o.classList.contains("active") : o.getAttribute("aria-selected") === "true"));
    e.preventDefault();
    buffer += e.key;
    clearTimeout(bufferTimer);
    bufferTimer = setTimeout(() => { buffer = ""; }, 600);
    const i = typeahead(options.map((o) => o.dataset.label || o.textContent.trim()), buffer, current);
    if (i < 0) return;
    const o = options[i];
    if (open) {
      o.dispatchEvent(new MouseEvent("mousemove", { bubbles: true }));
      o.scrollIntoView?.({ block: "nearest" });
    } else if (root.value !== o.dataset.value) {
      root.value = o.dataset.value;
    }
  });

  // Tabbing out of an open list closes it. The components handle clicks.
  doc.addEventListener("focusout", (e) => {
    const root = e.target instanceof Element ? e.target.closest(ROOTS) : null;
    if (root && leftFor(root, e.relatedTarget)) root.close?.(false);
  });
}

if (typeof document !== "undefined") wire();
