// The AI page (ui/admin/templates/ai_settings.html): the site's own
// provider settings show only when the site does not use the AI of its
// plan, and the image provider's key and address only for another
// provider than the text one.
const form = document.querySelector("form[action='/admin/settings/ai']");
if (form) {
  const update = () => {
    const network = form.querySelector("input[name=source][value=network]:checked");
    for (const el of form.querySelectorAll("[data-ai-own]")) el.hidden = !!network;
    const img = form.elements.namedItem("image_provider");
    for (const el of form.querySelectorAll("[data-image-own]")) el.hidden = !img || !img.value;
  };
  form.addEventListener("change", update);
  // Basecoat selects change on a click in their list.
  form.addEventListener("click", () => setTimeout(update));
  update();
}
