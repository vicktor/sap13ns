/* YouTube gallery: click-to-load players and the carousel's buttons.
 *
 * The server renders every video as a link to YouTube (li.ytg-item >
 * a.ytg-link) plus a hidden link to the youtube-nocookie player
 * (a.ytg-player) whose aria-label is the localized "Play video: …" text.
 * This script turns each thumbnail into a play button and creates the
 * player iframe only when the visitor presses it, so nothing is requested
 * from the player's site before that. A gallery plays one video at a time.
 * All visible text and labels come from the page, in its language.
 */
(function () {
  "use strict";

  var galleries = document.querySelectorAll(".ytg");
  if (!galleries.length) {
    return;
  }
  var reduced = window.matchMedia ? window.matchMedia("(prefers-reduced-motion: reduce)") : null;

  function enhance(gallery, item, players) {
    var link = item.querySelector("a.ytg-link");
    var player = item.querySelector("a.ytg-player");
    var thumb = link && link.querySelector(".ytg-thumb");
    var body = link && link.querySelector(".ytg-body");
    if (!link || !player || !thumb || !body || player.protocol !== "https:" || item.classList.contains("is-enhanced")) {
      return;
    }
    var title = body.querySelector(".ytg-title");
    var name = title ? title.textContent.replace(/\s+/g, " ").trim() : "";

    var media = document.createElement("div");
    media.className = "ytg-media";
    var button = document.createElement("button");
    button.type = "button";
    button.className = "ytg-play";
    button.setAttribute("aria-label", player.getAttribute("aria-label") || name);
    button.appendChild(thumb);
    media.appendChild(button);
    // The title (with the spoken duration) leaves the link, which goes:
    // the button names the video, and the player has its own YouTube link.
    item.insertBefore(media, link);
    item.insertBefore(body, link);
    item.removeChild(link);
    item.classList.add("is-enhanced");

    var entry = {
      stop: function () {
        if (!item.classList.contains("is-playing")) {
          return;
        }
        var frame = media.querySelector("iframe");
        if (frame) {
          media.replaceChild(button, frame);
        }
        item.classList.remove("is-playing");
      }
    };
    players.push(entry);

    button.addEventListener("click", function () {
      for (var i = 0; i < players.length; i++) {
        if (players[i] !== entry) {
          players[i].stop();
        }
      }
      var iframe = document.createElement("iframe");
      iframe.src = player.href;
      iframe.title = name ? name + " (YouTube)" : "YouTube";
      iframe.setAttribute("allow", "accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share");
      iframe.setAttribute("allowfullscreen", "");
      // YouTube refuses to play embeds that are requested without a referrer.
      iframe.setAttribute("referrerpolicy", "strict-origin-when-cross-origin");
      media.replaceChild(iframe, button);
      item.classList.add("is-playing");
      iframe.focus();
    });
  }

  function navButton(source, className) {
    var button = document.createElement("button");
    button.type = "button";
    button.className = "ytg-nav " + className;
    button.setAttribute("aria-label", source.getAttribute("aria-label") || "");
    var icon = source.querySelector("svg");
    if (icon) {
      button.appendChild(icon.cloneNode(true));
    }
    return button;
  }

  function carousel(gallery) {
    var list = gallery.querySelector(".ytg-list");
    var ui = gallery.querySelector(".ytg-ui");
    var prevSource = ui && ui.querySelector(".ytg-ui-prev");
    var nextSource = ui && ui.querySelector(".ytg-ui-next");
    if (!list || !prevSource || !nextSource) {
      return;
    }
    var head = gallery.querySelector(".ytg-head");
    if (!head) {
      head = document.createElement("div");
      head.className = "ytg-head";
      gallery.insertBefore(head, list);
    }
    var controls = document.createElement("div");
    controls.className = "ytg-controls";
    var prev = navButton(prevSource, "ytg-prev");
    var next = navButton(nextSource, "ytg-next");
    controls.appendChild(prev);
    controls.appendChild(next);
    head.appendChild(controls);

    // aria-disabled instead of disabled keeps the focus on a button that
    // reaches the end of the row.
    function update() {
      var max = list.scrollWidth - list.clientWidth;
      var pos = Math.abs(list.scrollLeft);
      controls.hidden = max <= 1;
      if (controls.hidden && !gallery.querySelector(".ytg-heading")) {
        head.hidden = true;
      } else {
        head.hidden = false;
      }
      prev.setAttribute("aria-disabled", pos <= 1 ? "true" : "false");
      next.setAttribute("aria-disabled", pos >= max - 1 ? "true" : "false");
    }

    function step(direction) {
      var item = list.querySelector(".ytg-item");
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

  for (var g = 0; g < galleries.length; g++) {
    var gallery = galleries[g];
    var players = [];
    var items = gallery.querySelectorAll("li.ytg-item");
    for (var i = 0; i < items.length; i++) {
      enhance(gallery, items[i], players);
    }
    if (gallery.classList.contains("ytg-carousel")) {
      carousel(gallery);
    }
  }
})();
