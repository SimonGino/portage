# Deployment

[English](deploy.md) · [简体中文](deploy.zh-CN.md)

The same binary runs in two shapes. This document covers all of it in one place: how the
shapes are decided, the three-step intended path, the secondary hand-written path,
building from source, public exposure, and what each of the two config files is for.
Pointing clients at the gateway (Claude Code / Codex CLI) is not deployment — see the
[README](../README.md#claude-code).

## The two shapes

One thing decides which shape you get: whether an admin password is set.

| | Admin password | Business configuration | What it's for |
| --- | --- | --- | --- |
| **With the console** | set | lives in the database, edited by clicking | the machine you configure *on* |
| **Forwarding only** | not set anywhere | comes from a declarative file | the machine you deploy *to* |

The password's **value is only used for seeding; its presence decides the shape**. With no
admin password set, `/panel` and `/panel/api/*` — login and session included — are never
registered: the 404 comes from the router, not from an auth check. There is no login form
to brute-force and no admin surface to accidentally leave exposed. `/v1` and `/healthz`
are all that's listening.

The intended path uses both shapes, in three steps: **configure locally with the console →
export one `channels.yaml` → deploy that file to a forwarding-only instance.**

## 1. Configure locally, with the console

```bash
PORTAGE_ADMIN_PASSWORD='pick-a-password' \
  docker compose -f deploy/docker-compose.yml up -d --build
```

Open <http://127.0.0.1:8317/panel>, log in, and add a channel → managed models →
an access point → an API key. Test the upstreams from here: a forwarding-only instance
never probes anything, so whatever you don't verify on this machine, nobody verifies.

This instance never gets a declarative file mounted — its database stays the source of
truth and the console stays writable. So the database starts empty and the log merely
says so (`api_keys is empty, every forwarded request will 401`) until you've added a
key: a warning, not a failure, because on this shape configuring a key requires a
gateway that's already running. Mount a file and that same condition refuses to boot
instead — see step 3.

The admin password comes from the environment, not the config file — anything baked
into a config file inside an image is baked into that image's layer history. It seeds
the database on first boot; once a password is stored, changing the variable does nothing.

The command above is for a machine with a repo checkout (it builds locally). To run the
console shape on a machine without one, use
[`deploy/docker-compose.admin.yml`](../deploy/docker-compose.admin.yml) instead: it pulls
the GHCR image; copy it and a `config.yaml` (start from `deploy/config.example.yaml`)
into a deployment directory, password via the same environment variable — usage is in
the file's header comment.

## 2. Export `channels.yaml`

One button at the foot of the console's left rail. It writes the whole business
configuration into a single file — every channel, managed model, credential, access
point and API key — and nothing about runtime state, so a `401` you hit locally doesn't
travel to the deployed machine as a disabled credential.

**The file carries secrets in cleartext**, both upstream credentials and `sk-ptg-…`
keys, because a redacted file can't be deployed and deploying it is the entire point.
It lands `0600`; keep it that way, and never commit it — `channels.yaml` is already in
`.gitignore`. The one file of this shape that *is* in git is `channels.example.yaml`,
and that one is a reference, not a config.

## 3. Deploy it, forwarding-only

There is nothing to build on the deployment machine: CI publishes a multi-arch
(amd64/arm64) image to GHCR on every `v*` tag, public, no login needed. `latest`
points at the newest release, and version tags like `1.2.3` pin an exact release.
For servers in mainland China where GHCR is unreliable, the same build is also pushed
to Alibaba Cloud ACR with an identical tag set — point `PORTAGE_IMAGE` at
`crpi-02g5kpg27o6b5u8n.cn-hangzhou.personal.cr.aliyuncs.com/simongino/portage`.

The deployment machine needs one directory and three files, all from the repo or from
step 2 — no checkout required:

```text
portage/                         ← deployment directory, any name
├── docker-compose.forward.yml   ← copied from deploy/
├── config.yaml                  ← start from deploy/config.example.yaml (global rate limit lives here)
└── channels.yaml                ← the export from step 2
```

```bash
mkdir -p data && sudo chown 65532:65532 data   # required on Linux: the database lands here; owner must be the container uid
docker compose -f docker-compose.forward.yml up -d
```

The `chown 65532` is needed on **Linux** servers only — the container runs as uid 65532
and bind mounts pass host permissions straight through; get the owner wrong and boot
fails with `unable to open database file (14)`: a permission problem, not a corrupt
database. Docker Desktop on macOS does its own permission mapping for bind mounts, so
the step can be skipped there. The database lives at `./data/gateway.db`; backing up is
copying that directory.

**Switching the image source and pinning a version** both go through the
`PORTAGE_IMAGE` environment variable — no compose edits. The default is GHCR `latest`;
for a server in mainland China, or to pin an exact release:

```bash
# domestic mirror + pinned version (recommended)
PORTAGE_IMAGE=crpi-02g5kpg27o6b5u8n.cn-hangzhou.personal.cr.aliyuncs.com/simongino/portage:0.1.0 \
  docker compose -f docker-compose.forward.yml up -d

# tired of the prefix? put it in a .env file next to the compose file (compose reads it automatically):
# PORTAGE_IMAGE=crpi-02g5kpg27o6b5u8n.cn-hangzhou.personal.cr.aliyuncs.com/simongino/portage:0.1.0
```

The compose file already points `PORTAGE_CHANNELS` at the mounted `channels.yaml` and
sets no admin password anywhere — which is exactly the forwarding-only shape. Outside a
container it's the `-channels` flag; `PORTAGE_CHANNELS` overrides it. The environment
variable has to exist because the image's `ENTRYPOINT` hard-codes `-config`. There is no
implicit default path and no search — a mistyped filename must fail loudly, not degrade
into silently serving whatever the database happened to hold.

With a file mounted, that file is the only source of truth for business configuration:
it's applied into the database at boot, entities missing from it are deleted, and with
no admin password there is no console left to write any of it. Anything statically
wrong — a candidate pointing at a channel that doesn't exist, an unknown field, an empty
`api_keys` list — fails the boot with exit code 1 and reports **every** problem at once
rather than stopping at the first, because the only way to fix one is edit-and-restart.
In a container that's a restart loop, and that's the point: the alternative is exiting 0
and disappearing quietly.

## Changing something later

Change it on the local instance, export again, redeploy the file, restart. The two
machines share nothing and there is no file watching — the declarative file is read once,
at boot.

## Upgrade notes

**The panel prefix moved from `/admin` to `/panel`** (#76). GETs on the old prefix 302
to the same path under the new one, so bookmarks and verification / reset links in old
emails keep working. **The one thing the redirect cannot save is the OAuth callback**:
GitHub / Google only redirect to the exact URI registered in their consoles. If you
configured OAuth login, update the callback URL in both consoles after upgrading, from
`…/admin/oauth/<provider>/callback` to `…/panel/oauth/<provider>/callback` — the panel's
"用户 → 登录与邮件" card shows the new URL ready to copy. Until you do, GitHub / Google
logins are rejected at the provider's step.

## Writing `channels.yaml` by hand (the secondary path)

[`channels.example.yaml`](../channels.example.yaml) is real exporter output run over fake
data, pinned by a round-trip test (`export → apply → export`, byte-identical), which is
why it can be trusted as a field reference — a hand-maintained sample goes stale the
first time a field is renamed and nothing notices.

It is **not** a config you can run. The API key in it is the factory placeholder, and a
placeholder API key is refused at boot: it's a door key published in a public repo.
Unknown fields are refused too. Exported files never contain either, so the strict
parser costs the main path nothing and only bites here — at boot, on the machine you're
still standing next to.

## Build from source (Go 1.26+ and Node)

```bash
make build          # builds the web admin and embeds it into bin/portage
./bin/portage
```

A plain `go build ./cmd/portage` works too — without the `webui` build tag, `/panel`
serves a "frontend not built" page. That's the path CI and Node-less machines take.
Note this is a build-time switch over the bundled assets only; whether the admin plane
exists at all is the password question above, not this tag.

## Putting it on the public internet

Publish the container port to localhost only and terminate TLS in nginx in front of it,
exposing just `/v1`. See [`deploy/nginx.conf.example`](../deploy/nginx.conf.example).
**Several nginx defaults are actively hostile to SSE** and must be overridden — get it
wrong and nothing errors, streams just hang or cut off.

## Configuration files

Two files, answering different questions.

`config.yaml` covers startup only: listen address, database path, admin password seeding,
retry parameters, a global token bucket (10 QPS / burst 20 out of the box, applied to
the forwarding plane only), and the call-log retention window. The whole file is
optional; every key has a default, and it's parsed leniently so an old deployment
carrying a since-removed key still boots. In a container the image bakes in a default
copy ([`deploy/config.example.yaml`](../deploy/config.example.yaml)); to change anything,
start from that file, save it as `config.yaml`, and mount it over the baked one — the
forward/admin compose files already declare that mount.

Everything operational — channels, managed models, access points, candidates,
credentials, API keys — has one source of truth, and *which* one depends on whether a
declarative file is mounted:

- **No declarative file** — the database is the source of truth and the console edits it.
  None of it lives in a file, and there's nothing to redeploy.
- **`channels.yaml` mounted** via `-channels` or `PORTAGE_CHANNELS` — the file is the
  source of truth. It's applied into the database at boot, entities absent from it are
  deleted, unknown fields are refused, and the console (where one exists at all) is
  read-only for business configuration.

Either way the database is what actually gets read per request — there is no
configuration cache anywhere in the process. What the file never describes is runtime
state: which credential a `401` pulled out of rotation, when, and why. That's the
gateway's to write, not yours to declare.
