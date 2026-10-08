# magpie web UI, gateway mode: fact survey

Repo `~/Code/GitHub/magpie`. All paths below are under `internal/gui/` unless noted; `A` = `assets/`.
Built and run: `go build -tags nogui -o magpie-bin .` (about 70 s; the default build links Wails/cgo, `nogui` skips it), then `HOME=<tmp> magpie-bin web --gateway --no-open --addr 127.0.0.1:18765`. Screenshots are driven with playwright-core and system Chrome, saved to `scratchpad/magpie-shots/` (31 PNGs, light and dark). All processes were killed.

## 1. Shell and navigation (gateway mode)

- **Top tabs, no sidebar.** `index.html:30-39` has `<header class="top">`: brand (bird logo + "magpie") on the left, `<nav class="seg" id="nav">` absolutely centred (`app.css:207-216`), icon buttons on the right.
- The nav markup lists 8 buttons. Gateway mode hides `agents`, `sessions` and `library` (`GATEWAY_HIDES`, `A/app.js:17`; `setGatewayMode`, `A/app.js:19930-19935`). What remains, in order: **Providers, Gateway, Routing, Usage, Plugins**. Settings is not a tab: it is the gear icon at the top right (`#prefs`, `A/app.js:19945`). Providers is the landing page.
- The tab questions:
  - Providers: who do I talk to upstream, and which models do I expose.
  - Gateway: how do clients connect to me (URL, keys, snippets, models, recent calls).
  - Routing: which upstream actually served each request (live trace) and routing groups.
  - Usage: how much, for what.
- Header right side: Update pill (only when a newer version exists, with a hover-only dismiss ×), refresh icon (`#sync`, its meaning follows the tab, `A/app.js:19970+`), "Open as window", gear. There is no user menu, no search and no breadcrumb.
- Mode is decided in `gatewaymode.go:35-48`. Priority: Settings choice (`on`/`off`), then the `--gateway` flag, then "no agent detected on this machine" (container or NAS). The page switches with `POST /api/settings/gateway-mode`. The page is gated by one random key in the URL (`?k=`) that is traded for an HttpOnly cookie (`web.go:60-170`).
- **Settings page**: sub-tabs General, Allowances, Network and sharing, Models, Privacy, Observability, Sync and backup, About. Each is a `.list` of label + subtitle + control rows.
  - Gateway mode hides only rows tied to local agents or the desktop: plain names, Codex agents v1, full context, Codex titles/auto-review, usage/balance/reset alerts, bar icon (`A/app.js:17881`, `17429`).
  - General gains a "Gateway mode: Automatic / On / Off" row (`A/app.js:17890-17902`).
  - Screenshot: `light-settings.png`.
- View transition: `.view { animation: view-in .24s }` (fade plus 5px rise, `app.css:297-298`).

## 2. Providers tab

### Layout
- Single-column **list of rows inside one rounded card** (`.list` plus `.row.provider`), not cards or master-detail. Row, left to right: logo (also a drag handle for ordering), name + sub line ("host · N models", or "signed in as X"), small agent icons showing who uses it, a fixed-width key pill (`no key`, `needs a key`, masked key, plan name), an **on/off switch**, a chevron (`A/app.js:4586-4686`). Turned-off providers move to a collapsible "Turned off" group.
- Under the list is a plain text button "+ Add provider" (`index.html:77`).
- **Clicking a row opens a modal dialog** (centred, 600 px, blurred backdrop) holding the editor. It is not an inline expansion (the `selected` class remains from an older design). Screenshots: `f2-1-providers-list.png`, `f2-2-models.png`, `f2-3-names.png`.
- With zero providers the page IS the add sheet, headed "ADD YOUR FIRST PROVIDER" (`light-providers.png`).

