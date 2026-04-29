package main

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{if .Q}}{{.Q}} — {{end}}ragotogar</title>
  <style>
    :root { color-scheme: dark; --bg:#0a0a0a; --fg:#e8e8e8; --mute:#888; --line:#222; --accent:#444; }
    * { box-sizing: border-box; }
    html, body { margin: 0; }
    body {
      font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Helvetica, Arial, sans-serif;
      background: var(--bg); color: var(--fg);
      max-width: 1400px; margin: 0 auto; padding: 2rem 1.5rem;
    }
    header { margin-bottom: 1.5rem; }
    h1 {
      font-weight: 200; letter-spacing: -0.03em; font-size: 1.5rem;
      margin: 0 0 1rem 0; opacity: 0.6;
    }
    h1 a { color: inherit; text-decoration: none; }
    form { display: flex; flex-direction: column; gap: 0.5rem; }
    input[type="search"] {
      flex: 1; padding: 0.75rem 1rem; font-size: 1rem; font-family: inherit;
      background: #141414; border: 1px solid var(--line); color: var(--fg);
      border-radius: 6px;
    }
    input[type="search"]:focus { outline: none; border-color: var(--accent); }
    button {
      padding: 0.75rem 1.5rem; font-family: inherit; font-size: 0.9rem;
      background: #1a1a1a; border: 1px solid var(--line); color: var(--fg);
      cursor: pointer; border-radius: 6px;
    }
    button:hover { background: #222; border-color: var(--accent); }
    .modes {
      display: flex; gap: 0; margin-bottom: 0.75rem;
      border: 1px solid var(--line); border-radius: 6px; overflow: hidden;
      width: fit-content;
    }
    .modes label {
      padding: 0.5rem 1rem; cursor: pointer; font-size: 0.85rem;
      color: var(--mute); user-select: none; border-right: 1px solid var(--line);
      transition: background 0.1s ease, color 0.1s ease;
    }
    .modes label:last-child { border-right: 0; }
    .modes label:hover { color: var(--fg); background: #1a1a1a; }
    .modes input { position: absolute; opacity: 0; pointer-events: none; }
    .modes label.active,
    .modes label:has(input:checked) { background: #222; color: var(--fg); }
    .modes .desc { color: var(--mute); font-size: 0.75rem; margin-left: 0.75rem; align-self: center; }
    .sliders {
      display: flex; gap: 1.5rem; flex-wrap: wrap;
      font-size: 0.8rem; color: var(--mute);
      margin-bottom: 0.75rem;
    }
    .sliders label {
      display: inline-flex; align-items: center; gap: 0.5rem; min-width: 14rem;
    }
    .sliders input[type="range"] {
      flex: 1; accent-color: var(--fg); cursor: pointer; max-width: 12rem;
    }
    .sliders output {
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
      color: var(--fg); font-variant-numeric: tabular-nums; min-width: 2.5rem;
      text-align: right;
    }
    .status { color: var(--mute); font-size: 0.875rem; margin: 1rem 0; }
    .status .latency {
      margin-left: 0.5rem; opacity: 0.7;
      font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
      font-variant-numeric: tabular-nums;
    }
    .grid {
      display: grid; gap: 1rem;
      grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    }
    .grid a {
      display: block; aspect-ratio: 3/2; overflow: hidden; border-radius: 4px;
      background: #141414; transition: transform 0.15s ease;
    }
    .grid a:hover { transform: scale(1.02); }
    .grid img { width: 100%; height: 100%; object-fit: cover; display: block; }
    .empty {
      color: var(--mute); font-size: 0.875rem; text-align: center;
      margin: 4rem 0;
    }
  </style>
</head>
<body>
  <header>
    <h1><a href="/">ragotogar</a></h1>
    <form method="GET" action="/">
      <div class="modes" title="Retrieval mode">
        <label class="{{if eq .Mode "naive"}}active{{end}}" title="Pure vector cosine similarity. Sub-second; broad semantic recall.">
          <input type="radio" name="mode" value="naive" {{if eq .Mode "naive"}}checked{{end}} onchange="this.form.submit()">vector
        </label>
        <label class="{{if eq .Mode "naive-verify"}}active{{end}}" title="Vector retrieval, then LLM yes/no check on each candidate. Higher precision; ~1–6s.">
          <input type="radio" name="mode" value="naive-verify" {{if eq .Mode "naive-verify"}}checked{{end}} onchange="this.form.submit()">vector+verify
        </label>
        <label class="{{if eq .Mode "fts-vector"}}active{{end}}" title="Vector + Postgres full-text search, fused via Reciprocal Rank Fusion. Catches literal-text matches vector misses.">
          <input type="radio" name="mode" value="fts-vector" {{if eq .Mode "fts-vector"}}checked{{end}} onchange="this.form.submit()">FTS+vector
        </label>
        <label class="{{if eq .Mode "fts-vector-verify"}}active{{end}}" title="Vector ∪ FTS fused via RRF, then LLM yes/no per candidate. Tightest precision; slowest.">
          <input type="radio" name="mode" value="fts-vector-verify" {{if eq .Mode "fts-vector-verify"}}checked{{end}} onchange="this.form.submit()">FTS+vector+verify
        </label>
      </div>
      <div class="sliders" title="Tune the precision/recall floor for each retrieval arm. Default cosine 0.50, FTS ratio 0.30.">
        <label>
          <span>cosine ≥</span>
          <input type="range" name="cosine" min="0" max="1" step="0.05"
            value="{{.CosineThreshold}}"
            oninput="this.nextElementSibling.value = parseFloat(this.value).toFixed(2)"
            onchange="this.form.submit()">
          <output>{{.CosineThreshold}}</output>
        </label>
        <label title="Adaptive FTS floor as a fraction of the top ts_rank in the result set. 0 = no filter, 1 = only the strongest match.">
          <span>fts ≥</span>
          <input type="range" name="fts" min="0" max="1" step="0.05"
            value="{{.FTSThresholdRel}}"
            oninput="this.nextElementSibling.value = parseFloat(this.value).toFixed(2)"
            onchange="this.form.submit()">
          <output>{{.FTSThresholdRel}}</output>
          <span>× max</span>
        </label>
      </div>
      <div style="display: flex; gap: 0.5rem;">
        <input type="search" name="q" autofocus
          placeholder='try: "warm light bedroom", "shots from a car"...'
          value="{{.Q}}">
        <button type="submit">search</button>
      </div>
    </form>
  </header>

  {{if .Q}}
    <div class="status">
      {{if .Results}}{{len .Results}} match{{if ne (len .Results) 1}}es{{end}} for "{{.Q}}"
      {{else}}no matches for "{{.Q}}"
      {{end}}
      {{if .Total}}<span class="latency">(out of {{.Total}} image{{if ne .Total 1}}s{{end}})</span>{{end}}
      {{if .Latency}}<span class="latency">({{.Latency}})</span>{{end}}
      {{if not .Results}} — try different words or broader concepts{{end}}
    </div>
  {{end}}

  {{if .Results}}
    <div class="grid">
      {{range .Results}}
        <a href="/photos/{{.Name}}" title="{{.Name}}">
          <img src="/photos/{{.Name}}.jpg" alt="{{.Name}}" loading="lazy">
        </a>
      {{end}}
    </div>
  {{else if not .Q}}
    <div class="empty">Search the photo library by description.</div>
  {{end}}
</body>
</html>
`

// photoHTML mirrors the cashier render: hero / dual-pillars / built photo-meta.
// Loads the cashier design system (styles.css served by cmd/web) plus the
// same Google Fonts cashier did. The synthesized "close" section is left
// out since it built prose from the description (not a direct pull).
const photoHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{stem .Photo.FileBasename}} — {{.Exif.CameraMake}} {{.Exif.CameraModel}}</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Fraunces:ital,opsz,wght,SOFT@0,9..144,200..900,0..100;1,9..144,200..900,0..100&family=Newsreader:ital,opsz,wght@0,6..72,300..700;1,6..72,300..700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<link rel="stylesheet" href="/styles.css">
</head>
<body>

<figure style="margin:0;padding:2rem;background:#fff;text-align:center;">
  <a href="/photos/{{.Photo.Name}}.jpg">
    <img src="/photos/{{.Photo.Name}}.jpg" alt="{{.Photo.Name}}" style="max-width:100%;height:auto;">
  </a>
</figure>

<section class="hero" data-screen-label="00 Hero">
  <div class="hero-header">
    <div class="mark">Photograph Analysis</div>
    {{if .Exif.DateTaken}}<div class="meta">{{humanDateOnly .Exif.DateTaken}}</div>{{end}}
  </div>
  <div class="hero-center">
    <div class="hero-overline">{{.Exif.CameraMake}} {{.Exif.CameraModel}}{{if .Exif.Software}} · {{.Exif.Software}} ·{{end}}</div>
    <h1 class="hero-title">{{stem .Photo.FileBasename}}<br><span class="italic">{{.Exif.CameraMake}} {{.Exif.CameraModel}}.</span></h1>
    {{if .Exif.DateTaken}}
    <p class="hero-sub">Captured on {{humanDate .Exif.DateTaken}}{{if .Exif.Software}}, processed through {{.Exif.Software}}{{end}}{{if .Inference.PreviewMs}}. Preview generated in {{derefInt .Inference.PreviewMs}}ms{{end}}{{if .Inference.InferenceMs}}; inference completed in {{msToSeconds .Inference.InferenceMs}}{{end}}.</p>
    {{end}}
  </div>
  <div class="hero-footer">
    <div class="tagline">Shot on {{.Exif.CameraMake}} {{.Exif.CameraModel}}{{if .Exif.FocalLengthMM}} — {{printf "%.1f mm" (deref .Exif.FocalLengthMM)}}{{end}}{{if .Exif.FNumber}}, f/{{printf "%.1f" (deref .Exif.FNumber)}}{{end}}{{if .Exif.ShutterSeconds}}, {{shutterFraction (deref .Exif.ShutterSeconds)}}s{{end}}{{if .Exif.ISO}}, ISO {{derefInt .Exif.ISO}}{{end}}{{if .Exif.ExposureMode}}, {{.Exif.ExposureMode}} exposure{{end}}{{if .Exif.WhiteBalance}}, {{.Exif.WhiteBalance}} white balance{{end}}{{if .Exif.Flash}}, {{.Exif.Flash}}{{end}}.</div>
    <div class="centered">§</div>
    <a href="/" class="scroll">Back to library</a>
  </div>
</section>

{{if or .Description.Subject .Description.Setting .Description.Light .Description.Colors .Description.Composition}}
<section class="dual-pillars" data-screen-label="Visual Analysis">
  <div class="section-marker"><span class="numeral">II.</span><span class="rule"></span><span class="label">Visual Analysis</span></div>
  <h2 class="section-h2" style="margin-bottom:4rem;">Five fields. <em>Subject. Setting. Light. Colors. Composition.</em></h2>
  <div class="pillar-grid">
    <div class="pillar-group">
      <h3>Subject &amp; Setting</h3>
      {{if .Description.Subject}}<div class="pillar"><div class="head">Subject.</div><div class="body">{{.Description.Subject}}</div></div>{{end}}
      {{if .Description.Setting}}<div class="pillar"><div class="head">Setting.</div><div class="body">{{.Description.Setting}}</div></div>{{end}}
    </div>
    <div class="pillar-group">
      <h3>Light, Colors &amp; Composition</h3>
      {{if .Description.Light}}<div class="pillar"><div class="head">Light.</div><div class="body">{{.Description.Light}}</div></div>{{end}}
      {{if .Description.Colors}}<div class="pillar"><div class="head">Colors.</div><div class="body">{{.Description.Colors}}</div></div>{{end}}
      {{if .Description.Composition}}<div class="pillar"><div class="head">Composition.</div><div class="body">{{.Description.Composition}}</div></div>{{end}}
    </div>
  </div>
</section>
{{end}}

<section class="built photo-meta" data-screen-label="Camera Settings">
  <div class="section-marker"><span class="numeral">III.</span><span class="rule"></span><span class="label">Camera Settings</span></div>
  <h2 class="section-h2">All metadata for <em>this frame:</em></h2>
  <div class="requirement-list">
    {{if .Photo.FileBasename}}<div class="requirement"><div class="num">file_name</div><div class="text">{{.Photo.FileBasename}}</div></div>{{end}}
    <div class="requirement"><div class="num">name</div><div class="text">{{.Photo.Name}}</div></div>
    {{if .Photo.FilePath}}<div class="requirement"><div class="num">path</div><div class="text">{{.Photo.FilePath}}</div></div>{{end}}
    {{if .Exif.CameraMake}}<div class="requirement"><div class="num">make</div><div class="text">{{.Exif.CameraMake}}</div></div>{{end}}
    {{if .Exif.CameraModel}}<div class="requirement"><div class="num">model</div><div class="text">{{.Exif.CameraModel}}</div></div>{{end}}
    {{if .Exif.LensModel}}<div class="requirement"><div class="num">lens_model</div><div class="text">{{.Exif.LensModel}}</div></div>{{end}}
    {{if .Exif.LensInfo}}<div class="requirement"><div class="num">lens_info</div><div class="text">{{.Exif.LensInfo}}</div></div>{{end}}
    {{if .Exif.DateTaken}}<div class="requirement"><div class="num">date_taken</div><div class="text">{{.Exif.DateTaken}}</div></div>{{end}}
    {{if .Exif.FocalLengthMM}}<div class="requirement"><div class="num">focal_length</div><div class="text">{{printf "%.1f mm" (deref .Exif.FocalLengthMM)}}</div></div>{{end}}
    {{if .Exif.FNumber}}<div class="requirement"><div class="num">f_number</div><div class="text">f/{{printf "%.1f" (deref .Exif.FNumber)}}</div></div>{{end}}
    {{if .Exif.ShutterSeconds}}<div class="requirement"><div class="num">exposure_time</div><div class="text">{{shutterFraction (deref .Exif.ShutterSeconds)}}s</div></div>{{end}}
    {{if .Exif.ISO}}<div class="requirement"><div class="num">iso</div><div class="text">{{derefInt .Exif.ISO}}</div></div>{{end}}
    {{if .Exif.WhiteBalance}}<div class="requirement"><div class="num">white_balance</div><div class="text">{{.Exif.WhiteBalance}}</div></div>{{end}}
    {{if .Exif.ExposureMode}}<div class="requirement"><div class="num">exposure_mode</div><div class="text">{{.Exif.ExposureMode}}</div></div>{{end}}
    {{if .Exif.Flash}}<div class="requirement"><div class="num">flash</div><div class="text">{{.Exif.Flash}}</div></div>{{end}}
    {{if .Exif.Software}}<div class="requirement"><div class="num">software</div><div class="text">{{.Exif.Software}}</div></div>{{end}}
    {{if .Exif.Artist}}<div class="requirement"><div class="num">artist</div><div class="text">{{.Exif.Artist}}</div></div>{{end}}
    {{if .Inference.PreviewMs}}<div class="requirement"><div class="num">preview_ms</div><div class="text">{{derefInt .Inference.PreviewMs}}</div></div>{{end}}
    {{if .Inference.InferenceMs}}<div class="requirement"><div class="num">inference_ms</div><div class="text">{{derefInt .Inference.InferenceMs}}</div></div>{{end}}
    {{if .Inference.Model}}<div class="requirement"><div class="num">model</div><div class="text">{{.Inference.Model}}</div></div>{{end}}
  </div>
  {{if .Exif.DateTaken}}<p class="kicker"><em>All settings recorded at the moment of capture — {{humanDate .Exif.DateTaken}}.</em></p>{{end}}
</section>

</body>
</html>
`
