/* Contact form: the appointment calendar beside the form. The server
 * renders a link that opens the calendar at Google and a hidden link to its
 * embed (a.contact-booking-embed), whose aria-label is the localized "Show
 * the calendar". This script turns that into a button and creates the
 * calendar's iframe only when the visitor presses it, so nothing is
 * requested from Google before that. All text comes from the page.
 */
(function () {
  "use strict";

  var boxes = document.querySelectorAll(".contact-booking");
  if (!boxes.length) {
    return;
  }

  function enhance(box) {
    var embed = box.querySelector("a.contact-booking-embed");
    if (!embed || embed.protocol !== "https:" || embed.hostname !== "calendar.google.com" || box.classList.contains("is-enhanced")) {
      return;
    }
    var frame = document.createElement("div");
    frame.className = "contact-booking-frame";
    var button = document.createElement("button");
    button.type = "button";
    button.className = "btn contact-booking-show";
    button.textContent = embed.getAttribute("aria-label") || "Show the calendar";
    frame.appendChild(button);
    button.addEventListener("click", function () {
      var iframe = document.createElement("iframe");
      iframe.src = embed.href;
      var title = box.querySelector(".contact-booking-title");
      iframe.title = title ? title.textContent.trim() : "Google Calendar";
      iframe.setAttribute("loading", "eager");
      frame.replaceChild(iframe, button);
      box.classList.add("is-open");
      iframe.focus();
    });
    embed.parentNode.insertBefore(frame, embed);
    box.classList.add("is-enhanced");
  }

  for (var i = 0; i < boxes.length; i++) {
    enhance(boxes[i]);
  }
})();
