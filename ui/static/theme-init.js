// Applies the saved or preferred colour scheme before the first paint.
// ?scheme=dark|light forces one for this view only (screenshots, shared links).
//
// The choice is kept in localStorage, which belongs to one host. On a
// network the page names the sites domain (<meta name="sap13ns-theme-domain">)
// and the choice also goes in a cookie for that domain, so that every site
// of the network, and the portal, open in it. window.sap13nsTheme.save is
// what the theme buttons call (admin.js, site.js).
(() => {
  const KEY = "themeMode";
  const COOKIE = "sap13ns-theme";
  const domain = () => {
    const d = document.querySelector('meta[name="sap13ns-theme-domain"]')?.content;
    const h = location.hostname;
    return d && (h === d || h.endsWith("." + d)) ? d : "";
  };
  const fromCookie = () => (document.cookie.match(/(?:^|;\s*)sap13ns-theme=(dark|light)(?:;|$)/) || [])[1];

  window.sap13nsTheme = {
    save(mode) {
      try { localStorage.setItem(KEY, mode); } catch (_) {}
      const d = domain();
      if (d) {
        document.cookie = `${COOKIE}=${mode}; Domain=${d}; Path=/; Max-Age=31536000; SameSite=Lax${location.protocol === "https:" ? "; Secure" : ""}`;
      }
    },
  };

  try {
    const forced = new URLSearchParams(location.search).get("scheme");
    const shared = domain() ? fromCookie() : undefined;
    let saved = null;
    try { saved = localStorage.getItem(KEY); } catch (_) {}
    if (shared && shared !== saved) {
      try { localStorage.setItem(KEY, shared); } catch (_) {}
      saved = shared;
    }
    const pick = forced === "dark" || forced === "light" ? forced : saved;
    const dark = pick ? pick === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
    document.documentElement.classList.toggle("dark", dark);
  } catch (_) {}
})();