### Creating a provider (`renderAdd`, `A/app.js:6329-6477`)
1. The add sheet is a searchable grid of ~55 preset tiles in sections. Each tile has a real brand logo and a name, and the grid is 5 columns wide.
   - Subscriptions (sign in, no key): Claude, ChatGPT, Copilot, Gemini CLI, Antigravity, plus "More in Plugins".
   - Vendors (the makers' own APIs): Anthropic, OpenAI, Gemini, DeepSeek, Kimi, GLM, Qwen, Mistral, and others.
   - Relays (one key, many vendors): OpenRouter, SiliconFlow, and others.
   - On this machine: Ollama, LM Studio, oMLX.
   - Footer line: "+ Custom provider — any OpenAI or Anthropic compatible URL".
2. A "Find a vendor…" search box filters by name, id, host or note. No hit gives "Nothing called X. Add it as a custom provider, or look for a plugin". The "Import…" button brings in providers configured in other apps (`openImportApps`, `A/app.js:8405`), and a discovery banner (`#providerDiscovery`) offers local configs it finds.
3. **Preset tile**: the dialog is tiny, with just API key (+ Show, + "Get a key ↗" link to the vendor's keys page), optional headers, proxy and so on. Models are not asked for up front. The hint text says "none picked, magpie asks for the list after saving". A vendor with global and China endpoints is one tile with a Global/China segmented control (`pairTile`, plus the editor's Region seg). Screenshot: `preset-deepseek-dark.png`.
4. **Custom provider** form fields:
   - Name (id auto-slugged).
   - Base URL with a **protocol segmented control**: OpenAI compatible / OpenAI Responses / Anthropic compatible / System One. Each protocol keeps its own URL (`A/app.js:7620-7700`).
   - "Detect APIs" button (probes which protocols answer) and "Each picked model".
   - API key, with Show.
   - Icon: choose a picture, favicon from the website, or built-in icons.
   - Headers (key/value rows plus a raw JSON toggle).
   - Proxy: Global / Direct / Custom.
   - Concurrency, queue size, queue wait, price rate (relay multiplier), "Send requests unmasked".
   - Account balance URL and field (optional).
   - Context window, max output and "compact at", each a text field with quick-pick chips (128K, 200K, ...).
   - Models (see below).
5. Footer buttons: Cancel and Add or Save. When editing there are also Remove and Duplicate on the left. The head has a link to the vendor site and the on/off switch. Label column on the left is 72 px, right-aligned, and the form is a 2-column grid (`.editor`, `app.css:2241`). There are many fields, so it is dense.

### Adding and managing models under a provider
**Flow A, custom provider, before first save** (`addFormModels`, `A/app.js:8747-8816`; screenshot `flow-4-picked.png`):
1. Type the base URL and optionally a key.
2. Click **Fetch models**. It calls `POST /api/provider/list` with the unsaved form values, "nothing is saved".
3. The result is a chip cloud. Each chip is a pill; clicking toggles a pick and picked chips show a tick in accent color.
4. Above the chips: "N of M picked", **Pick all** (becomes "Pick those shown" under a filter) and **Pick none**. Over 24 models a "filter N models…" box appears. Only the first 120 are drawn, then a "show N more" control.
5. The text box above it mirrors the picks as comma-separated ids and is editable. This is the manual-add path.
6. Empty list gives "The vendor's list is empty. Type the model ids instead."

**Flow B, saved provider editor** (`renderModels`, `A/app.js:8993-9560`; screenshots `f2-2-models.png`, `f2-3-names.png`):
- **Source of the list**: the vendor's live `/models` response, merged with models.dev metadata. The catalog is `internal/catalog` (caches models.dev; Codex's model cache; a built-in fallback). The `Model` struct has ID, Name, Efforts (reasoning levels), Price (per 1M, with long-context tiers), Context, MaxContext, Output, Images, APIs, Fast, and so on (`catalog/catalog.go:25-70`). The list excludes image, embedding and speech models; image models go to a separate "Images" setting.
- The list is chips (max about 132 px tall, scrolls, with an "Expand the list" fold). Each chip has the name (or id), a `free` badge, a rate tag, a context tag such as `128K`, and a test dot.
- **Picking semantics**:
  - Picked chips get the accent look.
  - With none picked, agents are served "the vendor's list, up to 24", drawn as **dashed** chips. Clicking one picks just that model.
  - The "Only through routing groups" checkbox hides all models from direct use.
