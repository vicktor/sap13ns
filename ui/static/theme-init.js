// Applies the saved or preferred colour scheme before the first paint.
// ?scheme=dark|light forces one for this view only (screenshots, shared links).
(() => {
  try {
    const forced = new URLSearchParams(location.search).get("scheme");
    const saved = localStorage.getItem("themeMode");
    const pick = forced === "dark" || forced === "light" ? forced : saved;
    const dark = pick ? pick === "dark" : matchMedia("(prefers-color-scheme: dark)").matches;
    document.documentElement.classList.toggle("dark", dark);
  } catch (_) {}
})();
