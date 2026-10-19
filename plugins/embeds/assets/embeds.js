/* Privacy-friendly embeds: click-to-load YouTube, Vimeo and TikTok players.
 *
 * The server renders every embed as a plain link (figure.embed > a.embed-link)
 * plus a hidden link to the privacy-friendly player (a.embed-player) whose
 * aria-label is the localized "Play video: …" text. This script adds a real
 * play button and creates the player iframe only when the visitor presses
 * it, so nothing is requested from the video site before that. All visible
 * text comes from the page, in the post's language.
 */
(function () {
  "use strict";

  var figures = document.querySelectorAll("figure.embed");
  if (!figures.length) {
    return;
  }

  function text(root, selector) {
    var el = root.querySelector(selector);
    return el ? el.textContent.replace(/\s+/g, " ").trim() : "";
  }

  function play(figure, frame, button, player, title, provider) {
    var iframe = document.createElement("iframe");
    iframe.src = player.href;
    iframe.title = provider && title.indexOf(provider) < 0 ? title + " (" + provider + ")" : title;
    iframe.setAttribute("allow", "accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share");
    iframe.setAttribute("allowfullscreen", "");
    // YouTube refuses to play embeds that are requested without a referrer.
    iframe.setAttribute("referrerpolicy", "strict-origin-when-cross-origin");
    frame.replaceChild(iframe, button);
    figure.classList.add("is-playing");
    iframe.focus();
  }

  function enhance(figure) {
    var link = figure.querySelector("a.embed-link");
    var player = figure.querySelector("a.embed-player");
    if (!link || !player || player.protocol !== "https:" || figure.classList.contains("is-enhanced")) {
      return;
    }
    var title = text(figure, ".embed-title");
    var provider = text(player, ".embed-provider");

    var frame = document.createElement("div");
    frame.className = "embed-frame";

    var button = document.createElement("button");
    button.type = "button";
    button.className = "embed-play";
    button.setAttribute("aria-label", player.getAttribute("aria-label") || title);

    var badge = document.createElement("span");
    badge.className = "embed-play-icon";
    badge.setAttribute("aria-hidden", "true");
    var icon = link.querySelector(".embed-icon svg");
    if (icon) {
      badge.appendChild(icon.cloneNode(true));
    }

    var label = document.createElement("span");
    label.className = "embed-play-text";
    var name = document.createElement("span");
    name.className = "embed-play-title";
    name.textContent = title;
    var source = document.createElement("span");
    source.className = "embed-play-provider";
    source.textContent = provider;
    label.appendChild(name);
    label.appendChild(source);

    // The provider's mark, drawn by embeds.css from a file of this plugin
    // (only TikTok has one: its placeholder has no thumbnail).
    var mark = document.createElement("span");
    mark.className = "embed-play-mark";
    mark.setAttribute("aria-hidden", "true");

    button.appendChild(mark);
    button.appendChild(badge);
    button.appendChild(label);
    frame.appendChild(button);
    figure.insertBefore(frame, figure.firstChild);
    figure.classList.add("is-enhanced");

    button.addEventListener("click", function () {
      play(figure, frame, button, player, title, provider);
    });
  }

  for (var i = 0; i < figures.length; i++) {
    enhance(figures[i]);
  }
})();
