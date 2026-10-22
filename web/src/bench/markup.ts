// GENERATED TRANSCRIPTION — the page and pane markup of the font
// substitution bench, transcribed from the design prototype. Deltas
// from the prototype are mechanical only: the runtime's ref/sc-for/sc-if
// syntax became a pane template and data-when context blocks the logic
// toggles, the chevron icon is class-based instead of an external asset, and
// the prototype's uploads/ @font-face helmet is dropped — Deck's app.css
// already binds the licensed faces.

export function paneHtml(n: number): string {
  return `<div data-pane="" style="background:var(--surface-card);border:var(--hairline);border-radius:var(--radius-panel);overflow:hidden;min-width:0;font-family:var(--font-ui)">
<div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;row-gap:8px;padding:10px 12px;border-bottom:var(--hairline);background:var(--surface-muted);font-family:var(--tc-chrome,var(--font-ui))">
<span data-drag="" draggable="true" style="flex:none;display:grid;place-items:center;width:20px;height:20px;border-radius:var(--radius-mark);background:var(--accent);color:var(--text-on-accent);font-size:var(--type-label-sm-size);font-weight:var(--weight-semibold);cursor:grab">${n}</span>
<div class="bp-selectwrap" style="flex:1 1 120px;min-width:100px"><select data-pane-font="" class="bp-input bp-input--sm bp-select" aria-label="Pane typeface" style="font-family:var(--tc-chrome,var(--font-ui))"></select><i class="bp-icon bp-icon--chevron"></i></div>
<span class="bp-badge bp-badge--sm" style="flex:none" data-pane-lic="">Licensed</span>
<div style="display:flex;align-items:center;gap:6px;width:100%">
<span class="bp-label" style="flex:none;width:34px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">weight</span><div class="bp-selectwrap" style="flex:1 1 auto;min-width:0"><select class="bp-input bp-input--sm bp-select" data-wsel="" aria-label="Weight" style="font-family:var(--tc-chrome,var(--font-ui))"></select><i class="bp-icon bp-icon--chevron"></i></div>
</div>
<div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(96px,1fr));align-items:center;gap:6px 10px;width:100%">
<div style="display:flex;align-items:center;gap:5px;min-width:0"><span class="bp-label" style="flex:none;width:34px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">size</span><input type="range" data-trim="s" aria-label="Size trim" style="flex:1 1 auto;min-width:0;accent-color:var(--accent)"></div>
<div style="display:flex;align-items:center;gap:5px;min-width:0"><span class="bp-label" style="flex:none;width:34px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">lead</span><input type="range" data-trim="l" aria-label="Line height trim" style="flex:1 1 auto;min-width:0;accent-color:var(--accent)"></div>
<div style="display:flex;align-items:center;gap:5px;min-width:0"><span class="bp-label" style="flex:none;width:34px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">track</span><input type="range" data-trim="t" aria-label="Tracking trim" style="flex:1 1 auto;min-width:0;accent-color:var(--accent)"></div>
</div>
</div>
<div data-panebody="" style="padding:20px 22px">
<div data-when="answer" style="display:none">
<div class="bp-eyebrow">Research answer</div>
<h3 class="bp-display-3" style="margin-top:10px;font-size:calc(var(--type-title-1-size) * 1)">Where did the gains come from?</h3>
<div class="bp-prose" style="margin-top:14px;font-size:var(--type-prose-sm-size);line-height:var(--type-prose-sm-lh);max-width:none;letter-spacing:var(--tc-prose-ls,0)">
<p>Renewables covered 41% of demand across the twelve months, against 38% in 2025.<sup class="bp-cite">1</sup> Four grids cleared 60%; two stayed under 20%.</p>
<p>The gap is storage, not panels — curtailment absorbed 1 in 9 potential solar hours, and the hydro line is 3.0 points lower on the drought-year actual.</p>
</div>
<div style="margin-top:16px"><div class="bp-conf"><div class="bp-conf__bars"><span class="bp-conf__seg bp-conf__seg--on"></span><span class="bp-conf__seg bp-conf__seg--on"></span><span class="bp-conf__seg bp-conf__seg--on"></span><span class="bp-conf__seg"></span></div><div class="bp-conf__label"><span class="bp-conf__level">High confidence</span><span>9 primary</span></div></div></div>
</div>
<div data-when="interface" style="display:none">
<div style="border:var(--hairline);border-radius:var(--radius-card);padding:12px 14px;margin-bottom:12px;background:var(--surface-muted)"><div class="bp-label">Coverage · 2026</div><div style="font-size:var(--type-title-1-size);font-weight:var(--weight-medium)">41%</div><div style="font-size:var(--type-body-sm-size);color:var(--success)">Up 3 points on 2025</div></div>
<table class="bp-table" style="margin-bottom:14px">
<thead><tr><th class="bp-table__h">Source</th><th class="bp-table__h">Status</th><th class="bp-table__h bp-table__h--num">Share</th></tr></thead>
<tbody>
<tr class="bp-table__r"><td class="bp-table__c">Annual review</td><td class="bp-table__c"><span class="bp-badge bp-badge--success bp-badge--sm">Verified</span></td><td class="bp-table__c bp-table__c--num">41%</td></tr>
<tr class="bp-table__r"><td class="bp-table__c">Curtailment</td><td class="bp-table__c"><span class="bp-badge bp-badge--evidence bp-badge--sm">Quoted</span></td><td class="bp-table__c bp-table__c--num">11%</td></tr>
<tr class="bp-table__r"><td class="bp-table__c">Regional split</td><td class="bp-table__c"><span class="bp-badge bp-badge--warning bp-badge--sm">Contradicts</span></td><td class="bp-table__c bp-table__c--num">60%</td></tr>
</tbody>
</table>
<div class="bp-field" style="margin-bottom:12px"><label class="bp-field__label">Brief title</label><input class="bp-input bp-input--sm" type="text" value="Grid renewables, 2026" aria-label="Brief title"><span class="bp-field__hint">Appears at the head of every export.</span></div>
<div style="display:flex;gap:8px;flex-wrap:wrap"><button class="bp-btn bp-btn--primary bp-btn--sm">Save</button><button class="bp-btn bp-btn--secondary bp-btn--sm">Preview</button><button class="bp-btn bp-btn--ghost bp-btn--sm">Cancel</button></div>
</div>
<div data-when="machine" style="display:none">
<pre class="bp-code" style="margin:0;white-space:pre;overflow-x:auto"><span class="c">// caps the chrome depends on</span>
export function pickStack(role) {
  const free = catalogue
    .filter(f =&gt; f.licence === "ofl")
    .sort((a, b) =&gt; b.weights - a.weights);
  if (!free.length) return SYSTEM[role];
  return [free[0].family, ...SYSTEM[role]];
}

<span style="color:var(--text-tertiary)">09:12:06.030</span> <span style="color:var(--error)">ERROR</span> 4 of 4 paid
<span style="color:var(--text-tertiary)">09:12:06.944</span> <span style="color:var(--success)">INFO </span> 31 free indexed

0O o° 1lI| 5S$ 8B rn m {[()]} =&gt; !== ::</pre>
</div>
<div data-when="specimen" style="display:none">
<div style="font-size:var(--type-title-1-size);font-weight:var(--weight-medium);overflow:hidden;text-overflow:ellipsis;white-space:nowrap">Hamburgefonstiv</div>
<div style="margin-top:8px;font-size:var(--type-title-3-size);overflow:hidden;text-overflow:ellipsis;white-space:nowrap">The quick brown fox jumps over</div>
<div style="margin-top:6px;font-size:var(--type-body-size)">Pack my box with five dozen jugs</div>
<div style="margin-top:6px;font-size:var(--type-body-sm-size);color:var(--text-secondary)">0123456789 · $1,024.00 · 10% · 1lI 0O rn m</div>
<div style="display:flex;flex-wrap:wrap;gap:4px 18px;margin:14px 0 10px;font-size:var(--type-title-3-size)"><span style="font-weight:300">Light</span><span style="font-weight:400">Regular</span><span style="font-weight:500">Medium</span><span style="font-weight:700">Bold</span><span style="font-style:italic">Italic</span></div>
<div class="bp-label" style="margin-bottom:10px">Small caps · labels · tabs · badges</div>
<div contenteditable="true" spellcheck="false" style="padding:10px 12px;border:var(--hairline);border-radius:var(--radius-control);background:var(--surface-muted);font-size:var(--type-title-3-size);outline:none">Type here…</div>
</div>
</div>
</div>`;
}

