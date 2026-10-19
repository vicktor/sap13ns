/* Instagram feed: the carousel's previous and next buttons.
 *
 * The server renders each feed as a list of links (ul.igf-list) that
 * scrolls and snaps without this script, plus hidden labels and icons for
 * the buttons (span.igf-ui), in the page's language. This script only adds
 * the two buttons; it changes nothing else, and loads nothing.
 */
(function () {
  "use strict";

  var feeds = document.querySelectorAll(".igf-carousel");
  if (!feeds.length) {
    return;
  }
  var reduced = window.matchMedia ? window.matchMedia("(prefers-reduced-motion: reduce)") : null;

  function navButton(source, className) {
    var button = document.createElement("button");
    button.type = "button";
    button.className = "igf-nav " + className;
    button.setAttribute("aria-label", source.getAttribute("aria-label") || "");
    var icon = source.querySelector("svg");
    if (icon) {
      var copy = icon.cloneNode(true);
      copy.setAttribute("aria-hidden", "true");
      button.appendChild(copy);
    }
    return button;
  }

  function carousel(feed) {
    var list = feed.querySelector(".igf-list");
    var ui = feed.querySelector(".igf-ui");
    var prevSource = ui && ui.querySelector(".igf-ui-prev");
    var nextSource = ui && ui.querySelector(".igf-ui-next");
    if (!list || !prevSource || !nextSource || feed.querySelector(".igf-controls")) {
      return;
    }
    var head = feed.querySelector(".igf-head");
    if (!head) {
      head = document.createElement("div");
      head.className = "igf-head";
      feed.insertBefore(head, list);
    }
    var controls = document.createElement("div");
    controls.className = "igf-controls";
    var prev = navButton(prevSource, "igf-prev");
    var next = navButton(nextSource, "igf-next");
    controls.appendChild(prev);
    controls.appendChild(next);
    head.appendChild(controls);

    // aria-disabled instead of disabled keeps the focus on a button that
    // reaches the end of the row.
    function update() {
      var max = list.scrollWidth - list.clientWidth;
      var pos = Math.abs(list.scrollLeft);
      controls.hidden = max <= 1;
      head.hidden = controls.hidden && !feed.querySelector(".igf-heading");
      prev.setAttribute("aria-disabled", pos <= 1 ? "true" : "false");
      next.setAttribute("aria-disabled", pos >= max - 1 ? "true" : "false");
    }

    function step(direction) {
      var item = list.querySelector(".igf-item");
      var width = item ? item.getBoundingClientRect().width : list.clientWidth;
      var gap = parseFloat(window.getComputedStyle(list).columnGap) || 0;
      var perPage = Math.max(1, Math.floor((list.clientWidth + gap) / (width + gap)));
      if (document.documentElement.dir === "rtl") {
        direction = -direction;
      }
      list.scrollBy({ left: direction * perPage * (width + gap), behavior: reduced && reduced.matches ? "auto" : "smooth" });
    }

    prev.addEventListener("click", function () {
      if (prev.getAttribute("aria-disabled") !== "true") {
        step(-1);
      }
    });
    next.addEventListener("click", function () {
      if (next.getAttribute("aria-disabled") !== "true") {
        step(1);
      }
    });

    var pending = false;
    function schedule() {
      if (!pending) {
        pending = true;
        window.requestAnimationFrame(function () {
          pending = false;
          update();
        });
      }
    }
    list.addEventListener("scroll", schedule, { passive: true });
    window.addEventListener("resize", schedule);
    update();
  }

  for (var i = 0; i < feeds.length; i++) {
    carousel(feeds[i]);
  }
})();