- **Bulk and search**: Select all, Select none, and "Free only" (only offered when the list mixes free and paid). A filter box appears at over 24 models and filters by id, name and default name.
- **Manual add**: an "add a model id…" input plus an Add model button (comma-separated ok). Such chips show as "Added by hand" with a plus sign.
- **Refresh** button (`POST /api/provider/models`) asks the vendor again and drops picks the vendor no longer lists. A footer note shows "vendor list · just now".
- **Test models** (`POST /api/provider/test`) sends a tiny request per model. Each chip gets a green or red dot, with a tooltip like "Answered via OpenAI in 120ms" or the error. **Right-click a chip** gives a context menu: Test this model, Copy model ID, "Asked on: Auto…" (per-model protocol override). Detect APIs and a "Test" action also exist under Endpoints.
- **"Names & levels"** button expands a per-picked-model sub-form (`drawNames`, `A/app.js:9170-9370`), staged until Save with an "unsaved" tag and "Restore default":
  - Display name (placeholder is the catalog name).
  - "Same as" (merge key with other providers' same model, used for auto routing groups).
  - **Price**, $ per 1M tokens: Input, Output, Cache read, Cache write, Cache write 1h (Claude only), and an optional "Long-context price over N tokens" row. Empty boxes show the catalog list price greyed as placeholder.
  - "Accepts images" checkbox.
  - Per-model API segmented control (Auto / chat / responses / anthropic).
  - Reasoning-level tickboxes (low/medium/high...), which must keep at least one.
  - Above them: "Provider in model names" (Off / Not on names I set / On) controls the suffix, so display becomes "GPT-4o · Fake Relay".
- Provider-wide: Context window, Max output and Compact-at (text plus quick chips, accepts `model=size`), a **Fallback** field (model to fall back to), Price rate, concurrency and queue.
- There is no per-model enable/disable toggle. "Enabled" means "picked", or "none picked, so the first 24". Enable/disable exists at provider level (the switch).

## 3. Gateway tab (`index.html:98-117`; `A/app.js:5021-5760`, `A/caller-keys.js`)

Top to bottom (screenshots `f2-5-gateway.png`, `g-keys-models.png`):
1. **Status card**: green dot, "Gateway running", "N models · no agent routed through it yet · five APIs, one URL", a Restart gateway button (or "Quit magpie X and take over" if an older instance holds the port, with port-conflict diagnosis for non-magpie, container and VM cases). The URL is a copyable pill on the right (`http://127.0.0.1:3425`).
2. **GATEWAY KEYS** block (only when "share on local network" is on, `providers.gateway.lan`):
   - Rows: avatar tick, name, masked secret `sk-magpie-key-…fbb8d9`, a model-scope button (default "All models"), a **Limit** button, copy, Rotate key, Remove. Add gateway key takes a name plus an optional "your own key" (so existing clients keep working).
   - **Per-key model/account allowlist** (`caller-keys.js:200-300`): a dropdown, "Models this key may use", with "All models", "Every `<provider>` model" (`provider/*`), individual `provider/model`, routing groups, and specific accounts or API keys (`provider/<acct>`). Backend: `access.Key{Models, Accounts}` (`access/access.go:109-125`), endpoints `GET /api/caller-keys`, `GET /api/caller-keys/models`, `POST /api/caller-keys/{action}` (`caller_keys.go`).
   - **Per-key budget** (the Limit button, `caller-keys.js:340-480`): token cap and estimated-cost cap, with a window of daily/weekly/monthly (calendar windows) and a "Count cache reads too" option. It shows "used of limit, left", with the reset time on hover.
   - Usage is counted per key.
3. **CONNECT** (foldable): rows API (seg: OpenAI / Responses / Anthropic / Gemini / System One), Base URL (copy pill; a dropdown when LAN URLs exist), API key (when LAN is on, pick a gateway key from a dropdown; otherwise `magpie`, "any value works on loopback"), Model (`provider/model` pill that follows what you click in the list below), and an Example block with a seg (Shell / curl / Python / Node) and a syntax-highlighted code box with a copy button. Each row has a one-line explanation under it ("What OPENAI_BASE_URL takes").
4. **MODELS N**: list of every exposed model: logo, `provider/model` in monospace, "Display name · Provider" as sub, badges for reasoning range (`low–high`), image-capable icon and context (`128K`), and a copy icon. Clicking a row selects it into the snippets. There is a "Copy all ids" button. Routing groups appear here too.
5. **RECENT CALLS**: live feed rows (time, agent, model, protocol, status plus latency). Rows expand to show headers and bodies as JSON trees or SSE event views, with archive download.

## 4. Routing tab (summary)

Not a config form first. It is a **live trace** (`routing.js`, 4163 lines): agent box, magpie box, then upstream account rows, animated per request. It shows the strategy ("Smart": which account has allowance renewing soonest goes first), counters (requests / rerouted / errors the agent saw) and "How the last request was routed" as a narrated step list with View usage and Replay. A "Context window" card shows a block-grid fill meter plus prompt-cache hit rate. Controls: **New group** and **Hide accounts** (screenshot mask). A routing group is a virtual model composed of member models. It has members, an optional classifier ("decision") model, effort levels, nesting (`groups.go:14-60`), and "found" groups auto-merged by the `same` key. Fallback is also set per provider (editor). Screenshot: `f2-5-routing.png`.

## 5. Usage tab (`index.html:122-160`; `A/app.js:12404-15840`)

- Header strip: sub-tabs **Overview / Requests / Sessions / Context** (seg, remembered in `localStorage`) and a period seg **Today / 7 days / 30 days / All**. Right side: refresh icon, auto-refresh interval pill ("5 s"), and a big "≈$0.000 effective prices". A one-line scope note sits under it ("Gateway calls only…").
- **Overview**: a KPI strip of 4 cells in one bordered card (Tokens with "in · out", Cache read, Reasoning, Calls); a **single bar chart** (hourly for Today, daily for ranges; input in lighter shade stacked under output; peak label at top right; hour labels along the axis; no gridlines); then ranked lists **AGENTS** and **MODELS** (the latter keyed by provider). Each row has an icon, name, sub ("Fake Relay · 1 call"), a thin share bar, tokens (in/out), and a green estimated cost. A footnote names the data file (`~/.config/magpie/usage.jsonl`). Empty state is one centred sentence in a card. Screenshot: `f2-5-usage.png`.
- **Requests** (ledger): a wide table with columns Time, Agent, Requested, Provider·account, Sent, Served, Effort, In, Out, Cache write, Cache read, Cost, Duration, Speed, Status (`LED_COLS`, `A/app.js:15454`). It has a dashboard above it with a metric toggle (Tokens / Cost / Requests / Speed) and a split dimension (Model / Model·provider / Provider / Agent), top 7 plus "Other". Provider filter, paged 100 per page, per-row detail expansion.
- Also: a subscription **Allowances** (quota) section above the stats, shown only for accounts with quotas, a per-gateway-key breakdown, and a provider "balance" check.

## 6. Visual design system (`A/app.css`, `A/theme.js`, `A/fonts.js`)

Vanilla CSS, one stylesheet of custom properties, **no framework, no webfont** (the system font is the whole typography). `theme.js` only sets `data-theme` before first paint, from the saved choice or `?theme=`. Auto-dark uses `prefers-color-scheme`. Switching themes crossfades via the `html.theming` class (`app.css:2680`).

**Tokens, light** (`app.css:2-47`):
```
--bg:#f4f4f6; --card:#fff; --card-2:#f9f9fb; --pill:#f1f1f4; --pill-hover:#e9e9ee;
--seg-track:#e6e6eb; --seg-thumb:var(--card);
--seg-edge:0 1px 2px rgba(0,0,0,.14), 0 0 0 .5px rgba(0,0,0,.08);
--line:#e3e3e8; --line-2:#ececf0;                 /* border, inner divider */
--fg:#1c1c21; --fg-2:#4b4b53; --ctl-fg:#63636b; --muted:#85858d; --faint:#b9b9c2;
--accent:#4f46e5; --accent-soft:#eeedfd; --accent-fg:#fff; --sel:#ecebfb;
--green:#189a58; --green-soft:#e4f6ec; --red:#d64545; --red-soft:#fbe9e9; --amber:#b45309; --amber-soft:#fbf0df;
--shadow:0 16px 44px rgba(20,22,30,.16), 0 2px 8px rgba(20,22,30,.06), 0 0 0 .5px rgba(20,22,30,.1);
--font:-apple-system,BlinkMacSystemFont,"SF Pro Text","Segoe UI",system-ui,sans-serif;
--code:ui-monospace,"SF Mono",Menlo,Consolas,monospace;
--c1..c7 (chart series): #4f46e5 #0ea5e9 #10b981 #f59e0b #ec4899 #8b5cf6 #64748b
--ease:cubic-bezier(.2,.8,.2,1)
```
**Dark**:
```
--bg:#1a1a1e; --card:#232327; --card-2:#1f1f23; --pill:#2d2d33; --pill-hover:#36363d;
--seg-track:rgba(255,255,255,.035); --seg-thumb:rgba(255,255,255,.15);
--line:#333339; --line-2:#2c2c32; --fg:#ededf1; --fg-2:#b9b9c1; --muted:#8b8b94; --faint:#56565f; --ctl-fg:#a1a1aa;
--accent:#a5b4fc; --accent-soft:#2b2c47; --accent-fg:#14142a; --sel:#33345a;
--green:#6fd99b; --red:#f28b82; --amber:#f2b544
--shadow:0 16px 44px rgba(0,0,0,.55), 0 2px 8px rgba(0,0,0,.3), 0 0 0 .5px rgba(255,255,255,.1);
```
Dark accents and series colors are pastel and lighter (not simply inverted); soft fills `--*-soft` come in pairs for badges and states.

**Type scale** (all px, system font): body `13px/1.4`, `-webkit-font-smoothing: antialiased`. Row name 13px weight 500, letter-spacing -.005em. Sub line 11.5px muted. Control text 12 to 12.5px. Chips and badges 11.5, 11 and 9px. Section label 11px, weight 600, `letter-spacing:.05em`, uppercase, muted (`.label`, `app.css:1456`). Table header 10.5px uppercase. Numbers use `font-variant-numeric: tabular-nums`. KPI is a large number, about 20px, with an uppercase caption. Code 11.5px monospace.

**Spacing and radii**: page gutter `padding: 0 12px`; header 46px tall; row `min-height:40px; padding:5px 9px 5px 11px; gap:9px`; list rows separated by `1px solid var(--line-2)`. Radii: **cards and lists 12px**, **dialogs 14px**, inputs 7px, buttons 6px, segmented 7px with 5 to 6px thumb, tiles 8px, chips and pills 999px, chart bars 3px. Section heading rows are 26px tall with margin `14px 0 6px`.

**Components**:
- **Cards/lists**: `.list{background:var(--card);border:1px solid var(--line);border-radius:12px;overflow:hidden}`, flat, hairline border, **no shadow**; rows divided by a lighter hairline. Hover is `--pill` tint. Page bg is cool grey (`#f4f4f6`) with white cards, which is the main source of the "quiet surface" look.
- **Segmented control** (the main control idiom, used for nav, protocol, API, period, theme, language): a `--pill` track with 2px padding and a sliding white **thumb** (`.thumb`, `--seg-thumb` plus `--seg-edge` shadow) animated by JS (`slide()`); inactive labels are muted. The header nav is the same but with no track, only the thumb (see screenshot, `.seg` has no background).
- **Buttons**: `.text` (borderless, 12px, `--ctl-fg`, hover `--pill`), `.text.action` (27px, 1px border, card bg, 1px shadow, outline-like), `.text.primary` (accent fill, white text, weight 500), `.text.danger-fill` for confirmations. Press feedback `scale(.96)`.
- **Toggle**: `.lib-switch` (`role=switch`), small pill with a knob `i`. On state is a dark pill in light mode (see `f2-1`).
- **Inputs** (`app.css:2287-2293`): 27px high, `border:1px solid var(--line)`, radius 7, 12.5px; focus is `border-color: var(--accent)` plus a **3px soft ring** `color-mix(in srgb, var(--accent) 22%, transparent)`. Label column is right-aligned 12px `--fg-2`, with a muted hint line under each field (hints are in every field).
- **Chips** (`.mchip`): 24px high pill, 1px border; picked state is `--accent-soft` bg, accent text, 45% accent border and a leading check; auto-served is a dashed border.
- **Dialog**: `.modal` backdrop `rgba(0,0,0,.28)` plus `blur(8px) saturate(1.1)`; `.dialog` width min(600px,100%), radius 14, ring + big soft shadow (`0 24px 60px rgba(0,0,0,.32)`). Header and footer stay fixed; only the fields scroll. Open and close are animated from JS (grows from the clicked row's rect: `originRect`/`fromRect`, `A/app.js:7259-7420`). Confirms use a native `<dialog class="action-confirm">` with a blurred backdrop, 14px radius and a red fill for destructive confirm.
- **Badges/status**: small tinted pills (`free`, `sponsored`, `Switched off`), green/amber/red dots, a key pill with a fixed width so dots line up down the list.
- **Empty states**: one centred muted sentence inside the same card shell with an action hint ("Add a provider, or sign in…"). No illustrations; first-run Providers is the full preset grid instead.
- **Loading**: skeleton placeholders with a shimmer gradient (`.skeleton`, `skeleton-shift 1.4s`, `app.css:717`); the Gateway page draws its skeleton so layout does not jump.
- **Toasts**: a small bottom-left pill (`.status`, green/red, e.g. "Gateway key created. Use Copy on its row to connect a client.").
- **Motion**: one easing `cubic-bezier(.2,.8,.2,1)` everywhere; durations .12 to .24s; view enter 5px rise + fade; press scale; theme crossfade .35s; list reorder by drag handle; a green `flash` highlight on a changed row; the bird logo has a wing/tail flap animation. `prefers-reduced-motion` is honored in places (view-in becomes fade, spinners off).
- **Brand icons**: real colored logos for every vendor and agent (`A/icons`, `.ic`), with mono ones tinted by `--logo-invert`. This is a large part of why the preset grid looks finished.

**What makes it look polished** (my read):
1. A tight token set with a cool-grey page, white cards and hairline borders, with no heavy shadows except on the overlay layer.
2. One control idiom (the sliding-thumb segmented control), consistent button tiers, and soft-ring focus.
3. Real vendor logos used as the primary identity everywhere (preset grid, rows, chips, usage).
4. Every field carries a one-line plain-language hint; empty states say what to do.
5. Small-type discipline: 11px uppercase section labels, tabular numbers, muted subs.
6. Sliding thumbs, dialog grow-from-row, press feedback and theme crossfade all on one easing curve.
7. Light and dark are both hand-tuned (pastel accent in dark), not auto-derived.
Rough edges: the provider editor is very long (dozens of advanced fields in one scrolling dialog, `f2-3-names.png`), `app.js` is a 20k-line monolith, and the Plugins tab is far busier than the rest.

## 7. Multi-user / admin vs user

**None.** There is one operator. The web page is protected by a single random link key (`?k=` traded for an HttpOnly cookie; `MAGPIE_WEB_KEY` pins it, `web.go:60-170`). No accounts, roles or login screen. "Gateway keys" (`access.Key`: id, name, off, secret, limit, models, accounts) are **caller credentials for API clients**, not UI users; they cannot log into the page. Per-key allowlists and budgets are the only multi-tenant-like feature (README line 211: "a named gateway key for each client, each with its own daily, weekly or monthly token and cost limit").

## Borrowable ideas for portage (short)

- Preset-grid first-run plus "Custom provider": a preset needs only a key; models are fetched after save.
- Model picking as chips: Fetch (unsaved, from the form) then Pick all / none / filter; manual add by id; per-model right-click test with a status dot; a "none picked means the vendor list, shown dashed" semantic.
- Per-model overrides (display name, price, image support, per-model protocol) staged and saved with the provider, with "Restore default" and catalog list price shown as placeholder.
- Gateway page as the single "how to connect" surface: copyable base URL, protocol seg, per-protocol snippet tabs, clicking a model fills the snippet, recent-calls live feed.
- Per-key model allowlist (`provider/*` wildcard, group, account) and per-key token/cost budget with calendar windows.
- Visual: the token set above, the sliding-thumb segmented control, 12px card radius with hairline and no shadow, blur-backdrop dialog with fixed head and foot.
