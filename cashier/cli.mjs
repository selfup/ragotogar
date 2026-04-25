#!/usr/bin/env node
// cashier/cli.mjs — cashier build post.md > post.html
import { readFileSync, existsSync } from 'node:fs';
import { dirname, resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseSections, parseBlocks, inline } from './parse.mjs';
import { renderSection } from './render.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT = resolve(__dirname, '..');

const args = process.argv.slice(2);
const cmd = args[0];

if (!cmd || cmd === 'help' || cmd === '--help' || cmd === '-h') {
  console.error(`cashier — long-form essay builder

usage:
  cashier build <file.md> [> out.html]   build one markdown file to stdout
  cashier build-all [indir] [outdir]     build every .md in indir → outdir/.html
                                         (defaults: posts → posts)
  cashier new <slug>                     scaffold a new empty post at posts/<slug>.md

  cashier convert <pre_posts/x.md> [> posts/x.md]
                                         send a rough draft + AUTHORING.md to your
                                         LLM and write the structured .md
  cashier convert-all [indir] [outdir]   convert every pre_posts/*.md → posts/*.md
                                         (defaults: pre_posts → posts)

  Options for convert / convert-all:
    --model=<name>     model id (default: $LLM_MODEL or 'claude-sonnet-4-5')

  Required env for convert / convert-all:
    LLM_URL     chat-completions endpoint
                (e.g. https://api.anthropic.com/v1/chat/completions,
                      https://api.openai.com/v1/chat/completions,
                      http://localhost:11434/v1/chat/completions)
    LLM_AUTH    value of the Authorization header
                (e.g. "Bearer sk-...")
`);
  process.exit(cmd ? 0 : 1);
}

// parse flags
const flags = {};
for (const a of args.slice(1)) {
  const m = a.match(/^--([^=]+)=(.*)$/);
  if (m) flags[m[1]] = m[2];
}
const positional = args.slice(1).filter(a => !a.startsWith('--'));

// ── `new` ──────────────────────────────────────────────────────────────────
if (cmd === 'new') {
  const slug = args[1];
  if (!slug) { console.error('usage: cashier new <slug>'); process.exit(1); }
  const { writeFileSync, mkdirSync } = await import('node:fs');
  const target = resolve(process.cwd(), 'posts', `${slug}.md`);
  mkdirSync(dirname(target), { recursive: true });
  const tpl = `---
title: ${slug.replace(/-/g, ' ').replace(/\b\w/g, c => c.toUpperCase())}
---

:::hero { "masthead": "...", "meta": "Draft" }
Overline

# Title / Word / *Italic.*

---

Subtitle.

---

Opening paragraph.
:::
`;
  writeFileSync(target, tpl);
  console.error('wrote', target);
  process.exit(0);
}

// ── `build-all` ────────────────────────────────────────────────────────────
if (cmd === 'build-all') {
  const { readdirSync, writeFileSync, mkdirSync } = await import('node:fs');
  const inDir = resolve(process.cwd(), args[1] || 'posts');
  const outDir = resolve(process.cwd(), args[2] || 'posts');
  if (!existsSync(inDir)) { console.error('not found:', inDir); process.exit(1); }
  mkdirSync(outDir, { recursive: true });
  const files = readdirSync(inDir).filter(f => f.endsWith('.md'));
  if (files.length === 0) { console.error('no .md files in', inDir); process.exit(0); }
  for (const f of files) {
    const srcPath = join(inDir, f);
    const outPath = join(outDir, f.replace(/\.md$/, '.html'));
    writeFileSync(outPath, buildHtml(readFileSync(srcPath, 'utf8')));
    console.error('built', outPath);
  }
  process.exit(0);
}