export function pageHtml(panes: string): string {
  return `<div data-app="" style="min-height:100vh;background:var(--surface);color:var(--text-primary)">

<header data-topbar="" style="position:fixed;top:0;left:0;right:0;z-index:50;background:color-mix(in srgb,var(--surface) 90%,transparent);backdrop-filter:blur(14px);-webkit-backdrop-filter:blur(14px);border-bottom:var(--hairline);transition:transform .3s ease,opacity .3s ease">
<div style="display:flex;align-items:center;gap:14px;flex-wrap:wrap;max-width:1480px;margin:0 auto;padding:10px 24px">
<span class="bp-appname" style="font-size:19px">Font substitution</span>
<div class="bp-tabs bp-tabs--pill" data-modeseg=""><button class="bp-tab bp-tab--selected" data-mode="showcase">Showcase</button><button class="bp-tab" data-mode="compare">Compare</button></div>
<div style="flex:1;min-width:12px"></div>
<span data-note="" class="bp-field__hint" style="max-width:34ch;text-align:right"></span>
<div style="display:flex;align-items:center;gap:7px"><span class="bp-label" style="font-family:var(--tc-chrome,var(--font-ui))">Size</span><input type="range" data-scale="" min="0.85" max="1.3" step="0.05" value="1" aria-label="Overall size" style="width:104px;accent-color:var(--accent)"><span data-scaleout="" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;width:38px;font-family:var(--tc-chrome,var(--font-ui))">100%</span></div>
<button class="bp-btn bp-btn--secondary bp-btn--sm" data-scan="">Scan installed</button>
<div class="bp-selectwrap" style="width:auto"><select class="bp-input bp-input--sm bp-select" data-palette="" aria-label="Palette"><option value="cobalt">Cobalt</option><option value="neutral">Neutral</option><option value="warm">Warm</option><option value="teal">Teal</option><option value="violet">Violet</option></select><i class="bp-icon bp-icon--chevron"></i></div>
<label class="bp-switch bp-switch--sm"><input type="checkbox" data-dark=""><span class="bp-switch__track"><span class="bp-switch__thumb"></span></span><span style="font-size:var(--type-label-size);font-variant-caps:var(--type-caps);letter-spacing:var(--type-label-ls);color:var(--text-secondary)">Dark</span></label>
<button class="bp-btn bp-btn--ghost bp-btn--sm" data-focus="">Focus</button>
</div>
<div data-cmpctls="" style="display:none;align-items:center;gap:14px;flex-wrap:wrap;max-width:1480px;margin:0 auto;padding:8px 24px 10px;border-top:var(--hairline)">
<span class="bp-label">Context</span>
<div class="bp-tabs bp-tabs--pill" data-ctxseg=""><button class="bp-tab bp-tab--selected" data-ctx="answer">Answer</button><button class="bp-tab" data-ctx="interface">Interface</button><button class="bp-tab" data-ctx="machine">Machine</button><button class="bp-tab" data-ctx="specimen">Specimen</button></div>
<span class="bp-label">Match</span>
<div class="bp-tabs bp-tabs--pill"><button class="bp-tab" data-match="x" title="Match every pane to the reference: size so the lowercase measures the same, lead to hold the 21px line box, tracking to hold the character advance">x-height</button><button class="bp-tab" data-match="full" title="Match every pane to the reference: size so the digit 8 measures the same (a neutral tall glyph — no descender or cap disagreement), lead to hold the 21px line box, tracking to hold the character advance">full height</button></div>
<span class="bp-label">Substitute</span>
<div class="bp-selectwrap" style="width:auto"><select class="bp-input bp-input--sm bp-select" data-target="" aria-label="Role the candidate replaces"><option value="prose">Prose only</option><option value="display">Display only</option><option value="ui">Interface only</option><option value="mono">Mono only</option><option value="all">All four roles</option></select><i class="bp-icon bp-icon--chevron"></i></div>
</div>
</header>

<div data-frame="" style="display:flex;max-width:1480px;margin:0 auto;padding-top:var(--tc-topbar,104px)">

<aside data-rail="" style="position:sticky;top:var(--tc-topbar,104px);align-self:flex-start;width:304px;flex:none;height:calc(100vh - var(--tc-topbar,104px));overflow-y:auto;padding:26px 10px 40px 24px;transition:width .3s ease,padding .3s ease,opacity .25s ease">
<div class="bp-panel">
<div class="bp-panel__head"><span class="bp-panel__title">Roles</span><button class="bp-btn bp-btn--ghost bp-btn--sm" data-reset="">Reset</button></div>
<div class="bp-rows bp-rows--inset">
<div style="display:flex;flex-direction:column;gap:8px">
<div style="display:flex;align-items:center;justify-content:space-between;gap:8px"><span class="bp-label">Display</span><span class="bp-badge bp-badge--sm" data-lic="display">Licensed</span></div>
<div class="bp-selectwrap"><select class="bp-input bp-input--sm bp-select" data-font="display" aria-label="Display typeface"></select><i class="bp-icon bp-icon--chevron"></i></div>
<div style="display:grid;grid-template-columns:auto 1fr 42px;gap:6px 8px;align-items:center">
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">weight</span><div class="bp-selectwrap" style="grid-column:2 / span 2"><select class="bp-input bp-input--sm bp-select" data-wsel="" data-role="display" aria-label="Display weight"></select><i class="bp-icon bp-icon--chevron"></i></div>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">size</span><input type="range" data-trim="s" data-role="display" aria-label="Display size trim" style="width:100%;accent-color:var(--accent)"><span data-out="s" data-role="display" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">100%</span>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">lead</span><input type="range" data-trim="l" data-role="display" aria-label="Display line height trim" style="width:100%;accent-color:var(--accent)"><span data-out="l" data-role="display" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">100%</span>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">track</span><input type="range" data-trim="t" data-role="display" aria-label="Display tracking trim" style="width:100%;accent-color:var(--accent)"><span data-out="t" data-role="display" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">0</span>
</div>
</div>
<div style="display:flex;flex-direction:column;gap:8px">
<div style="display:flex;align-items:center;justify-content:space-between;gap:8px"><span class="bp-label">Interface</span><span class="bp-badge bp-badge--sm" data-lic="ui">Licensed</span></div>
<div class="bp-selectwrap"><select class="bp-input bp-input--sm bp-select" data-font="ui" aria-label="Interface typeface"></select><i class="bp-icon bp-icon--chevron"></i></div>
<div style="display:grid;grid-template-columns:auto 1fr 42px;gap:6px 8px;align-items:center">
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">weight</span><div class="bp-selectwrap" style="grid-column:2 / span 2"><select class="bp-input bp-input--sm bp-select" data-wsel="" data-role="ui" aria-label="Interface weight"></select><i class="bp-icon bp-icon--chevron"></i></div>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">size</span><input type="range" data-trim="s" data-role="ui" aria-label="Interface size trim" style="width:100%;accent-color:var(--accent)"><span data-out="s" data-role="ui" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">100%</span>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">lead</span><input type="range" data-trim="l" data-role="ui" aria-label="Interface line height trim" style="width:100%;accent-color:var(--accent)"><span data-out="l" data-role="ui" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">100%</span>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">track</span><input type="range" data-trim="t" data-role="ui" aria-label="Interface tracking trim" style="width:100%;accent-color:var(--accent)"><span data-out="t" data-role="ui" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">0</span>
</div>
</div>
<div style="display:flex;flex-direction:column;gap:8px">
<div style="display:flex;align-items:center;justify-content:space-between;gap:8px"><span class="bp-label">Prose</span><span class="bp-badge bp-badge--sm" data-lic="prose">Licensed</span></div>
<div class="bp-selectwrap"><select class="bp-input bp-input--sm bp-select" data-font="prose" aria-label="Prose typeface"></select><i class="bp-icon bp-icon--chevron"></i></div>
<div style="display:grid;grid-template-columns:auto 1fr 42px;gap:6px 8px;align-items:center">
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">weight</span><div class="bp-selectwrap" style="grid-column:2 / span 2"><select class="bp-input bp-input--sm bp-select" data-wsel="" data-role="prose" aria-label="Prose weight"></select><i class="bp-icon bp-icon--chevron"></i></div>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">size</span><input type="range" data-trim="s" data-role="prose" aria-label="Prose size trim" style="width:100%;accent-color:var(--accent)"><span data-out="s" data-role="prose" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">100%</span>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">lead</span><input type="range" data-trim="l" data-role="prose" aria-label="Prose line height trim" style="width:100%;accent-color:var(--accent)"><span data-out="l" data-role="prose" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">100%</span>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">track</span><input type="range" data-trim="t" data-role="prose" aria-label="Prose tracking trim" style="width:100%;accent-color:var(--accent)"><span data-out="t" data-role="prose" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">0</span>
</div>
</div>
<div style="display:flex;flex-direction:column;gap:8px">
<div style="display:flex;align-items:center;justify-content:space-between;gap:8px"><span class="bp-label">Mono</span><span class="bp-badge bp-badge--sm" data-lic="mono">Licensed</span></div>
<div class="bp-selectwrap"><select class="bp-input bp-input--sm bp-select" data-font="mono" aria-label="Mono typeface"></select><i class="bp-icon bp-icon--chevron"></i></div>
<div style="display:grid;grid-template-columns:auto 1fr 42px;gap:6px 8px;align-items:center">
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">weight</span><div class="bp-selectwrap" style="grid-column:2 / span 2"><select class="bp-input bp-input--sm bp-select" data-wsel="" data-role="mono" aria-label="Mono weight"></select><i class="bp-icon bp-icon--chevron"></i></div>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">size</span><input type="range" data-trim="s" data-role="mono" aria-label="Mono size trim" style="width:100%;accent-color:var(--accent)"><span data-out="s" data-role="mono" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">100%</span>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">lead</span><input type="range" data-trim="l" data-role="mono" aria-label="Mono line height trim" style="width:100%;accent-color:var(--accent)"><span data-out="l" data-role="mono" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">100%</span>
<span class="bp-label" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">track</span><input type="range" data-trim="t" data-role="mono" aria-label="Mono tracking trim" style="width:100%;accent-color:var(--accent)"><span data-out="t" data-role="mono" style="font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-variant-numeric:tabular-nums;text-align:right;font-family:var(--tc-chrome,var(--font-ui))">0</span>
</div>
</div>
</div>
</div>
<nav style="margin-top:22px;display:flex;flex-direction:column;gap:2px">
<div class="bp-eyebrow" style="padding:0 10px 6px">Sections</div>
<a class="bp-menuitem bp-menuitem--selected" href="#answer" data-sec="answer"><span class="bp-menuitem__label">Answer</span></a>
<a class="bp-menuitem" href="#interface" data-sec="interface"><span class="bp-menuitem__label">Interface</span></a>
<a class="bp-menuitem" href="#machine" data-sec="machine"><span class="bp-menuitem__label">Machine voice</span></a>
<a class="bp-menuitem" href="#specimen" data-sec="specimen"><span class="bp-menuitem__label">Specimen</span></a>
</nav>

</aside>

<main data-showcase="" style="flex:1;min-width:0;padding:30px 32px 120px;font-family:var(--font-ui)">

<section id="answer" style="margin-bottom:72px;scroll-margin-top:calc(var(--tc-topbar,104px) + 24px)">
<div style="margin-bottom:22px;font-family:var(--tc-chrome,var(--font-ui))">
<div class="bp-eyebrow" style="font-family:var(--tc-chrome,var(--font-ui))">Display · Prose · Evidence</div>
<h2 class="bp-title-2" style="margin-top:4px;font-family:var(--tc-chrome,var(--font-ui))">Answer</h2>
<p class="bp-field__hint" style="max-width:62ch">The reading surface: a display question, Baskerville answer text, citation markers and the source list.</p>
</div>
<div class="bp-card" style="padding:clamp(28px,4vw,52px)">
<div style="max-width:68ch;margin:0 auto">
<div class="bp-eyebrow">Research answer · 14 sources</div>
<h1 class="bp-display-3" style="margin-top:14px;max-width:22ch">How much of the 2026 grid ran on renewables?</h1>
<div class="bp-prose" style="margin-top:24px;letter-spacing:var(--tc-prose-ls,0)">
<p>Three quarters of the year's new capacity was wind or solar, but generation share moved less than the build-out suggests: renewables covered 41% of demand across the twelve months, against 38% in 2025.<sup class="bp-cite">1</sup> The gap is storage, not panels — curtailment absorbed roughly 1 in 9 potential solar hours in the two sunniest regions.<sup class="bp-cite">2</sup></p>
<p>Read the regional split before quoting the headline. Four grids cleared 60%; two stayed under 20%, and both of those are where demand grew fastest.<sup class="bp-cite">3</sup> A national average this wide is a summary of two different systems, not one.</p>
<p>One caveat. The 41% figure counts hydro at its long-run mean rather than the drought-year actual, which the grid authority reports separately at 3.0 points lower. Both numbers are kept here; the contradiction is flagged in the source list rather than resolved.</p>
</div>
<div style="margin-top:28px;max-width:320px">
<div class="bp-conf"><div class="bp-conf__bars"><span class="bp-conf__seg bp-conf__seg--on"></span><span class="bp-conf__seg bp-conf__seg--on"></span><span class="bp-conf__seg bp-conf__seg--on"></span><span class="bp-conf__seg"></span></div><div class="bp-conf__label"><span class="bp-conf__level">High confidence</span><span>9 primary · 1 contradicts</span></div></div>
</div>
<h3 style="margin-top:36px;padding-bottom:10px;border-bottom:var(--hairline);font-size:var(--type-title-3-size);letter-spacing:var(--type-title-3-ls)">Sources</h3>
<div style="margin-top:14px;display:flex;flex-direction:column;gap:10px">
<span class="bp-source"><span class="bp-source__num">1</span><span class="bp-source__body"><span class="bp-source__title">Annual electricity review 2026: capacity, generation and curtailment</span><span class="bp-source__meta">grid-authority.example<span>· Feb 2026</span></span><span class="bp-source__quote">Renewables covered 41% of demand across the twelve months, against 38% the previous year.</span></span></span>
<span class="bp-source"><span class="bp-source__num">2</span><span class="bp-source__body"><span class="bp-source__title">Curtailment in high-penetration solar regions</span><span class="bp-source__meta">energy-institute.example<span>· Jan 2026</span></span><span class="bp-source__quote">Roughly 1 in 9 potential solar hours was curtailed in the two sunniest regions.</span></span></span>
<span class="bp-source"><span class="bp-source__num">3</span><span class="bp-source__body"><span class="bp-source__title">Regional grids diverge as demand growth concentrates</span><span class="bp-source__meta">policy-review.example<span>· Dec 2025</span></span><span class="bp-source__quote">Four grids cleared 60%; two remained under 20%, both in the fastest-growing demand centres.</span></span></span>
</div>
</div>
</div>
</section>

<section id="interface" style="margin-bottom:72px;scroll-margin-top:calc(var(--tc-topbar,104px) + 24px)">
<div style="margin-bottom:22px;font-family:var(--tc-chrome,var(--font-ui))">
<div class="bp-eyebrow" style="font-family:var(--tc-chrome,var(--font-ui))">Interface · Mono in data</div>
<h2 class="bp-title-2" style="margin-top:4px;font-family:var(--tc-chrome,var(--font-ui))">Interface</h2>
<p class="bp-field__hint" style="max-width:62ch">Small caps chrome, tabular data, badges and controls — where a substitute usually shows its seams.</p>
</div>
<div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(330px,1fr));gap:16px;align-items:start">
<div style="display:flex;flex-direction:column;gap:16px;min-width:0">
<div class="bp-panel">
<div class="bp-panel__head"><span class="bp-panel__title">Evidence queue</span><span class="bp-panel__sub">Updated 09:12</span></div>
<table class="bp-table bp-table--inset">
<thead><tr><th class="bp-table__h">Source</th><th class="bp-table__h">Retrieved</th><th class="bp-table__h">Status</th><th class="bp-table__h bp-table__h--num">Coverage</th></tr></thead>
<tbody>
<tr class="bp-table__r"><td class="bp-table__c">Annual electricity review</td><td class="bp-table__c"><code>2026-02-14</code></td><td class="bp-table__c"><span class="bp-badge bp-badge--success">Verified</span></td><td class="bp-table__c bp-table__c--num">41%</td></tr>
<tr class="bp-table__r"><td class="bp-table__c">Curtailment in solar regions</td><td class="bp-table__c"><code>2026-01-30</code></td><td class="bp-table__c"><span class="bp-badge bp-badge--evidence">Quoted</span></td><td class="bp-table__c bp-table__c--num">11%</td></tr>
<tr class="bp-table__r"><td class="bp-table__c">Regional grids diverge</td><td class="bp-table__c"><code>2025-12-08</code></td><td class="bp-table__c"><span class="bp-badge bp-badge--warning">Contradicts</span></td><td class="bp-table__c bp-table__c--num">60%</td></tr>
<tr class="bp-table__r"><td class="bp-table__c">Provisional hydro series</td><td class="bp-table__c"><code>2025-11-19</code></td><td class="bp-table__c"><span class="bp-badge">Unread</span></td><td class="bp-table__c bp-table__c--num">3.0 pts</td></tr>
</tbody>
</table>
</div>
<div class="bp-panel">
<div class="bp-panel__head"><span class="bp-panel__title">Filters</span></div>
<div class="bp-panel__body" style="display:flex;flex-wrap:wrap;gap:8px">
<span class="bp-tag bp-tag--plain bp-tag--selected"><span class="bp-tag__label">Primary only</span></span>
<span class="bp-tag bp-tag--plain"><span class="bp-tag__label">Since 2025</span></span>
<span class="bp-tag bp-tag--plain"><span class="bp-tag__label">Peer reviewed</span></span>
<span class="bp-tag bp-tag--plain"><span class="bp-tag__label">Excludes preprints</span></span>
</div>
</div>
</div>
<div class="bp-panel" style="min-width:0">
<div class="bp-panel__head"><span class="bp-panel__title">Report defaults</span></div>
<div class="bp-panel__body" style="display:flex;flex-direction:column;gap:16px">
<div class="bp-field"><label class="bp-field__label">Brief title</label><input class="bp-input" type="text" value="Grid renewables, 2026" aria-label="Brief title"><span class="bp-field__hint">Appears at the head of every export.</span></div>
<div class="bp-field"><label class="bp-field__label">Citation style</label><div class="bp-selectwrap"><select class="bp-input bp-select" aria-label="Citation style"><option>Numbered markers</option><option>Author and year</option><option>Footnotes</option></select><i class="bp-icon bp-icon--chevron"></i></div></div>
<label class="bp-choice"><input type="checkbox" checked="" style="accent-color:var(--accent)"><span class="bp-choice__body">Flag contradictions inline<span class="bp-choice__desc">Marks any figure that two sources disagree on.</span></span></label>
<div style="display:flex;gap:8px;flex-wrap:wrap"><button class="bp-btn bp-btn--primary">Save defaults</button><button class="bp-btn bp-btn--secondary">Preview</button><button class="bp-btn bp-btn--ghost">Reset</button></div>
<div style="display:flex;gap:10px;align-items:flex-start;padding:12px 14px;border-radius:var(--radius-control);background:var(--accent-tint);border:1px solid var(--accent-border);font-size:var(--type-body-sm-size);color:var(--text-secondary)"><span><b style="color:var(--accent-on-tint);font-weight:var(--weight-medium)">Draft saved.</b> 14 sources carried over, 1 flagged for review.</span></div>
</div>
</div>
</div>
</section>

<section id="machine" style="margin-bottom:72px;scroll-margin-top:calc(var(--tc-topbar,104px) + 24px)">
<div style="margin-bottom:22px;font-family:var(--tc-chrome,var(--font-ui))">
<div class="bp-eyebrow" style="font-family:var(--tc-chrome,var(--font-ui))">Mono</div>
<h2 class="bp-title-2" style="margin-top:4px;font-family:var(--tc-chrome,var(--font-ui))">Machine voice</h2>
<p class="bp-field__hint" style="max-width:62ch">Code, logs and diffs. Blueprint sets mono bold and leans on a true cursive italic for comments — the two things a free mono most often lacks.</p>
</div>
<div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:16px;align-items:start">
<div style="display:flex;flex-direction:column;gap:16px;min-width:0">
<div class="bp-panel">
<div class="bp-panel__head"><span class="bp-panel__title">stacks.ts</span></div>
<div class="bp-panel__body"><pre class="bp-code" style="margin:0;white-space:pre;overflow-x:auto"><span class="c">// The substitute has to carry the caps the chrome depends on.</span>
export function pickStack(role: Role): string[] {
  const free = catalogue.filter(f =&gt; f.licence === "ofl");
  const ranked = free
    .filter(f =&gt; f.roles.includes(role) &amp;&amp; f.smallCaps)
    .sort((a, b) =&gt; b.weights - a.weights);

  <span class="c">// No candidate clears the bar — fall back to the system stack.</span>
  if (!ranked.length) return SYSTEM[role];
  return [ranked[0].family, ...SYSTEM[role]];
}</pre></div>
</div>
<div class="bp-panel">
<div class="bp-panel__head"><span class="bp-panel__title">typography.css</span></div>
<div class="bp-panel__body"><pre class="bp-code" style="margin:0;white-space:pre;overflow-x:auto"><span style="color:var(--text-tertiary)">@@ -5,4 +5,4 @@</span>
<span style="display:block;background:var(--error-tint)">- --font-ui: "Euclid Flex", sans-serif;</span>
<span style="display:block;background:var(--success-tint)">+ --font-ui: "Public Sans", sans-serif;</span>
  --font-prose: "Baskerville 10 Pro", serif;</pre></div>
</div>
</div>
<div style="display:flex;flex-direction:column;gap:16px;min-width:0">
<div class="bp-panel">
<div class="bp-panel__head"><span class="bp-panel__title">audit.log</span></div>
<div class="bp-panel__body"><pre class="bp-code" style="margin:0;white-space:pre;overflow-x:auto"><span style="color:var(--text-tertiary)">09:12:04.118</span> <span style="color:var(--success)">INFO </span> 4 families in use
<span style="color:var(--text-tertiary)">09:12:04.402</span> <span style="color:var(--success)">INFO </span> euclid-flex ok, 14 cuts
<span style="color:var(--text-tertiary)">09:12:05.771</span> <span style="color:var(--warning)">WARN </span> dank-mono: no bold italic
<span style="color:var(--text-tertiary)">09:12:06.030</span> <span style="color:var(--error)">ERROR</span> licence: 4 of 4 non-redistributable
<span style="color:var(--text-tertiary)">09:12:06.944</span> <span style="color:var(--success)">INFO </span> 31 free candidates indexed</pre></div>
</div>
<div class="bp-panel">
<div class="bp-panel__head"><span class="bp-panel__title">Ambiguity</span></div>
<div class="bp-panel__body"><pre class="bp-code" style="margin:0;white-space:pre;overflow-x:auto">0O o° 1lI| i¡ 5S s$ 8B rn m cl d
{[()]} &lt;=&gt; != === -&gt; =&gt; :: ;; ,.
il1Il1 · O0Q0O · S5$5S · Z2z2Z · B8&amp;8B</pre></div>
</div>
</div>
</div>
</section>

<section id="specimen" style="scroll-margin-top:calc(var(--tc-topbar,104px) + 24px)">
<div style="margin-bottom:22px;font-family:var(--tc-chrome,var(--font-ui))">
<div class="bp-eyebrow" style="font-family:var(--tc-chrome,var(--font-ui))">All four roles</div>
<h2 class="bp-title-2" style="margin-top:4px;font-family:var(--tc-chrome,var(--font-ui))">Specimen</h2>
<p class="bp-field__hint" style="max-width:62ch">Each role at the sizes and weights Blueprint actually asks for. A candidate that misses one of these is a candidate that changes the system.</p>
</div>

<div class="bp-card" style="margin-bottom:14px">
<div style="display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;padding-bottom:12px;border-bottom:var(--hairline);font-family:var(--tc-chrome,var(--font-ui))"><span class="bp-label" style="color:var(--accent);font-family:var(--tc-chrome,var(--font-ui))">Display</span><span style="font-size:var(--type-title-3-size);font-weight:var(--weight-medium)" data-fname="display">Euclid Flex XL</span><span class="bp-badge bp-badge--sm" data-lic="display">Licensed</span><span class="bp-field__hint" style="margin-left:auto">always Light 300, always caps</span></div>
<div style="padding-top:16px;display:flex;flex-direction:column;gap:10px">
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">display 1</span><span class="bp-display-1" style="min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">Evidence</span></div>
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">display 2</span><span class="bp-display-2" style="min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">Sources first</span></div>
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">display 3</span><span class="bp-display-3" style="min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">What changed in 2026?</span></div>
</div>
<div style="margin-top:18px;font-family:var(--font-display);font-weight:var(--weight-light);text-transform:uppercase;letter-spacing:var(--type-display-3-ls);font-size:calc(var(--type-title-1-size) * 1)">ABCDEFGHIJKLMNOPQRSTUVWXYZ 0123456789 10% No. 42</div>
<div contenteditable="true" spellcheck="false" style="margin-top:14px;padding:12px 16px;border:var(--hairline);border-radius:var(--radius-control);background:var(--surface-muted);font-family:var(--font-display);font-weight:var(--weight-light);text-transform:uppercase;letter-spacing:var(--type-display-3-ls);font-size:var(--type-title-1-size);outline:none">Type the words you will actually set</div>
<div class="bp-field__hint" style="margin-top:6px;font-family:var(--tc-chrome,var(--font-ui))">Editable.</div>
</div>

<div class="bp-card" style="margin-bottom:14px">
<div style="display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;padding-bottom:12px;border-bottom:var(--hairline);font-family:var(--tc-chrome,var(--font-ui))"><span class="bp-label" style="color:var(--accent);font-family:var(--tc-chrome,var(--font-ui))">Interface</span><span style="font-size:var(--type-title-3-size);font-weight:var(--weight-medium)" data-fname="ui">Euclid Flex</span><span class="bp-badge bp-badge--sm" data-lic="ui">Licensed</span><span class="bp-field__hint" style="margin-left:auto">chrome · labels · data</span></div>
<div style="padding-top:16px;display:flex;flex-direction:column;gap:8px">
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">title 1</span><span class="bp-title-1" style="min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">Evidence queue</span></div>
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">title 3</span><span class="bp-title-3" style="min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">Retrieved this week</span></div>
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">body</span><span style="min-width:0;font-size:var(--type-body-size);line-height:var(--type-body-lh);overflow:hidden;text-overflow:ellipsis;white-space:nowrap">Four grids cleared 60% of demand; two stayed under 20%.</span></div>
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">body sm</span><span style="min-width:0;font-size:var(--type-body-sm-size);overflow:hidden;text-overflow:ellipsis;white-space:nowrap">Pack my box with five dozen liquor jugs — $1,024.00 · 3.14159 · No. 42</span></div>
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">label</span><span class="bp-label" style="min-width:0">Small caps carry every label, tab, badge and tag</span></div>
</div>
<div style="display:flex;flex-wrap:wrap;gap:6px 26px;margin:18px 0 14px;font-size:var(--type-title-3-size)">
<span style="font-weight:300">Light<small style="display:block;font-family:var(--tc-chrome,var(--font-ui));font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-weight:400">300</small></span>
<span style="font-weight:400">Regular<small style="display:block;font-family:var(--tc-chrome,var(--font-ui));font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-weight:400">400</small></span>
<span style="font-weight:500">Medium<small style="display:block;font-family:var(--tc-chrome,var(--font-ui));font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-weight:400">500</small></span>
<span style="font-weight:600">Semibold<small style="display:block;font-family:var(--tc-chrome,var(--font-ui));font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-weight:400">600</small></span>
<span style="font-weight:700">Bold<small style="display:block;font-family:var(--tc-chrome,var(--font-ui));font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-weight:400">700</small></span>
<span style="font-style:italic">Italic<small style="display:block;font-family:var(--tc-chrome,var(--font-ui));font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-style:normal;font-weight:400">400i</small></span>
</div>
<div style="font-size:var(--type-body-size);line-height:1.8;word-break:break-all">ABCDEFGHIJKLMNOPQRSTUVWXYZ abcdefghijklmnopqrstuvwxyz 0123456789 10% 0.1 &amp;?!@#$€£ ({[—–·…]})</div>
<div contenteditable="true" spellcheck="false" style="margin-top:14px;padding:12px 16px;border:var(--hairline);border-radius:var(--radius-control);background:var(--surface-muted);font-size:var(--type-title-3-size);outline:none">Handgloves &amp; Hamburgefonstiv — type here</div>
<div class="bp-field__hint" style="margin-top:6px;font-family:var(--tc-chrome,var(--font-ui))">Editable.</div>
</div>

<div class="bp-card" style="margin-bottom:14px">
<div style="display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;padding-bottom:12px;border-bottom:var(--hairline);font-family:var(--tc-chrome,var(--font-ui))"><span class="bp-label" style="color:var(--accent);font-family:var(--tc-chrome,var(--font-ui))">Prose</span><span style="font-size:var(--type-title-3-size);font-weight:var(--weight-medium)" data-fname="prose">Baskerville 10 Pro</span><span class="bp-badge bp-badge--sm" data-lic="prose">Licensed</span><span class="bp-field__hint" style="margin-left:auto">answers · quotes · excerpts</span></div>
<div data-wrole="prose" style="padding-top:16px;display:flex;flex-direction:column;gap:10px;letter-spacing:var(--tc-prose-ls,0)">
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">lead</span><span style="min-width:0;font-family:var(--font-prose);font-size:var(--type-prose-lead-size);line-height:var(--type-prose-lead-lh);overflow:hidden;text-overflow:ellipsis;white-space:nowrap">The gap is storage, not panels.</span></div>
<div style="font-family:var(--font-prose);font-size:var(--type-prose-size);line-height:var(--type-prose-lh)">Nobody remembers the typeface of a brief that held them — that is the point. A well-set answer disappears the way a good window does: you see the evidence, not the glass. Figures do the opposite: 41% of demand, 1 in 9 hours curtailed, 3.0 points of hydro. <em>Italic carries the quote</em>, and the reader should not have to notice why.</div>
<div style="font-family:var(--font-prose);font-size:var(--type-prose-sm-size);line-height:var(--type-prose-sm-lh);color:var(--text-secondary);padding-left:10px;border-left:2px solid var(--signal-border)">Renewables covered 41% of demand across the twelve months, against 38% the previous year.</div>
</div>
<div data-wrole="prose" style="margin-top:16px;font-family:var(--font-prose);font-size:var(--type-prose-sm-size);line-height:1.9;word-break:break-all">ABCDEFGHIJKLMNOPQRSTUVWXYZ abcdefghijklmnopqrstuvwxyz 0123456789 10% &amp;?!@#$€£ ({[—–·…]})</div>
<div contenteditable="true" spellcheck="false" data-wrole="prose" style="margin-top:14px;padding:12px 16px;border:var(--hairline);border-radius:var(--radius-control);background:var(--surface-muted);font-family:var(--font-prose);font-size:var(--type-prose-size);outline:none">Handgloves &amp; Hamburgefonstiv — type here</div>
<div class="bp-field__hint" style="margin-top:6px;font-family:var(--tc-chrome,var(--font-ui))">Editable.</div>
</div>

<div class="bp-card">
<div style="display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;padding-bottom:12px;border-bottom:var(--hairline);font-family:var(--tc-chrome,var(--font-ui))"><span class="bp-label" style="color:var(--accent);font-family:var(--tc-chrome,var(--font-ui))">Mono</span><span style="font-size:var(--type-title-3-size);font-weight:var(--weight-medium)" data-fname="mono">Dank Mono</span><span class="bp-badge bp-badge--sm" data-lic="mono">Licensed</span><span class="bp-field__hint" style="margin-left:auto">set bold · cursive italic at 400</span></div>
<div style="padding-top:16px;display:flex;flex-direction:column;gap:10px">
<div data-wrole="mono" style="font-family:var(--font-mono);font-weight:var(--type-mono-weight);font-size:var(--type-title-1-size);letter-spacing:var(--type-mono-tracking);overflow:hidden;text-overflow:ellipsis;white-space:nowrap">const answer = 0x2A;</div>
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">mono</span><code style="min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">SELECT id, share FROM grid WHERE year = 2026 ORDER BY share DESC;</code></div>
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">cursive</span><code style="min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap"><em>// the comment is a different voice, not a lighter one</em></code></div>
<div style="display:flex;align-items:baseline;gap:12px"><span class="bp-label" style="flex:none;width:74px;font-size:var(--type-label-sm-size);color:var(--text-tertiary);font-family:var(--tc-chrome,var(--font-ui))">in a cell</span><span style="font-size:var(--type-body-sm-size)">Retrieved <code>2026-02-14</code> · <code>41.0%</code> · <code>1/9</code></span></div>
</div>
<div data-wrole="mono" style="margin-top:16px;font-family:var(--font-mono);font-weight:var(--type-mono-weight);font-size:var(--type-body-size);line-height:1.8;word-break:break-all">ABCDEFGHIJKLM abcdefghijklm 0123456789 10% 0O 1lI ~^&lt;&gt;|\\/ ({[—·…]})</div>
<div contenteditable="true" spellcheck="false" data-wrole="mono" style="margin-top:14px;padding:12px 16px;border:var(--hairline);border-radius:var(--radius-control);background:var(--surface-muted);font-family:var(--font-mono);font-weight:var(--type-mono-weight);font-size:var(--type-title-3-size);outline:none">let focus = true; // type here</div>
<div class="bp-field__hint" style="margin-top:6px;font-family:var(--tc-chrome,var(--font-ui))">Editable.</div>
</div>
</section>
</main>

<main data-compare="" style="display:none;flex:1;min-width:0;padding:30px 32px 120px">
<div style="margin-bottom:22px;font-family:var(--tc-chrome,var(--font-ui))">
<div class="bp-eyebrow" style="font-family:var(--tc-chrome,var(--font-ui))">Side by side</div>
<h2 class="bp-title-2" style="margin-top:4px;font-family:var(--tc-chrome,var(--font-ui))" data-cmptitle="">Three typefaces, in one answer</h2>
<p class="bp-field__hint" style="max-width:62ch">Each pane swaps one role and leaves the other three on the faces the showcase is set to — a substitution you would actually ship. Drag a number to reorder.</p>
</div>
<div data-grid="" style="display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px;align-items:start">
${panes}
</div>
</main>

</div>
<button data-focuspill="" class="bp-btn bp-btn--secondary bp-btn--sm bp-btn--pill" style="display:none;position:fixed;right:20px;bottom:20px;z-index:60;opacity:.5">Exit focus · F</button>
</div>
`;
}
