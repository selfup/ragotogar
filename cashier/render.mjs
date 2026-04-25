// cashier/render.mjs — one renderer per section type.
// Input:  { type, props, body }   body is markdown
// Output: HTML string
import { parseBlocks, inline, esc } from './parse.mjs';
import { execFileSync } from 'child_process';
import { readFileSync, unlinkSync } from 'fs';
import { tmpdir } from 'os';
import { join } from 'path';

function embedImage(src, width = 2048) {
  const path = src.replace(/^file:\/\//, '');
  const tmp = join(tmpdir(), `cashier_${process.pid}_${Date.now()}.jpg`);
  try {
    execFileSync('magick', [path, '-resize', `${width}x>`, '-quality', '85', tmp]);
    return `data:image/jpeg;base64,${readFileSync(tmp).toString('base64')}`;
  } finally {
    try { unlinkSync(tmp); } catch {}
  }
}

const RUST_KEYWORDS = new Set([
  'as','async','await','break','const','continue','crate','dyn','else','enum','extern',
  'false','fn','for','if','impl','in','let','loop','match','mod','move','mut','pub',
  'ref','return','self','Self','static','struct','super','trait','true','type','unsafe',
  'use','where','while','box'
]);
const RUST_DEF_KEYWORDS = new Set(['impl','enum','struct','trait','fn','type','mod','union']);

function highlightRust(src) {
  let out = '';
  let i = 0;
  const n = src.length;
  let prevSig = ''; // last significant (non-whitespace) token text

  while (i < n) {
    const c = src[i];
    // whitespace — preserve but don't disturb prevSig
    if (/\s/.test(c)) {
      let j = i; while (j < n && /\s/.test(src[j])) j++;
      out += src.slice(i, j); i = j; continue;
    }
    // line comment
    if (c === '/' && src[i + 1] === '/') {
      let j = src.indexOf('\n', i); if (j === -1) j = n;
      out += `<span class="tok-com">${esc(src.slice(i, j))}</span>`;
      i = j; prevSig = ''; continue;
    }
    // block comment
    if (c === '/' && src[i + 1] === '*') {
      let j = src.indexOf('*/', i + 2); j = j === -1 ? n : j + 2;
      out += `<span class="tok-com">${esc(src.slice(i, j))}</span>`;
      i = j; prevSig = ''; continue;
    }
    // string
    if (c === '"') {
      let j = i + 1;
      while (j < n && src[j] !== '"') { if (src[j] === '\\' && j + 1 < n) j += 2; else j++; }
      j = Math.min(j + 1, n);
      out += `<span class="tok-str">${esc(src.slice(i, j))}</span>`;
      i = j; prevSig = 'str'; continue;
    }
    // attribute #[...]
    if (c === '#' && src[i + 1] === '[') {
      let j = i + 2, depth = 1;
      while (j < n && depth > 0) { if (src[j] === '[') depth++; else if (src[j] === ']') depth--; j++; }
      out += `<span class="tok-attr">${esc(src.slice(i, j))}</span>`;
      i = j; prevSig = 'attr'; continue;
    }
    // number
    if (/[0-9]/.test(c)) {
      let j = i;
      while (j < n && /[0-9a-fA-FxX_.]/.test(src[j])) j++;
      out += `<span class="tok-num">${esc(src.slice(i, j))}</span>`;
      i = j; prevSig = 'num'; continue;
    }
    // path separator `::`
    if (c === ':' && src[i + 1] === ':') {
      out += '::'; i += 2; prevSig = '::'; continue;
    }
    // identifier
    if (/[A-Za-z_]/.test(c)) {
      let j = i;
      while (j < n && /[A-Za-z0-9_]/.test(src[j])) j++;
      const word = src.slice(i, j);
      const followedByCall = src[j] === '(' || (src[j] === '!' && src[j + 1] === '(');
      if (RUST_KEYWORDS.has(word)) {
        out += `<span class="tok-key">${esc(word)}</span>`;
      } else if (prevSig === '::' || RUST_DEF_KEYWORDS.has(prevSig) || followedByCall) {
        out += `<span class="tok-fn">${esc(word)}</span>`;
      } else {
        out += esc(word);
      }
      i = j; prevSig = word; continue;
    }
    // any other single char
    out += esc(c); i++; prevSig = c;
  }
  return out;
}

function proseRender(cls, defaultScreen, defaultNum, defaultLabel, props, body) {
  const { heading, rest } = splitHeading(body);
  const content = parseBlocks(rest).map(b => {
    if (b.type === 'p') return `<p class="body">${inline(b.text)}</p>`;
    if (b.type === 'code') return renderCode(b);
    if (b.type === 'ul') return `<ul class="body-list">${b.items.map(i => `<li>${inline(i)}</li>`).join('')}</ul>`;
    if (b.type === 'ol') return `<ol class="body-list">${b.items.map(i => `<li>${inline(i)}</li>`).join('')}</ol>`;
    if (b.type === 'h') return `<h${b.level} class="body-h">${inline(b.text)}</h${b.level}>`;
    return '';
  }).join('\n  ');
  return `<section class="${cls}" data-screen-label="${props.label || defaultScreen}">
  ${sectionMarker(props.num || defaultNum, props.label || defaultLabel)}
  <h2 class="section-h2">${inline(heading || '')}</h2>
  ${content}
</section>`;
}

function renderCode(b) {
  const lang = (b.lang || '').trim();
  const file = (b.file || '').trim();
  const head = (lang || file)
    ? `<div class="code-head">${lang ? `<div class="code-lang">${esc(lang)}</div>` : ''}${file ? `<div class="code-file">${esc(file)}</div>` : ''}</div>`
    : '';
  const body = lang.toLowerCase() === 'rust' ? highlightRust(b.text) : esc(b.text);
  return `<div class="code-block">${head}<pre><code>${body}</code></pre></div>`;
}

// Render body as a plain markdown block — paragraphs, lists, etc.
function renderBody(md, { pClass = '' } = {}) {
  const blocks = parseBlocks(md);
  return blocks.map(b => {
    if (b.type === 'p')  return `<p${pClass ? ` class="${pClass}"` : ''}>${inline(b.text)}</p>`;
    if (b.type === 'h')  return `<h${b.level}>${inline(b.text)}</h${b.level}>`;
    if (b.type === 'ul') return `<ul>${b.items.map(i => `<li>${inline(i)}</li>`).join('')}</ul>`;
    if (b.type === 'ol') return `<ol>${b.items.map(i => `<li>${inline(i)}</li>`).join('')}</ol>`;
    if (b.type === 'quote') return `<blockquote>${inline(b.text)}</blockquote>`;
    if (b.type === 'code') return renderCode(b);
    return '';
  }).join('\n');
}

// Pull the first heading out of a body, return { heading, rest }
function splitHeading(body) {
  const lines = body.split('\n');
  for (let i = 0; i < lines.length; i++) {
    const h = lines[i].match(/^(#{1,6})\s+(.*)$/);
    if (h) {
      return { heading: h[2], level: h[1].length, rest: [...lines.slice(0, i), ...lines.slice(i + 1)].join('\n').trim() };
    }
    if (lines[i].trim()) break; // non-heading content first — no split
  }
  return { heading: null, rest: body };
}

// Split a body by horizontal rule `---` into chunks
function splitByRule(body) {
  return body.split(/^\s*---\s*$/m).map(s => s.trim()).filter(Boolean);
}

const sectionMarker = (num, label) =>
  `<div class="section-marker"><span class="numeral">${num}</span><span class="rule"></span><span class="label">${inline(label)}</span></div>`;

// ── renderers ──
const R = {
  hero({ props, body }) {
    const chunks = splitByRule(body);
    const [titleBlock = '', sub = '', tag = ''] = chunks;
    // In hero, H1 may come after an overline paragraph — scan anywhere for it.
    const lines = titleBlock.split('\n');
    let hIdx = lines.findIndex(l => /^#\s+/.test(l));
    let heading = null, overline = '';
    if (hIdx >= 0) {
      heading = lines[hIdx].replace(/^#\s+/, '');
      overline = [...lines.slice(0, hIdx), ...lines.slice(hIdx + 1)].join('\n').trim();
    } else {
      overline = titleBlock.trim();
    }
    // Hero title: split on `/` BEFORE inline, so the slash-to-<br> pass never
    // touches slashes that appear inside generated HTML tags (e.g. </span>).
    const title = (heading || '').split(/\s*\/\s*/).map(part => {
      const h = inline(part);
      return h.replace(/<em>/g, '<span class="italic">').replace(/<\/em>/g, '</span>');
    }).join('<br>');
    const img = props.image
      ? `<figure style="margin:0;padding:2rem;background:#fff;text-align:center;"><img src="${embedImage(props.image)}" style="max-width:100%;max-height:75vh;width:auto;height:auto;display:inline-block;" alt=""></figure>\n`
      : '';
    return `${img}<section class="hero" data-screen-label="00 Hero">
  <div class="hero-header">
    <div class="mark">${inline(props.masthead || '')}</div>
    <div class="meta">${inline(props.meta || '')}</div>
  </div>
  <div class="hero-center">
    ${overline ? `<div class="hero-overline">${inline(overline)}</div>` : ''}
    <h1 class="hero-title">${title}</h1>
    ${sub ? `<p class="hero-sub">${inline(sub)}</p>` : ''}
  </div>
  <div class="hero-footer">
    <div class="tagline">${inline(tag)}</div>
    <div class="centered">§</div>
    <div class="scroll">Continue</div>
  </div>
</section>`;
  },

  'false-choice'({ props, body }) {
    const [intro = '', left = '', right = '', conclusion = ''] = splitByRule(body);
    const { heading: h } = splitHeading(intro);
    const parseSide = (s) => {
      const a = splitHeading(s);
      const b = splitHeading(a.rest);
      const blocks = parseBlocks(b.rest);
      const content = blocks.map(blk => {
        if (blk.type === 'p') return `<p>${inline(blk.text)}</p>`;
        if (blk.type === 'code') return renderCode(blk);
        return '';
      }).join('\n');
      return { label: a.heading, sub: b.heading, content };
    };
    const L = parseSide(left), Rr = parseSide(right);
    return `<section class="false-choice" data-screen-label="${props.label || '01 Premise'}">
  ${sectionMarker(props.num || 'I.', props.label || 'The Premise')}
  <h2 class="section-h2" style="max-width:15ch;margin-bottom:4rem;">${inline(h || '')}</h2>
  <div class="two-column">
    <div class="column"><div class="column-header">${inline(L.label || '')}</div><h3>${inline(L.sub || '')}</h3>${L.content}</div>
    <div class="vs-divider"><div class="line"></div><div class="mark">versus</div><div class="line"></div></div>
    <div class="column"><div class="column-header">${inline(Rr.label || '')}</div><h3>${inline(Rr.sub || '')}</h3>${Rr.content}</div>
  </div>
  ${conclusion ? `<p class="conclusion">${inline(conclusion)}</p>` : ''}
</section>`;
  },

  landscape({ props, body }) {
    const [main = '', statBlock = ''] = splitByRule(body);
    const { heading, rest } = splitHeading(main);
    // rest: lead paragraph + optional chip list
    const blocks = parseBlocks(rest);
    const lead = blocks.find(b => b.type === 'p')?.text || '';
    const chips = blocks.find(b => b.type === 'ul')?.items || [];
    const sm = splitHeading(statBlock);
    return `<section class="landscape" data-screen-label="${props.label || '02 Landscape'}">
  ${sectionMarker(props.num || 'II.', props.label || 'Landscape')}
  <div class="body-grid">
    <div>
      <h2 class="section-h2">${inline(heading || '')}</h2>
      <p class="lead">${inline(lead)}</p>
      ${chips.length ? `<div class="state-list">${chips.map(c => `<span class="state-chip">${inline(c)}</span>`).join('')}</div>` : ''}
    </div>
    <div class="big-stat">
      <div class="number">${inline(sm.heading || props.stat || '')}</div>
      <p class="caption">${inline(sm.rest.split('\n').find(l => l.trim()) || '')}</p>
      ${props.source ? `<div class="source">Source: ${inline(props.source)}</div>` : ''}
    </div>
  </div>
</section>`;
  },

  built({ props, body }) {
    const { heading, rest } = splitHeading(body);
    const [listMd = '', kicker = ''] = splitByRule(rest);
    const items = (parseBlocks(listMd).find(b => b.type === 'ol' || b.type === 'ul')?.items) || [];
    const romans = ['i.','ii.','iii.','iv.','v.','vi.','vii.','viii.','ix.','x.','xi.','xii.'];
    return `<section class="built" data-screen-label="${props.label || '03 Requirements'}">
  ${sectionMarker(props.num || 'III.', props.label || 'Requirements')}
  <h2 class="section-h2">${inline(heading || '')}</h2>
  <div class="requirement-list">
    ${items.map((t, idx) => `<div class="requirement"><div class="num">${romans[idx] || (idx+1)+'.'}</div><div class="text">${inline(t)}</div></div>`).join('\n    ')}
  </div>
  ${kicker ? `<p class="kicker">${inline(kicker)}</p>` : ''}
</section>`;
  },

  'photo-meta'({ props, body }) {
    const { heading, rest } = splitHeading(body);
    const [listMd = '', kicker = ''] = splitByRule(rest);
    const items = (parseBlocks(listMd).find(b => b.type === 'ol' || b.type === 'ul')?.items) || [];
    return `<section class="built photo-meta" data-screen-label="${props.label || 'Metadata'}">
  ${sectionMarker(props.num || 'III.', props.label || 'Metadata')}
  <h2 class="section-h2">${inline(heading || '')}</h2>
  <div class="requirement-list">
    ${items.map(t => {
      const sep = t.indexOf(': ');
      const key = sep >= 0 ? t.slice(0, sep) : t;
      const val = sep >= 0 ? t.slice(sep + 2) : '';
      return `<div class="requirement"><div class="num">${esc(key)}</div><div class="text">${inline(val)}</div></div>`;
    }).join('\n    ')}
  </div>
  ${kicker ? `<p class="kicker">${inline(kicker)}</p>` : ''}
</section>`;
  },

  analogy({ props, body }) {
    const [main = '', scene = ''] = splitByRule(body);
    const { heading, rest } = splitHeading(main);
    const iconItems = (parseBlocks(rest).find(b => b.type === 'ul')?.items) || [];
    const icons = [
      `<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="1.3"><rect x="12" y="8" width="24" height="32" rx="2"/></svg>`,
      `<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="1.3"><circle cx="24" cy="24" r="14"/></svg>`,
      `<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="1.3"><path d="M8 12 L40 12 L40 36 L8 36 Z"/></svg>`
    ];
    const sceneBlocks = parseBlocks(scene).map(b => {
      if (b.type === 'p') return `<p class="scene">${inline(b.text)}</p>`;
      if (b.type === 'code') return renderCode(b);
      return '';
    }).filter(Boolean).join('\n    ');
    return `<section class="analogy" data-screen-label="${props.label || '04 Analogy'}">
  ${sectionMarker(props.num || 'IV.', props.label || 'Analogy')}
  <div class="body-grid">
    <div>
      <h2 class="section-h2">${inline(heading || '')}</h2>
      <div class="icon-row">
        ${iconItems.slice(0, 3).map((item, idx) => {
          const [label, sub] = item.split(/\s*—\s*/);
          return `<div class="icon-item"><div class="icon">${icons[idx]}</div><div class="label">${inline(label || '')}</div><div class="sub">${inline(sub || '')}</div></div>`;
        }).join('\n        ')}
      </div>
    </div>
    <div>${sceneBlocks}</div>
  </div>
</section>`;
  },

  how({ props, body }) {
    const { heading, rest } = splitHeading(body);
    const steps = splitByRule(rest);
    const romans = ['i','ii','iii','iv','v','vi','vii','viii'];
    return `<section class="how" data-screen-label="${props.label || '06 Journey'}">
  ${sectionMarker(props.num || 'VI.', props.label || 'Journey')}
  <h2 class="section-h2" style="max-width:18ch;margin-bottom:4rem;">${inline(heading || '')}</h2>
  <div class="steps">
    ${steps.map((s, idx) => {
      // step chunk: label paragraph, then # heading, then description paragraph
      const blocks = parseBlocks(s);
      const firstP = blocks.find(b => b.type === 'p');
      const label = firstP?.text || '';
      const headingBlock = blocks.find(b => b.type === 'h');
      const stepH = headingBlock?.text || '';
      const desc = blocks.filter(b => b.type === 'p' && b !== firstP).map(b => b.text).join(' ');
      return `<div class="step"><div class="num">${romans[idx] || idx+1}</div><div class="label">${inline(label)}</div><h3>${inline(stepH)}</h3><p>${inline(desc)}</p></div>`;
    }).join('\n    ')}
  </div>
</section>`;
  },

  'dual-pillars'({ props, body }) {
    const [intro = '', left = '', right = ''] = splitByRule(body);
    const { heading } = splitHeading(intro);
    const renderGroup = (md) => {
      const { heading: h, rest } = splitHeading(md);
      const items = (parseBlocks(rest).find(b => b.type === 'ul')?.items) || [];
      return { label: h, items };
    };
    const L = renderGroup(left), Rr = renderGroup(right);
    const pillar = (g) => `<div class="pillar-group"><h3>${inline(g.label || '')}</h3>${g.items.map(i => {
      const [head, ...body] = i.split(/\s*—\s*/);
      return `<div class="pillar"><div class="head">${inline(head)}</div><div class="body">${inline(body.join(' — '))}</div></div>`;
    }).join('')}</div>`;
    return `<section class="dual-pillars" data-screen-label="${props.label || '07 Pillars'}">
  ${sectionMarker(props.num || 'VII.', props.label || 'Pillars')}
  <h2 class="section-h2" style="margin-bottom:4rem;">${inline(heading || '')}</h2>
  <div class="pillar-grid">${pillar(L)}${pillar(Rr)}</div>
</section>`;
  },

  objections({ props, body }) {
    const { heading, rest } = splitHeading(body);
    const pairs = splitByRule(rest);
    return `<section class="objections" data-screen-label="${props.label || '09 Objections'}">
  ${sectionMarker(props.num || 'IX.', props.label || 'Objections')}
  <h2 class="section-h2" style="max-width:20ch;margin-bottom:4rem;">${inline(heading || '')}</h2>
  <div class="qa-grid">
    ${pairs.map(p => {
      const blocks = parseBlocks(p);
      const q = blocks[0]?.text || '';
      const a = blocks.slice(1).map(b => b.text).join(' ');
      return `<div class="qa"><div class="q">"${inline(q)}"</div><div class="a">${inline(a)}</div></div>`;
    }).join('\n    ')}
  </div>
</section>`;
  },

  'why-not'({ props, body }) {
    return proseRender('why-not', '10 Obstacle', 'X.', 'Obstacle', props, body);
  },

  prose({ props, body }) {
    return proseRender('prose', 'Notes', 'V.', 'Notes', props, body);
  },

  action({ props, body }) {
    const { heading, rest } = splitHeading(body);
    const steps = splitByRule(rest);
    const markers = ['Step One', 'Step Two', 'Step Three', 'Step Four'];
    return `<section class="action" data-screen-label="${props.label || '11 Action'}">
  ${sectionMarker(props.num || 'XI.', props.label || 'Action')}
  <h2 class="section-h2" style="margin-bottom:4rem;">${inline(heading || '')}</h2>
  <div class="action-steps">
    ${steps.map((s, idx) => {
      const blocks = parseBlocks(s);
      const h = blocks.find(b => b.type === 'h')?.text || '';
      const desc = blocks.filter(b => b.type === 'p').map(b => b.text).join(' ');
      return `<div class="action-step"><div class="marker">${markers[idx] || 'Step ' + (idx+1)}</div><h3>${inline(h)}</h3><p>${inline(desc)}</p></div>`;
    }).join('\n    ')}
  </div>
</section>`;
  },

  proposal({ props, body }) {
    const [intro = '', cardBlock = '', ann = ''] = splitByRule(body);
    const { heading, rest } = splitHeading(intro);
    const lede = parseBlocks(rest).find(b => b.type === 'p')?.text || '';
    // card block: first line = card mark | tier ; H3 title; italic/paragraph subtitle; list = panels (label | code | status); footer line1/line2
    const cbLines = cardBlock.split('\n').map(l => l.trim());
    const headerLine = (cbLines[0] || '').split('|').map(s => s.trim());
    const cardMark = headerLine[0] || '';
    const cardTier = headerLine[1] || '';
    const cbBlocks = parseBlocks(cbLines.slice(1).join('\n'));
    const cardTitle = cbBlocks.find(b => b.type === 'h' && b.level === 3)?.text || '';
    const cardSubtitle = cbBlocks.find(b => b.type === 'p')?.text || '';
    const panelItems = cbBlocks.find(b => b.type === 'ol' || b.type === 'ul')?.items || [];
    const panels = panelItems.map((it, idx) => {
      const parts = it.split('|').map(s => s.trim());
      const [code = '████ · ████ · ████', status = 'Sealed'] = parts;
      const used = /spent|used/i.test(status);
      const hiddenClass = used ? 'used' : 'hidden';
      return `<div class="scratch-panel ${hiddenClass}"><span class="scratch-num">${String(idx+1).padStart(2,'0')}</span><span class="scratch-code">${inline(code)}</span><span class="scratch-status">${inline(status)}</span></div>`;
    }).join('\n          ');
    // footer from props
    const footerL1 = props.footer1 || '';
    const footerL2 = props.footer2 || '';
    // annotations: each item = "Label — Content"
    const annItems = (parseBlocks(ann).find(b => b.type === 'ul' || b.type === 'ol')?.items) || [];
    const annotations = annItems.map(it => {
      const [label, ...content] = it.split(/\s*—\s*/);
      return `<div class="annotation"><div class="label">${inline(label)}</div><div class="content">${inline(content.join(' — '))}</div></div>`;
    }).join('\n      ');
    return `<section class="proposal" data-screen-label="${props.label || '05 The Proposal'}">
  ${sectionMarker(props.num || 'V.', props.label || 'The Proposal')}
  <div class="title-block">
    <h2 class="section-h2" style="font-size:clamp(3rem,8vw,6.5rem);line-height:0.95;letter-spacing:-0.04em;margin-bottom:2.5rem;">${inline(heading || '')}</h2>
    <p class="lede">${inline(lede)}</p>
  </div>
  <div class="card-display">
    <div class="verification-card">
      <div class="card-header"><div class="card-mark">${inline(cardMark)}</div><div class="card-tier">${inline(cardTier)}</div></div>
      <h3 class="card-title">${inline(cardTitle)}</h3>
      <p class="card-subtitle">${inline(cardSubtitle)}</p>
      <div class="scratch-stack">
          ${panels}
      </div>
      <div class="card-footer"><div class="card-activation">${inline(footerL1)}<br>${inline(footerL2)}</div><div class="card-specimen">SPECIMEN</div></div>
    </div>
    <div class="card-annotations">
      ${annotations}
    </div>
  </div>
</section>`;
  },

  comparison({ props, body }) {
    const { heading, rest } = splitHeading(body);
    // rest contains a pipe-table
    const lines = rest.split('\n').map(l => l.trim()).filter(Boolean).filter(l => l.startsWith('|'));
    const rows = lines.filter(l => !/^\|\s*-+/.test(l)).map(l => l.replace(/^\||\|$/g, '').split('|').map(c => c.trim()));
    if (rows.length === 0) return '';
    const [header, ...dataRows] = rows;
    const oursIdx = header.length - 1;
    const renderCell = (i, val, headLabel) => {
      if (i === 0) return `<div class="feature">${inline(val)}</div>`;
      if (i === oursIdx) return `<div class="val good" data-label="${inline(headLabel)}">${inline(val)}</div>`;
      return `<div class="val bad" data-label="${inline(headLabel)}">${inline(val)}</div>`;
    };
    return `<section class="comparison" data-screen-label="${props.label || '08 Comparison'}">
  ${sectionMarker(props.num || 'VIII.', props.label || 'Against the Alternatives')}
  <h2 class="section-h2" style="margin-bottom:4rem;max-width:24ch;">${inline(heading || '')}</h2>
  <div class="comparison-table">
    <div class="comp-row header">
      ${header.map((h, i) => i === 0 ? `<div class="feature">${inline(h)}</div>` : i === oursIdx ? `<div class="ours">${inline(h)}</div>` : `<div>${inline(h)}</div>`).join('')}
    </div>
    ${dataRows.map(row => `<div class="comp-row">
      ${row.map((v, i) => renderCell(i, v, header[i])).join('')}
    </div>`).join('\n    ')}
  </div>
</section>`;
  },

  close({ props, body }) {
    const [quote = '', post = ''] = splitByRule(body);
    // in blockquote: wrap *italic* in <em>, leave plain text; then wrap last sentence (non-em) in emphasis span
    const quoteHtml = inline(quote);
    return `<section class="close" data-screen-label="${props.label || '12 Close'}">
  ${sectionMarker(props.num || 'XII.', props.label || 'In Closing')}
  <div class="close-content">
    <blockquote>${quoteHtml}</blockquote>
    ${post ? `<p class="post">${inline(post)}</p>` : ''}
  </div>
  <div class="close-footer"><div class="mark">${inline(props.mark || '')}</div><div class="meta">${inline(props.meta || '')}</div></div>
</section>`;
  },
};

export function renderSection(sec) {
  const fn = R[sec.type];
  if (fn) return fn(sec);
  // unknown → plain prose section
  return `<section class="${sec.type}">\n${renderBody(sec.body)}\n</section>`;
}

export const renderers = R;
