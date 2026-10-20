// The motion library's player (ui/static/motion.css): elements with an
// animation wait on its first frame until they come into view, then play
// it. data-sap-stagger delays each child after the one before, and
// data-sap-delay="ms" delays one element. Nothing runs without the class
// sap-motion on <html> (theme-init.js). window.sapMotion.replay() plays
// everything again, for the customiser's preview after a change.
(() => {
  const html = document.documentElement;
  const STEP = 0.09; // seconds between the children of data-sap-stagger
  const SELECTOR = "[data-sap-animate], [data-sap-stagger] > *, [data-sap-edit]";
  let observer = null;

  const animated = (el) => {
    const name = getComputedStyle(el).animationName;
    return !!name && name.split(",").some((n) => n.trim().startsWith("sap-"));
  };

  function delays() {
    for (const box of document.querySelectorAll("[data-sap-stagger]")) {
      [...box.children].forEach((el, i) => el.style.setProperty("--sap-delay", (i * STEP).toFixed(2) + "s"));
    }
    for (const el of document.querySelectorAll("[data-sap-delay]")) {
      const ms = parseInt(el.dataset.sapDelay, 10);
      if (ms > 0) el.style.setProperty("--sap-delay", Math.min(ms, 5000) / 1000 + "s");
    }
  }

  function start() {
    observer?.disconnect();
    if (!html.classList.contains("sap-motion")) return;
    const els = [...document.querySelectorAll(SELECTOR)].filter(animated);
    if (!els.length) return;
    delays();
    observer = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        e.target.classList.add("sap-in");
        observer.unobserve(e.target);
      }
    }, { rootMargin: "0px 0px -6% 0px", threshold: 0.12 });
    for (const el of els) observer.observe(el);
  }

  window.sapMotion = {
    replay() {
      const off = html.classList.contains("sap-motion-off") || matchMedia("(prefers-reduced-motion: reduce)").matches;
      html.classList.toggle("sap-motion", !off && "IntersectionObserver" in window);
      for (const el of document.querySelectorAll(".sap-in")) el.classList.remove("sap-in");
      void html.offsetWidth; // restart the animations from their first frame
      for (const el of document.querySelectorAll(SELECTOR)) {
        el.style.animation = "none";
        void el.offsetWidth;
        el.style.removeProperty("animation");
      }
      start();
    },
  };
  start();
})();
