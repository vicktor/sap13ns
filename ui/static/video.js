/* Click-to-load YouTube and Vimeo players (core video embeds).
 *
 * The server renders every video as figure.sap-video > a.sap-video-link: a
 * plain link to the video page, with the poster and the title it cached.
 * This script turns the link into a real play button labelled "Play video:
 * …" and, only when it is pressed, replaces it with the provider's player:
 * youtube-nocookie.com (YouTube's privacy-enhanced mode) or
 * player.vimeo.com with dnt=1. Nothing is requested from YouTube or Vimeo
 * before that. Player URLs are checked against the same pattern as the
 * server's sanitizer (content.PlayerURLPattern). All text comes from the
 * page, in its language.
 */
(function () {
  "use strict";

  var PLAYER = /^https:\/\/(?:www\.youtube-nocookie\.com\/embed\/(?:[A-Za-z0-9_-]{11}\?autoplay=1(?:&start=[0-9]{1,6})?|videoseries\?list=[A-Za-z0-9_-]{10,64}&autoplay=1)|player\.vimeo\.com\/video\/[1-9][0-9]{0,11}\?autoplay=1&dnt=1(?:&h=[0-9a-f]{6,20})?(?:#t=[0-9]{1,6}s)?)$/;

  // enhance turns the links below root into buttons.
  function enhance(root) {
    var links = (root || document).querySelectorAll("a.sap-video-link[data-video-src]");
    for (var i = 0; i < links.length; i++) {
      var link = links[i];
      if (!PLAYER.test(link.getAttribute("data-video-src") || "")) {
        continue;
      }
      var button = document.createElement("button");
      button.type = "button";
      button.className = "sap-video-link";
      button.setAttribute("data-video-src", link.getAttribute("data-video-src"));
      button.setAttribute("data-video-title", link.getAttribute("data-video-title") || "");
      button.setAttribute("aria-label", link.getAttribute("data-video-label") || link.textContent.trim());
      while (link.firstChild) {
        button.appendChild(link.firstChild);
      }
      link.parentNode.replaceChild(button, link);
    }
  }

  // play replaces a player's link or button with the player itself.
  function play(el) {
    var src = el && el.getAttribute("data-video-src");
    if (!src || !PLAYER.test(src) || !el.parentNode) {
      return false;
    }
    var iframe = document.createElement("iframe");
    iframe.className = "sap-video-frame";
    iframe.src = src;
    iframe.title = el.getAttribute("data-video-title") || "";
    iframe.setAttribute("allow", "autoplay; fullscreen; picture-in-picture; encrypted-media");
    iframe.setAttribute("allowfullscreen", "");
    iframe.setAttribute("loading", "lazy");
    // YouTube refuses to play without a referrer: send the origin only.
    iframe.setAttribute("referrerpolicy", "strict-origin-when-cross-origin");
    iframe.setAttribute("sandbox", "allow-scripts allow-same-origin allow-presentation allow-popups allow-popups-to-escape-sandbox");
    var figure = el.closest("figure.sap-video");
    el.parentNode.replaceChild(iframe, el);
    if (figure) {
      figure.classList.add("is-playing");
    }
    iframe.focus();
    return true;
  }

  document.addEventListener("click", function (ev) {
    var el = ev.target && ev.target.closest ? ev.target.closest(".sap-video-link[data-video-src]") : null;
    if (!el || ev.defaultPrevented || ev.button !== 0) {
      return;
    }
    // A modified click on the link opens the video page, as links do.
    if (el.tagName === "A" && (ev.ctrlKey || ev.metaKey || ev.shiftKey || ev.altKey)) {
      return;
    }
    // The editor's own players are handled by the editor.
    if (el.closest("[data-editor-visual]")) {
      return;
    }
    if (play(el)) {
      ev.preventDefault();
    }
  });

  function start() {
    enhance(document);
    // Content shown later (the editor's preview) gets buttons too. A public
    // page (site.min.js) without players has nothing to watch.
    if (window.MutationObserver && document.querySelector("figure.sap-video, .sap-video-link, [data-editor-visual]")) {
      new MutationObserver(function (records) {
        for (var i = 0; i < records.length; i++) {
          for (var j = 0; j < records[i].addedNodes.length; j++) {
            var n = records[i].addedNodes[j];
            if (n.nodeType === 1 && (n.matches("a.sap-video-link") || n.querySelector("a.sap-video-link"))) {
              enhance(n.matches("a.sap-video-link") ? n.parentNode : n);
            }
          }
        }
      }).observe(document.body, { childList: true, subtree: true });
    }
  }

  window.sapVideo = { enhance: enhance, play: play, pattern: PLAYER };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start);
  } else {
    start();
  }
})();
