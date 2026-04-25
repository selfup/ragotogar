// cashier/parse.mjs — tiny markdown (no deps)
// Handles: headings, paragraphs, unordered + ordered lists, blockquote,
// inline **bold**, *italic*, `code`, [text](url), and line breaks.

export const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

export function inline(src) {
  let s = esc(src);
  // code
  s = s.replace(/`([^`]+)`/g, '<code class="code-inline">$1</code>');
  // bold **x**
  s = s.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  // italic *x*  (avoid ** already consumed)
  s = s.replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>');
  // links
  s = s.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2">$1</a>');
  // hard break: trailing \\
  s = s.replace(/\\\n/g, '<br>');
  return s;
}

// Parse a markdown chunk into a list of { type, ... } blocks.
export function parseBlocks(md) {
  const lines = md.replace(/\r\n/g, '\n').split('\n');
  const blocks = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) { i++; continue; }

    // fenced code block ```lang [filename] ... ```
    if (/^```/.test(line)) {
      const info = line.slice(3).trim();
      const spaceIdx = info.indexOf(' ');
      const lang = spaceIdx === -1 ? info : info.slice(0, spaceIdx);
      const file = spaceIdx === -1 ? '' : info.slice(spaceIdx + 1).trim();
      const buf = [];
      i++;
      while (i < lines.length && !/^```/.test(lines[i])) { buf.push(lines[i]); i++; }
      i++; // skip closing fence
      blocks.push({ type: 'code', lang, file, text: buf.join('\n') });
      continue;
    }

    // heading
    const h = line.match(/^(#{1,6})\s+(.*)$/);
    if (h) { blocks.push({ type: 'h', level: h[1].length, text: h[2] }); i++; continue; }

    // blockquote
    if (line.startsWith('>')) {
      const buf = [];
      while (i < lines.length && lines[i].startsWith('>')) {
        buf.push(lines[i].replace(/^>\s?/, '')); i++;
      }
      blocks.push({ type: 'quote', text: buf.join(' ') });
      continue;
    }

    // unordered list
    if (/^[-*]\s/.test(line)) {
      const items = [];
      while (i < lines.length && /^[-*]\s/.test(lines[i])) {
        items.push(lines[i].replace(/^[-*]\s+/, '')); i++;
      }
      blocks.push({ type: 'ul', items });
      continue;
    }

    // ordered list
    if (/^\d+\.\s/.test(line)) {
      const items = [];
      while (i < lines.length && /^\d+\.\s/.test(lines[i])) {
        items.push(lines[i].replace(/^\d+\.\s+/, '')); i++;
      }
      blocks.push({ type: 'ol', items });
      continue;
    }

    // paragraph (until blank line)
    const buf = [];
    while (i < lines.length && lines[i].trim() && !/^(```|#{1,6}\s|>|[-*]\s|\d+\.\s)/.test(lines[i])) {
      buf.push(lines[i]); i++;
    }
    blocks.push({ type: 'p', text: buf.join(' ') });
  }
  return blocks;
}

// Split a full document into a sequence of fenced section chunks.
//   :::hero { "label": "00 Hero" }
//   ...markdown...
//   :::
// Anything before the first fence becomes a doc-level header (title + meta).
export function parseSections(md) {
  const out = [];
  const re = /^:::([\w-]+)(?:\s+(\{[^\n]*\}))?\s*\n([\s\S]*?)\n:::\s*(?=\n|$)/gm;
  let lastIndex = 0;
  let pre = '';
  let m;
  while ((m = re.exec(md)) !== null) {
    if (m.index > lastIndex) pre += md.slice(lastIndex, m.index);
    let props = {};
    if (m[2]) { try { props = JSON.parse(m[2]); } catch (e) {} }
    out.push({ type: m[1], props, body: m[3] });
    lastIndex = re.lastIndex;
  }
  if (lastIndex < md.length) pre += md.slice(lastIndex);
  return { pre: pre.trim(), sections: out };
}
