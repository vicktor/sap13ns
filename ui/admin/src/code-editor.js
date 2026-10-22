// The code editor of the admin panel: CodeMirror 6 on a textarea, bundled
// by npm run build into ui/static/vendor/codemirror/code-editor.min.js (no
// CDN). The textarea stays the field: every change is written back to it
// and fires "input", so the page's own handlers see what they saw before.
// The editor lives in a shadow root: there CodeMirror's styles go in as
// constructed stylesheets (CSSOM), which the CSP allows, where in the page
// itself it would add a <style> element, which the CSP blocks. The admin's
// colour tokens are custom properties, so they reach it.
// Only what a code field needs, to keep the file small: the legacy xml
// (HTML) and css modes rather than the full language packages.
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection, highlightSpecialChars } from "@codemirror/view";
import { EditorState } from "@codemirror/state";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { StreamLanguage, syntaxHighlighting, defaultHighlightStyle, bracketMatching, indentOnInput } from "@codemirror/language";
import { searchKeymap, highlightSelectionMatches } from "@codemirror/search";
import { html as htmlMode } from "@codemirror/legacy-modes/mode/xml";
import { css as cssMode } from "@codemirror/legacy-modes/mode/css";

const setup = [
  lineNumbers(), highlightActiveLineGutter(), highlightSpecialChars(), history(), drawSelection(), indentOnInput(),
  syntaxHighlighting(defaultHighlightStyle, { fallback: true }), bracketMatching(), highlightActiveLine(), highlightSelectionMatches(),
  keymap.of([...defaultKeymap, ...searchKeymap, ...historyKeymap, indentWithTab]),
];

const theme = EditorView.theme({
  "&": { minHeight: "var(--sap-code-min, 14rem)", maxHeight: "70vh", fontSize: "13px", color: "var(--foreground)", backgroundColor: "var(--background)", border: "1px solid var(--border)", borderRadius: "var(--radius-md, 6px)" },
  "&.cm-focused": { outline: "2px solid var(--ring)", outlineOffset: "1px" },
  ".cm-scroller": { overflow: "auto", fontFamily: "var(--font-mono, ui-monospace, monospace)", lineHeight: "1.55" },
  ".cm-content": { caretColor: "var(--foreground)" },
  ".cm-gutters": { backgroundColor: "var(--muted)", color: "var(--muted-foreground)", borderRight: "1px solid var(--border)" },
  ".cm-activeLine": { backgroundColor: "color-mix(in oklab, var(--muted) 55%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "color-mix(in oklab, var(--accent) 70%, transparent)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": { backgroundColor: "color-mix(in oklab, var(--primary) 25%, transparent)" },
  ".cm-tooltip": { backgroundColor: "var(--popover)", color: "var(--popover-foreground)", border: "1px solid var(--border)" },
});

/** attach puts an editor over textarea (lang "html" or "css") and returns it. */
function attach(textarea, opts = {}) {
  if (textarea._sapEditor) return textarea._sapEditor;
  const lang = StreamLanguage.define(opts.lang === "css" ? cssMode : htmlMode);
  const host = document.createElement("div");
  host.className = "sap-code-editor";
  const root = host.attachShadow({ mode: "open" });
  const view = new EditorView({
    root,
    parent: root,
    state: EditorState.create({
      doc: textarea.value,
      extensions: [
        setup, lang, theme, EditorView.lineWrapping,
        EditorState.readOnly.of(Boolean(textarea.readOnly)),
        EditorView.contentAttributes.of({ "aria-label": opts.label || textarea.labels?.[0]?.textContent || "Code", spellcheck: "false", autocapitalize: "off" }),
        EditorView.updateListener.of((u) => {
          if (!u.docChanged) return;
          textarea.value = u.state.doc.toString();
          textarea.dispatchEvent(new Event("input", { bubbles: true }));
        }),
      ],
    }),
  });
  if (opts.minHeight) host.style.setProperty("--sap-code-min", opts.minHeight);
  textarea.hidden = true;
  textarea.after(host);
  // The label still focuses the field.
  if (textarea.id) for (const l of textarea.labels || []) l.addEventListener("click", (e) => { e.preventDefault(); view.focus(); });
  const api = {
    view,
    focus: () => view.focus(),
    get value() { return view.state.doc.toString(); },
    set value(v) { view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v } }); },
    destroy() { view.destroy(); host.remove(); textarea.hidden = false; delete textarea._sapEditor; },
  };
  textarea._sapEditor = api;
  return api;
}

window.sapCodeEditor = { attach };
// Any textarea marked data-code-editor="html|css" gets one on load.
for (const t of document.querySelectorAll("textarea[data-code-editor]")) attach(t, { lang: t.dataset.codeEditor });
