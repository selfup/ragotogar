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
    .status { color: var(--mute); font-size: 0.875rem; margin: 1rem 0; }
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
        <label class="{{if eq .Mode "naive"}}active{{end}}" title="Pure vector similarity. Fast, broad recall.">
          <input type="radio" name="mode" value="naive" {{if eq .Mode "naive"}}checked{{end}} onchange="this.form.submit()">vector
        </label>
        <label class="{{if eq .Mode "naive-verify"}}active{{end}}" title="Vector retrieval, then LLM yes/no check on each candidate. Slower (~3–6s) but higher precision.">
          <input type="radio" name="mode" value="naive-verify" {{if eq .Mode "naive-verify"}}checked{{end}} onchange="this.form.submit()">naive-verify
        </label>
        <label class="{{if eq .Mode "local"}}active{{end}}" title="LLM extracts keywords, then walks the graph. Often underperforms naive on small corpora.">
          <input type="radio" name="mode" value="local" {{if eq .Mode "local"}}checked{{end}} onchange="this.form.submit()">graph
        </label>
        <label class="{{if eq .Mode "hybrid"}}active{{end}}" title="Local + global. Broadest coverage.">
          <input type="radio" name="mode" value="hybrid" {{if eq .Mode "hybrid"}}checked{{end}} onchange="this.form.submit()">hybrid
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
      {{else}}no matches for "{{.Q}}" — try different words or broader concepts
      {{end}}
    </div>
  {{end}}

  {{if .Results}}
    <div class="grid">
      {{range .Results}}
        <a href="/photos/{{.Name}}.html" title="{{.Name}}">
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