// ── `convert` / `convert-all` ──────────────────────────────────────────────
if (cmd === 'convert' || cmd === 'convert-all') {
  const { readdirSync, writeFileSync, mkdirSync } = await import('node:fs');
  const LLM_URL = process.env.LLM_URL;
  const LLM_AUTH = process.env.LLM_AUTH;
  const model = flags.model || process.env.LLM_MODEL || 'claude-sonnet-4-5';
  if (!LLM_URL || !LLM_AUTH) {
    console.error('convert requires LLM_URL and LLM_AUTH env vars. see `cashier help`.');
    process.exit(1);
  }
  const authoringPath = join(ROOT, 'AUTHORING.md');
  const authoring = existsSync(authoringPath) ? readFileSync(authoringPath, 'utf8') : '';
  if (!authoring) { console.error('AUTHORING.md not found at project root'); process.exit(1); }

  const system = `You are converting a rough draft into a Cashier Standard essay.
Follow the spec below exactly. Use only section types it defines. Preserve the author's voice.
Do not pad with filler. Output ONLY the structured markdown (frontmatter + :::sections), no prose wrapper, no fences around the whole output.

${authoring}`;

  async function convertOne(srcPath, outPath) {
    const draft = readFileSync(srcPath, 'utf8');
    const body = {
      model,
      max_tokens: 8192,
      messages: [
        { role: 'system', content: system },
        { role: 'user', content: 'Convert this rough draft:\n\n' + draft }
      ],
    };
    const res = await fetch(LLM_URL, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Authorization': LLM_AUTH },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      console.error(`LLM error ${res.status}: ${await res.text()}`);
      process.exit(1);
    }
    const data = await res.json();
    // Support OpenAI-compatible { choices:[{message:{content}}] }
    // and Anthropic native { content:[{text}] }
    let text = data?.choices?.[0]?.message?.content
      ?? (Array.isArray(data?.content) ? data.content.map(c => c.text || '').join('') : '')
      ?? '';
    if (!text) { console.error('no content returned:', JSON.stringify(data).slice(0, 400)); process.exit(1); }
    // strip accidental code fence wrappers
    text = text.replace(/^```(?:markdown|md)?\n/, '').replace(/\n```\s*$/, '').trim() + '\n';
    if (outPath) {
      mkdirSync(dirname(outPath), { recursive: true });
      writeFileSync(outPath, text);
      console.error('wrote', outPath);
    } else {
      process.stdout.write(text);
    }
  }

  if (cmd === 'convert') {
    if (!positional[0]) { console.error('usage: cashier convert <file.md>'); process.exit(1); }
    const src = resolve(process.cwd(), positional[0]);
    if (!existsSync(src)) { console.error('not found:', src); process.exit(1); }
    await convertOne(src, null);
    process.exit(0);
  }

  // convert-all
  const inDir = resolve(process.cwd(), positional[0] || 'pre_posts');
  const outDir = resolve(process.cwd(), positional[1] || 'posts');
  if (!existsSync(inDir)) { console.error('not found:', inDir); process.exit(1); }
  mkdirSync(outDir, { recursive: true });
  const files = readdirSync(inDir).filter(f => f.endsWith('.md'));
  if (files.length === 0) { console.error('no .md files in', inDir); process.exit(0); }
  for (const f of files) {
    await convertOne(join(inDir, f), join(outDir, f));
  }
  console.error(`converted ${files.length} file(s). run \`cashier build-all\` to render.`);
  process.exit(0);
}

// ── `build` ────────────────────────────────────────────────────────────────
if (cmd !== 'build' || !args[1]) {
  console.error('usage: cashier build <file.md> [> out.html]');
  process.exit(1);
}

const mdPath = resolve(process.cwd(), args[1]);
if (!existsSync(mdPath)) { console.error('not found:', mdPath); process.exit(1); }
process.stdout.write(buildHtml(readFileSync(mdPath, 'utf8')));

// ── shared ─────────────────────────────────────────────────────────────────
function buildHtml(md) {
  const stylesPath = join(ROOT, 'styles.css');
  const styles = existsSync(stylesPath) ? readFileSync(stylesPath, 'utf8') : '';

  let doc = md, meta = {};
  const fm = md.match(/^---\n([\s\S]*?)\n---\n/);
  if (fm) {
    doc = md.slice(fm[0].length);
    fm[1].split('\n').forEach(l => {
      const m = l.match(/^(\w+):\s*(.*)$/);
      if (m) meta[m[1]] = m[2].replace(/^["']|["']$/g, '');
    });
  }

  const { sections } = parseSections(doc);
  const body = sections.map(renderSection).join('\n\n');
  const title = meta.title || 'Untitled';

  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>${title}</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Fraunces:ital,opsz,wght,SOFT@0,9..144,200..900,0..100;1,9..144,200..900,0..100&family=Newsreader:ital,opsz,wght@0,6..72,300..700;1,6..72,300..700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<style>
${styles}
</style>
</head>
<body>
<main>
${body}
</main>
<script>
const io = new IntersectionObserver((entries) => {
  entries.forEach(e => { if (e.isIntersecting) { e.target.classList.add('visible'); io.unobserve(e.target); } });
}, { threshold: 0.08, rootMargin: '0px 0px -50px 0px' });
document.querySelectorAll('.reveal').forEach(el => io.observe(el));
</script>
</body>
</html>`;
}
