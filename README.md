# Portage

**Keep the harness. Change the model.**

[![CI](https://github.com/SimonGino/portage/actions/workflows/ci.yml/badge.svg)](https://github.com/SimonGino/portage/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Single binary](https://img.shields.io/badge/deploy-single%20binary-1B365D)](#quick-start)

[English](README.md) · [简体中文](README.zh-CN.md)

Portage is a self-hosted model gateway: put every model you can reach — official APIs,
OpenAI-compatible relays, Ollama / vLLM on your own machine — behind one address, and run
any of them inside agent harnesses like Claude Code and Codex CLI.

It works by translating protocols. Each harness speaks exactly one (Claude Code speaks
Anthropic Messages, Codex CLI speaks OpenAI Responses, while most models only expose
OpenAI Chat Completions). Portage translates between all three in the middle, and passes
bytes through untouched when the protocols already match. Point the harness at Portage
and switching models is one line of config — no patched clients, no forked harness, no
wrapper scripts.

```mermaid
flowchart LR
    subgraph clients["The harness you already use"]
        CC["Claude Code<br/>Anthropic Messages"]
        CX["Codex CLI<br/>OpenAI Responses"]
        APP["Your scripts / SDK<br/>Chat Completions"]
    end

    PG{{"Portage<br/>one binary · SQLite · optional web admin"}}

    subgraph up["Whatever model you can get"]
        T["Open-weight models<br/>via any OpenAI-compatible relay"]
        L["Your own hardware<br/>Ollama · vLLM · MLX"]
        A["Anthropic · OpenAI<br/>native"]
    end

    CC --> PG
    CX --> PG
    APP --> PG
    PG -- "different protocol → translate" --> T
    PG --> L
    PG -- "same protocol → byte passthrough" --> A
```

## What you can run

| The model you want | Reached over | In the harness |
| --- | --- | --- |
| **Open-weight models** — whatever the good one is this month | any OpenAI-compatible endpoint | Claude Code · Codex CLI |
| **Your own hardware** — Ollama, vLLM, LM Studio, MLX | Chat Completions on localhost | Claude Code · Codex CLI |
| **The relay or aggregator you already pay for** | Chat Completions or Responses | Claude Code · Codex CLI |
| **Your employer's internal deployment** | whichever of the three it exposes | Claude Code · Codex CLI |
| **Anthropic and OpenAI themselves** | their native protocol, bytes untouched | Claude Code · Codex CLI |

Crossing the two big vendors — Claude models inside Codex CLI, GPT models inside Claude
Code — falls out of the same machinery. It's a consequence, not the point.

Streaming (SSE) and tool calling work on every one of those paths. For an agent harness
those aren't features, they're the floor — a translation path without them is a
translation path that doesn't work.

## And while it's in the path

Once every request goes through one place, a few things become free:

| | |
| --- | --- |
| **Swap vendors without touching clients** | An *access point* is a stable public model name bound to real upstreams. Move it, and every harness follows. |
| **Stop handing out real vendor keys** | Portage issues its own `sk-ptg-…` keys. Upstream credentials never leave the server, never appear in a log or an error body. |
| **Survive a dead key at 3am** | Each channel holds a pool of credentials. A `401` pulls one out of rotation and the request continues on the next. |
| **Know where the tokens went** | One row per call — model, channel, the credential that actually served it, tokens including cache reads and writes, status, latency. |

> *portage (n.):* carrying a boat overland between two waterways that don't connect.
> **Carrying costs something — if you can float, don't land.** That's the name and the
> hard rule: when the two sides already speak the same protocol, raw bytes go straight
> through and nothing is transcoded. Translate only when you must, and say in the logs
> what got left on the bank.

## Quick start

The same binary runs in two shapes, and one thing decides which: whether an admin
password is set.

| | Admin password | Business configuration | What it's for |
| --- | --- | --- | --- |
| **With the console** | set | lives in the database, edited by clicking | the machine you configure *on* |
| **Forwarding only** | not set anywhere | comes from a declarative file | the machine you deploy *to* |

The intended path uses both, in three steps: **configure locally with the console →
export one `channels.yaml` → deploy that file to a forwarding-only instance.**

### 1. Configure locally, with the console

```bash
PORTAGE_ADMIN_PASSWORD='pick-a-password' \
  docker compose -f deploy/docker-compose.yml up -d --build
```

Open <http://127.0.0.1:8317/admin>, log in, and add a channel → managed models →
an access point → an API key. Test the upstreams from here: a forwarding-only instance
never probes anything, so whatever you don't verify on this machine, nobody verifies.

### 2. Export `channels.yaml`

One button at the foot of the console's left rail: the whole business configuration in
a single file, and nothing about runtime state. **The file carries secrets in
cleartext**; it lands `0600`, and it never gets committed — `channels.yaml` is already
in `.gitignore`.

### 3. Deploy it, forwarding-only

One directory and three files on the deployment machine, image pulled from GHCR, no
repo checkout needed:

```text
portage/
├── docker-compose.forward.yml   ← copied from deploy/
├── config.yaml                  ← start from deploy/config.example.yaml (global rate limit lives here)
└── channels.yaml                ← the export from step 2
```

```bash
mkdir -p data && sudo chown 65532:65532 data
docker compose -f docker-compose.forward.yml up -d
```

With a file mounted, that file is the only source of truth for business configuration;
anything statically wrong with it fails the boot with exit code 1 and reports every
problem at once.

The full story of each step — the empty-database warning, what the export does and
doesn't contain, failure modes and the restart loop, changing configuration later,
writing `channels.yaml` by hand, building from source, public exposure — is in the
**[deployment guide](docs/deploy.md)**.

## Claude Code

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8317
export ANTHROPIC_AUTH_TOKEN=sk-ptg-…    # your Portage key — never a vendor key
export ANTHROPIC_MODEL=gw-sonnet        # an access point name, or channel/model
claude
```

That's the whole setup. Claude Code keeps sending Anthropic Messages; Portage decides per
channel whether that goes out untouched or gets translated into Chat Completions or
Responses first. Keys are accepted as either `x-api-key` or `Authorization: Bearer`,
so `ANTHROPIC_API_KEY` works just as well as `ANTHROPIC_AUTH_TOKEN`.

Claude Code also fires background requests (titles, summaries) at a small fast model —
`ANTHROPIC_SMALL_FAST_MODEL` on older versions, `ANTHROPIC_DEFAULT_HAIKU_MODEL` on newer
ones. Point it at an access point too, or those requests go looking for a model name your
upstream may not have.

## Codex CLI

Configure Portage as a custom provider in `~/.codex/config.toml`:

```toml
model_provider = "portage"

[model_providers.portage]
name = "Portage"
base_url = "http://127.0.0.1:8317/v1"
wire_api = "responses"          # the gateway decides whether to translate downstream
env_key = "PORTAGE_API_KEY"     # your sk-ptg-… key, never an upstream key

[profiles.sonnet]
model = "gw-sonnet"
model_provider = "portage"
model_context_window = 200000   # must match the real upstream window
```

**You have to set `model_context_window` yourself** — this is the one thing the gateway
cannot do for you. Codex doesn't read `/v1/models`; it trusts its own built-in model
catalog. Give an access point a custom name like `gw-sonnet` and Codex falls back to a
272k window, putting auto-compaction around 245k. If the real upstream window is smaller,
the request hits the upstream's `400` long before compaction ever fires.

<details>
<summary>How remote compaction is handled (read this if you use long sessions)</summary>

When Codex decides to compact, it sends a request whose input tail carries a
`compaction_trigger` and expects **exactly one** compaction item back. Zero items is a
fatal, non-retryable error on the client. Portage handles the two cases differently:

- **Responses passthrough** — only allowed on channels whose upstream actually understands
  the trigger; tick *Codex compaction: supported* on the channel. Otherwise the request is
  refused with an explanatory `400`, rather than forwarded to an upstream that will
  silently ignore the field and return nothing. Responses-shaped wire ≠ compaction support.
- **Translated paths** (Responses → Anthropic / Chat Completions) — synthesized locally.
  Portage rewrites the compaction turn into a pure summarization request, wraps the
  summary in its own envelope (`ptg1:` + base64) as exactly one compaction item, and
  unwraps it when Codex plays it back on the next turn. The channel checkbox doesn't
  apply here.

The legacy `POST /v1/responses/compact` is not implemented and returns `501`.
</details>

## The admin console

Five screens, each answering one operational question: **Models · API Keys · Call Log ·
Access Points · Rankings**. Hierarchy comes from typography, color carries status and one
accent, nothing decorative. Upstream credentials are displayable and copyable in exactly
one place — the credential pool — and appear in no list, no log, and no error message.
The left rail also holds the export button that produces `channels.yaml`.

**On a forwarding-only instance none of this exists.** With no admin password set,
`/admin` and `/admin/api/*` — login and session included — are never registered: the
404 comes from the router, not from an auth check. There is no login form to brute-force
and no admin surface to accidentally leave exposed. `/v1` and `/healthz` are all that's
listening.

<!-- Screenshots: drop sanitized PNGs into docs/images/ and uncomment.
     See docs/images/README.md for what to capture.

| Models | Call log | Rankings |
| --- | --- | --- |
| ![Models](docs/images/admin-models.png) | ![Call log](docs/images/admin-logs.png) | ![Rankings](docs/images/admin-rankings.png) |
-->

Full visual and interaction spec in [`DESIGN.md`](DESIGN.md).

## How it's put together

A **channel** is one upstream account: a `base_url`, the protocols it can speak, the
models you've declared on it, and a pool of credentials. An **access point** is the model
name your harness asks for, bound to candidates drawn from those channels — or skip it and
address a managed model directly as `channel/model`.

- **A retry ladder that knows when to stop.** Backoff within a credential (2 attempts by
  default), then the next credential in the channel, capped by a global attempt budget. A
  `401` pulls a credential out of rotation until you restore it; a `403` moves on without
  pulling it — that usually just means this key isn't entitled to this model. Once the
  first byte of a stream is out, nothing is retried: the first byte is a promise.
- **Numbers that come from the upstream, not from a guess.** On translated paths usage is
  read off the canonical stream; on passthrough a read-only tap extracts it without
  touching a byte of what the client receives.
- **One binary.** React admin embedded into the Go binary, SQLite for storage. No Redis,
  no sidecar, no external dependency of any kind.

## Protocol matrix

The diagonal is byte passthrough. All six cross-protocol paths are open.

| Inbound ＼ Upstream | Chat Completions | Responses | Anthropic |
| --- | --- | --- | --- |
| **Chat Completions** | passthrough | ✅ | ✅ |
| **Responses** | ✅ | passthrough | ✅ |
| **Anthropic Messages** | ✅ | ✅ | passthrough |

Endpoints: `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`,
`/v1/responses`, plus `/v1/models` (the intersection of access points and protocols) and
an unauthenticated `/healthz`.

The one remaining `501` is `/v1/messages/count_tokens` on a non-Anthropic channel: the
other two protocols have no equivalent endpoint. That's *impossible*, not *unfinished*.

Translation is hub-and-spoke, not point-to-point: each protocol implements one `Codec`,
and `A→B` means *A decodes to canonical, B encodes out*. There is no N×N mesh of pairwise
converters. Every path gets golden samples — real harness requests and SSE transcripts —
recorded before the implementation is written.

## Configuration

Two files, answering different questions: `config.yaml` covers startup only (listen
address, database path, admin password seeding, retry parameters, global rate limit) and
the whole file is optional; everything operational — channels, managed models, access
points, candidates, credentials, API keys — has one source of truth, which is the
database (edited by the console) when no declarative file is mounted, and the
`channels.yaml` file (applied into the database at boot) when one is. The full rules are
in the [deployment guide](docs/deploy.md#configuration-files).

## Deliberately not doing

Multi-tenancy, billing, top-up codes, redemption, notifications, evals, Elasticsearch
logging, anything training-related. No "user tier × channel group" matrix — weighted
routing is routing, not commerce. No image generation or audio endpoints in v1.

**No stateful Responses.** A request carrying `previous_response_id` gets an explicit
`400` rather than having the field quietly dropped — silently dropping it means the client
believes the history is still there while every turn is actually a fresh one, and the
degradation is invisible. Translated paths always refuse; same-protocol passthrough
follows a per-channel *supports stateful chaining* flag, on by default.

**No capability probing.** Whether a model supports tool calling or thinking can't be
probed accurately, can't be falsified, and has no consumer — the gateway never blocks a
request on capability. When that information is genuinely needed, it gets learned from the
call log instead. Observed behaviour doesn't expire; a self-reported capability does.

## Status

Shipped: passthrough core, API keys and usage logging, all six translation paths with
in-credential backoff, the admin console and credential pools, declarative
`channels.yaml` with its export button and the forwarding-only shape.

In progress: weighted split across multiple candidates and candidate-level failover. The
semantics are settled; until the implementation lands, configuration is gated to a single
candidate per access point.

Work in flight lives in [Issues](https://github.com/SimonGino/portage/issues).

## Documentation

The design documents are written in Chinese and are the source of truth for the project.

| Document | Layer | Contents |
| --- | --- | --- |
| [docs/deploy.md](docs/deploy.md) | Operations | The two shapes, the three-step workflow, hand-written path, public exposure, config files |
| [docs/口径层设计.md](docs/口径层设计.md) | Requirements | Scope, the protocol matrix, boundaries and non-goals, decision log |
| [docs/MVP设计草案.md](docs/MVP设计草案.md) | Implementation | Modules, canonical event model, codec interface, data model, golden tests |
| [CONTEXT.md](CONTEXT.md) | Glossary | Domain vocabulary (access point / candidate / channel / credential pool / API key) |
| [DESIGN.md](DESIGN.md) | Design spec | Admin console visual and interaction rules |

## Credits

Written from scratch, no forked code. Protocol details, field semantics, and the sharp
edges between them were checked against these projects:

- [new-api](https://github.com/QuantumNous/new-api) — Go gateway forwarding core, SSE
  streaming, key auth and channel routing.
- [sub2api](https://github.com/Wei-Shaw/sub2api) — primary reference for protocol
  translation in Go; its `apicompat/` covers every direction Portage needs.
  **LGPL-3.0: implementation ideas and field semantics were referenced, no code copied.**
- [litellm](https://github.com/BerriAI/litellm) — the most complete field-mapping
  reference; thinking, tool calling, and usage semantics were cross-checked per provider.
- [opencodex](https://github.com/lidge-jun/opencodex) — primary reference for Codex CLI
  client behaviour: auto-compaction, reasoning replay, the Responses SSE event line.
- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) — primary reference on one
  topic only: cross-protocol fidelity of thinking/reasoning and how to handle signatures.
