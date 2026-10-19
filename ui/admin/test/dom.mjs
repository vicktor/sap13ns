// A tiny HTML parser for the editor tests: it builds the minimal node tree
// (nodeType, nodeName, childNodes, data, getAttribute) that the editor's
// DOM -> Markdown converter reads, so no DOM library is needed.
const VOID = new Set(["br", "img", "hr", "meta", "link", "input", "col", "area", "base", "wbr", "source"]);
const AUTOCLOSE_P = new Set(["p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "table", "blockquote", "pre", "hr"]);
const decode = (s) => s.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (all, e) => {
  if (e[0] === "#") return String.fromCodePoint(e[1].toLowerCase() === "x" ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10));
  return { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: "\u00A0" }[e.toLowerCase()] ?? all;
});
function node(type, name, data) {
  const n = { nodeType: type, nodeName: name, childNodes: [], parentNode: null, data, attrs: {} };
  n.getAttribute = (a) => (Object.prototype.hasOwnProperty.call(n.attrs, a.toLowerCase()) ? n.attrs[a.toLowerCase()] : null);
  return n;
}
function append(parent, child) { child.parentNode = parent; parent.childNodes.push(child); }
export function parseHTML(html) {
  const root = node(11, "#document-fragment");
  const stack = [root];
  const top = () => stack[stack.length - 1];
  const close = (name) => {
    for (let k = stack.length - 1; k > 0; k--) if (stack[k].nodeName.toLowerCase() === name) { stack.length = k; return; }
  };
  const re = /<!--([\s\S]*?)-->|<!([^>]*)>|<\/([A-Za-z][\w:-]*)\s*>|<([A-Za-z][\w:-]*)((?:\s+[^\s"'>/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>`]+))?)*)\s*(\/?)>|([^<]+)|(<)/g;
  let m;
  while ((m = re.exec(html))) {
    if (m[1] !== undefined) append(top(), node(8, "#comment", m[1]));
    else if (m[2] !== undefined) append(top(), node(8, "#comment", m[2]));
    else if (m[3] !== undefined) close(m[3].toLowerCase());
    else if (m[4] !== undefined) {
      const name = m[4].toLowerCase();
      const cur = top().nodeName.toLowerCase();
      if (cur === "p" && AUTOCLOSE_P.has(name)) stack.pop();
      if (name === "li" && cur === "li") stack.pop();
      if ((name === "td" || name === "th") && (cur === "td" || cur === "th")) stack.pop();
      const el = node(1, name.toUpperCase());
      const ar = /([^\s"'>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+)))?/g;
      let a;
      while ((a = ar.exec(m[5]))) el.attrs[a[1].toLowerCase()] = decode(a[2] ?? a[3] ?? a[4] ?? "");
      append(top(), el);
      if (name === "script" || name === "style") {
        const end = html.toLowerCase().indexOf(`</${name}`, re.lastIndex);
        append(el, node(3, "#text", html.slice(re.lastIndex, end < 0 ? html.length : end)));
        re.lastIndex = end < 0 ? html.length : end;
      } else if (!VOID.has(name) && !m[6]) stack.push(el);
    } else append(top(), node(3, "#text", decode(m[7] ?? m[8])));
  }
  return root;
}
