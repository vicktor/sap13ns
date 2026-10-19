// sap13ns pricing table: the monthly/yearly switch of a theme's plans (the
// sap13ns theme's sap/plans partial). A [data-plans] box holds a
// [data-plan-switch] radio group, hidden until this script shows it: without
// JavaScript the table shows each plan's monthly price with its yearly price
// on a line below. With it the box gets .has-switch, and .is-yearly while the
// yearly view is chosen (the theme's CSS shows the matching prices), and each
// link with data-href-year goes there instead of its own href.
// Part of the site bundle (scripts/js-bundles.mjs). Dependency-free, CSP-safe.
(() => {
  "use strict";

  for (const box of document.querySelectorAll("[data-plans]")) {
    const group = box.querySelector("[data-plan-switch]");
    if (!group) continue;
    const radios = [...group.querySelectorAll('[role="radio"]')];
    if (radios.length === 0) continue;

    const choose = (radio, focus) => {
      for (const r of radios) {
        const on = r === radio;
        r.setAttribute("aria-checked", on ? "true" : "false");
        r.tabIndex = on ? 0 : -1;
      }
      const yearly = radio.dataset.interval === "year";
      box.classList.toggle("is-yearly", yearly);
      for (const a of box.querySelectorAll("a[data-href-year]")) {
        if (a.dataset.hrefMonth === undefined) a.dataset.hrefMonth = a.getAttribute("href");
        a.setAttribute("href", yearly ? a.dataset.hrefYear : a.dataset.hrefMonth);
      }
      if (focus) radio.focus();
    };

    // Buttons: Enter and Space click them.
    group.addEventListener("click", (e) => {
      const r = e.target.closest('[role="radio"]');
      if (r && radios.includes(r)) choose(r, false);
    });
    // A radio group: the arrow keys move the choice, Home and End go to
    // the first and the last.
    group.addEventListener("keydown", (e) => {
      const i = radios.indexOf(document.activeElement);
      if (i < 0) return;
      let j;
      switch (e.key) {
        case "ArrowRight":
        case "ArrowDown":
          j = (i + 1) % radios.length;
          break;
        case "ArrowLeft":
        case "ArrowUp":
          j = (i - 1 + radios.length) % radios.length;
          break;
        case "Home":
          j = 0;
          break;
        case "End":
          j = radios.length - 1;
          break;
        default:
          return;
      }
      e.preventDefault();
      choose(radios[j], true);
    });

    box.classList.add("has-switch");
    group.hidden = false;
    choose(radios.find((r) => r.getAttribute("aria-checked") === "true") || radios[0], false);
  }
})();
