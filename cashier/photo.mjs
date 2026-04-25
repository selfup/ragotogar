#!/usr/bin/env node
// cashier/photo.mjs — generate a Cashier Standard .md from a photo JSON file
// usage: node cashier/photo.mjs <input.json> [output.md]
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, resolve } from 'node:path';

const [,, inputPath, outputPath] = process.argv;
if (!inputPath) {
  console.error('usage: node cashier/photo.mjs <input.json> [output.md]');
  process.exit(1);
}

const data = JSON.parse(readFileSync(resolve(process.cwd(), inputPath), 'utf8'));
const { name, file, path: filePath, preview, preview_ms, inference, inference_ms, metadata: m, fields } = data;

const MONTHS = ['January','February','March','April','May','June','July','August','September','October','November','December'];

function formatDate(dt) {
  const [y, mo, d] = dt.split(' ')[0].split(':');
  return `${parseInt(d)} ${MONTHS[parseInt(mo) - 1]} ${y}`;
}

function stripBullets(text) {
  return (text || '').split('\n').map(l => l.replace(/^[-*]\s+/, '').trim()).filter(Boolean).join(' ');
}

function firstSentence(text) {
  const s = stripBullets(text);
  return (s.match(/^[^.!?]+[.!?]/) || [s])[0].trim();
}

const date     = formatDate(m.date_time_original);
const time     = m.date_time_original.split(' ')[1];
const camera   = `${m.make} ${m.model}`;
const fileStem = file.replace(/\.[^.]+$/, '');
const year     = m.date_time_original.slice(0, 4);

const allMeta = [
  ['file_name',            m.file_name],
  ['name',                 name],
  ['path',                 filePath],
  ['make',                 m.make],
  ['model',                m.model],
  ['date_time_original',   m.date_time_original],
  ['focal_length',         m.focal_length],
  ['f_number',             `f/${m.f_number}`],
  ['exposure_time',        `${m.exposure_time}s`],
  ['iso',                  m.iso],
  ['exposure_compensation',m.exposure_compensation],
  ['white_balance',        m.white_balance],
  ['metering_mode',        m.metering_mode],
  ['exposure_mode',        m.exposure_mode],
  ['flash',                m.flash],
  ['image_width',          m.image_width],
  ['image_height',         m.image_height],
  ['artist',               m.artist],
  ['copyright',            m.copyright],
  ['software',             m.software],
  ['preview',              preview],
  ['preview_ms',           preview_ms],
  ['inference',            inference],
  ['inference_ms',         inference_ms],
].filter(([, v]) => v !== '' && v !== null && v !== undefined);

const metaList = allMeta.map(([k, v], i) => `${i + 1}. ${k}: ${v}`).join('\n');

const subject     = stripBullets(fields.subject);
const setting     = stripBullets(fields.setting);
const light       = stripBullets(fields.light);
const colors      = stripBullets(fields.colors);
const composition = stripBullets(fields.composition);

const md = `---
title: ${fileStem} — ${camera}
---

:::hero { "masthead": "Photograph Analysis", "meta": "${date}", "image": "file://${filePath}" }
${camera} · DxO-processed still · ${m.artist}

# ${fileStem} / *${camera}.*

---

Captured on ${date} at ${time}, processed through ${m.software}. Preview generated in ${preview}; inference completed in ${inference}.

---

Shot on ${camera} — ${m.focal_length}, f/${m.f_number}, ${m.exposure_time}s, ISO ${m.iso}, ${m.exposure_mode} exposure with ${m.metering_mode.toLowerCase()} metering, ${m.white_balance} white balance, ${m.flash.toLowerCase()}. Image dimensions: ${m.image_width} × ${m.image_height}.
:::

:::dual-pillars { "num": "II.", "label": "Visual Analysis" }
# Five fields. *Subject. Setting. Light. Colors. Composition.*

---

# Subject & Setting
- Subject. — ${subject}
- Setting. — ${setting}

---

# Light, Colors & Composition
- Light. — ${light}
- Colors. — ${colors}
- Composition. — ${composition}
:::

:::photo-meta { "num": "III.", "label": "Camera Settings" }
# All metadata for *this frame:*

${metaList}

---

*All settings recorded at the moment of capture — ${time}, ${date}.*
:::

:::close { "num": "IV.", "label": "In Closing", "mark": "${m.artist}", "meta": "${fileStem} · ${year}" }
*${firstSentence(fields.subject)}* **${firstSentence(fields.setting)}**

---

File: ${file} · Original: ${m.date_time_original} · Processed in ${m.software} · Preview: ${preview} · Inference: ${inference}
:::
`;

if (outputPath) {
  const out = resolve(process.cwd(), outputPath);
  mkdirSync(dirname(out), { recursive: true });
  writeFileSync(out, md);
  console.error('wrote', out);
} else {
  process.stdout.write(md);
}
