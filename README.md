# Avagenc Chat

[![Go 1.25](https://img.shields.io/badge/go-1.25-00ADD8.svg)](https://go.dev/doc/devel/release#go1.25)
[![Deploy: Cloud Run](https://img.shields.io/badge/deploy-Cloud%20Run-4285F4.svg)](.github/workflows/deploy.yaml)

The backend of Avagenc Chat: a **group chat whose other members are AI agents**. One
human and a roster of agents share a single conversation thread. Every agent runs
in-process, in one Go binary, on one shared transcript.

```
                       ┌──────────────────── one Zep thread: chat-{userID} ───────────────────┐
                       │                                                                      │
POST /ava    ─────────►│  ava      ─┬─ tool: postera (schedule a future self-message)          │
POST /ava/voice ──────►│  (orchestr)│                                                          │
POST /ava/awaken ─────►│            └─ tool: zee │ rafal │ yori  ──► specialist runner ────────┤
                       │                                                                      │
POST /zee    ─────────►│  zee    — Tuya smart home                                             │
POST /rafal  ─────────►│  rafal  — Google Workspace (Calendar, Gmail, Contacts)                │
POST /yori   ─────────►│  yori   — Spotify                                                     │
                       │                                                                      │
                       └──────────────────────────────────────────────────────────────────────┘
                            every turn — human's, Ava's, a specialist's — is appended here,
                            visible to the human and readable by every agent on its next turn
```

Callers are the Avagenc Chat web SPA (Firebase-authenticated), a third-party voice
client (API key), and Google Cloud Tasks (API key), which calls back when an agent
asked to be woken at a future moment. The design is optimized for one usage pattern:
**a long-lived, single conversation per user, into which any number of agents write.**
Nearly every decision below points back to that sentence.

---

## Contents

- [Why this shape](#why-this-shape)
- [Design principles](#design-principles)
- [The surface at a glance](#the-surface-at-a-glance)
- [Module map](#module-map)
- [Run it](#run-it)
- [Modular monolith: what is actually enforced](#modular-monolith-what-is-actually-enforced)
- [Layers and dependency direction](#layers-and-dependency-direction)
- [The agent architecture](#the-agent-architecture)
- [Postera: prospective memory](#postera-prospective-memory)
- [The memory triad](#the-memory-triad)
- [The wallet](#the-wallet)
- [Trust boundaries](#trust-boundaries)
- [The module family](#the-module-family)
- [Infrastructure](#infrastructure)
- [Design decisions & trade-offs](#design-decisions--trade-offs)
- [Non-goals](#non-goals)
- [External constraints](#external-constraints)
- [Verification](#verification)
- [Layout](#layout)
- [Reading the source](#reading-the-source)
- [License](#license)

## Why this shape

Most multi-agent backends model a request as a **pipeline**: a router picks an agent,
that agent produces an answer, the answer is returned, the intermediate state is
discarded. Agents get private contexts; a supervisor stitches their outputs together.

This one models it as a **room**. There is exactly one transcript per user, and every
participant — the human, the orchestrator, each specialist — writes into it and reads
from it. That single decision is what the rest of the codebase is arranged around, and
it produces properties a pipeline cannot:

- **No context marshalling.** When Ava delegates to `zee`, she does not restate the
  human's request. `zee` reads the last few messages of the same thread and sees the
  original wording, verbatim, along with everything else that just happened. The
  delegation payload is literally the string `"@zee"` — see
  [`internal/agent/ava/instruction.txt`](internal/agent/ava/instruction.txt).
- **The transcript is the state.** There is no orchestration state machine to keep in
  sync with the conversation, because the conversation *is* the state. A run is a pure
  function of (thread, trigger).
- **Silence is a valid turn.** If a specialist has already answered the human on the
  shared thread, the orchestrator's correct final response is *empty* — repeating the
  answer would be a second copy of a message the human already sees. Handlers therefore
  treat an empty final response as success, not as failure
  ([`internal/agent/specialist/handler.go:83`](internal/agent/specialist/handler.go)).

The cost is that agents must be *instructed* about the room — that they are being read
by others, that a bare `@name` is a real request, that echoing is noise. That
instruction is not prompt-tinkering scattered through the code; it is a four-layer
composition described in [The agent architecture](#the-agent-architecture).

The second shaping insight is structural. A system with seven independent domains
(agents, identity, linking, three kinds of memory, money) is usually the moment a team
reaches for microservices. The coupling that actually hurts, though, is **compile-time
dependency direction**, not process boundaries — and that can be enforced inside one
binary, for free, with none of the operational cost. Hence: a
[modular monolith](#modular-monolith-what-is-actually-enforced) whose boundaries are
checkable by `go list`, not by convention.

## Design principles

Rules that hold across the codebase. Decisions later cite them by name.

- **Explicit over clever.** Every dependency is constructed by name in
  [`cmd/http/main.go`](cmd/http/main.go) and passed as a parameter. There is no
  container, no reflection-based injection, no `Build()` that assembles a hidden graph.
  A reader learns the entire system's wiring by reading one file top to bottom.
- **Consumer-defined ports.** An interface is declared by the package that *needs* it,
  sized to what that package uses, and implemented by an adapter in a sibling
  subpackage — the [Go proverb](https://go-proverbs.github.io/) "accept interfaces,
  return structs", applied at the package level. A feature package never imports its
  own adapter.
- **No abstraction without a second implementation or a real seam.** `Handler` types
  are HTTP glue and nothing else. A `Service` exists only where there is genuine
  business logic to hold (`session` has an ownership check; `postera` has none, so it
  has no service).
- **Sentinel errors per feature, translated at the adapter.** Backends speak their own
  error vocabulary; adapters map it to the feature's sentinels, and consumers match
  with `errors.Is`. No package outside `internal/session/zep` knows Zep has a
  `*zep.NotFoundError`.
- **Fail fast at boot, never at runtime.** Missing configuration is `log.Fatal` during
  startup, not a nil-pointer panic on the first request. The wallet adapter validates
  its schema at construction so a skipped migration fails the deploy, not the
  hundredth charge.
- **Money is integers.** All wallet amounts are `int64` micro-rupiah. There is no
  `float64` anywhere on a value path.

## The surface at a glance

Every route is registered individually in `main.go`; there is no `/{agent}` dispatcher
(see [decision](#one-route-per-agent-not-agent)).

| Endpoint | Purpose | Auth |
| --- | --- | --- |
| `GET /` | health / build info | none |
| `POST /ava` | chat with the orchestrator | Firebase ID token + balance > 0 |
| `POST /ava/voice` | same, in the voice channel (TTS-shaped replies) | `api-key` (third party) + `user-id` header + balance |
| `POST /ava/awaken` | Cloud Tasks callback: Ava's own scheduled message arrives | `api-key` (postera) + `user-id` header + balance |
| `POST /zee` | Tuya smart-home specialist, addressed directly | Firebase + balance |
| `POST /rafal` | Google Workspace specialist | Firebase + balance |
| `POST /yori` | Spotify specialist | Firebase + balance |
| `GET /wallet` | balance, in rupiah and authoritative micros | Firebase |
| `GET /wallet/usage/today` | today's tokens and cost, in the caller's timezone | Firebase + `time-zone` header |
| `GET \| DELETE /sessions/messages` | read / clear episodic memory | Firebase |
| `GET \| DELETE /knowledge` | read / wipe the semantic knowledge graph | Firebase |
| `GET /postera` | upcoming self-scheduled messages | Firebase |
| `DELETE /postera/{posterumID}` | cancel one | Firebase |
| `GET /{gworkspace,spotify}/auth-url` | mint an OAuth consent URL | Firebase |
| `GET \| POST \| DELETE /{gworkspace,spotify}/connection` | link status / connect / disconnect | Firebase |
| `GET /tuya/connection` | link status (Tuya is onboarded manually) | Firebase |
| `OPTIONS /` | CORS preflight | none |

Agent routes additionally require a `time-zone` header (IANA name). Every `DELETE`
answers `204`. Errors are a uniform `{"detail": "…"}` JSON body written by
`apihttp.WriteProblem` — problem-*shaped*, but served as `application/json`, not the
`application/problem+json` media type of
[RFC 9457](https://www.rfc-editor.org/rfc/rfc9457). Clients should key on the status
code and `detail`, not on the content type.

Parked, wired but commented out in `main.go` pending a product decision:
`POST /wallet/topup` and `POST /wallet/topup/notification`
(see [The wallet](#the-wallet)).

## Module map

| Module | Owns | Port it defines | Adapter |
| --- | --- | --- | --- |
| `internal/agent` | the three ADK state-delta keys, and the base instruction template every agent composes into | — | — |
| `internal/agent/ava` | the orchestrator's entry points and its sub-agent adapter | `ava.SubAgent` (satisfied here) | — |
| `internal/agent/specialist` | the specialist entry point, shared by all three | — | — |
| `internal/identity` | who the caller is | — | Firebase Admin SDK, API key |
| `internal/session` | **episodic** memory: the thread and its messages | `session.Store` | `session/zep` |
| `internal/knowledge` | **semantic** memory: the user's knowledge graph | `knowledge.Store` | `knowledge/zep` |
| `internal/postera` | **prospective** memory: future self-messages | — (the SDK is the orchestrator) | — |
| `internal/wallet` | double-entry ledger, per-run billing, balance gate | `wallet.Ledger` | `wallet/postgres`, `wallet/midtrans` |
| `internal/link` | HMAC-signed OAuth state, shared by real integrations | — | — |
| `internal/link/{gworkspace,spotify,tuya}` | one account-linking flow each | `Connector` (per slice) | the vendor SDK client |

## Run it

Go 1.25+. Every variable below is fatal-if-missing at boot; there are no silent
defaults except `PORT`.

```bash
cp .env.example .env      # then fill it in
go run ./cmd/http
```

<details>
<summary>Environment variables (all required unless noted)</summary>

| Variable | What it is |
| --- | --- |
| `APP_ENV` | `production` or `development`; reported by `GET /` |
| `PORT` | *optional*, defaults to `8080` |
| `HOST_URL` | this service's public base URL; Cloud Tasks calls back to `HOST_URL/ava/awaken` |
| `CORS_ALLOWED_ORIGIN` | exactly **one** origin, matched literally |
| `WEB_APP_URL` | SPA origin; every OAuth redirect URI is derived from it |
| `FIREBASE_PROJECT_ID` | verifies caller ID tokens |
| `GOOGLE_APPLICATION_CREDENTIALS` | *optional on GCP*; a service-account key path for local runs, unset in Cloud Run where ADC applies |
| `GCP_PROJECT_ID` | Cloud Tasks and Firestore project |
| `CLOUD_TASKS_LOCATION_ID`, `CLOUD_TASKS_QUEUE_ID` | queue that delivers self-awaken callbacks |
| `FIRESTORE_DATABASE_ID` | Firestore database holding `tuya_accounts`, `gworkspace_tokens`, `spotify_tokens` |
| `ZEP_API_KEY` | thread + knowledge-graph backend |
| `GEMINI_API_KEY` | model roster; the model ID (`gemini-3.5-flash`) is pinned in `main.go`, not env |
| `TUYA_ACCESS_ID`, `TUYA_ACCESS_SECRET`, `TUYA_BASE_URL` | zee |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | rafal + Workspace linking |
| `SPOTIFY_CLIENT_ID`, `SPOTIFY_CLIENT_SECRET` | yori + Spotify linking |
| `OAUTH_STATE_SECRET` | HMAC key for OAuth state; one secret for all integrations |
| `POSTERA_DB_URL` | Postgres for prospective memory |
| `POSTERA_API_KEY` | shared secret Cloud Tasks presents at `/ava/awaken` |
| `THIRD_PARTY_API_KEY` | shared secret for `/ava/voice` |
| `WALLET_DB_URL` | Postgres for the wallet — a **separate database**, migrated by the deploy pipeline |
| `MIDTRANS_SERVER_KEY`, `MIDTRANS_BASE_URL` | *parked* — only needed if top-up is re-enabled |

</details>

A full round trip, including the failure paths a client must handle:

```bash
# Talk to Ava. The thread is derived from the token — there is no session ID to pass.
curl -sS https://<host>/ava \
  -H "Authorization: Bearer $FIREBASE_ID_TOKEN" \
  -H "time-zone: Asia/Jakarta" \
  -H "content-type: application/json" \
  -d '{"message":"nyalain lampu kamar dong"}'

# 200 — Ava delegated to zee, and zee already answered on the shared thread,
#       so Ava's own final turn is deliberately empty.
{"response":""}

# 402 — balance exhausted. Every agent route can return this; the UI must route to top-up.
{"detail":"insufficient balance"}

# 401 — missing/expired ID token.       400 — no time-zone header.
# 502 — the run produced no final response, or the model/tooling failed mid-run.
```

The wallet is charged either way: a run that burned tokens and *then* failed is still
billed, because the tokens were really spent
([`biller.go:96`](internal/wallet/biller.go)).

## Modular monolith: what is actually enforced

"Modular monolith" is easy to claim and easy to violate. Here it means one testable
property: **the internal import graph is a forest of independent trees that meet only
at `main`.** That is checkable, so here it is, generated from the source:

```
cmd/http                        -> agent  agent/ava  agent/specialist  identity
                                   knowledge  knowledge/zep  link/gworkspace
                                   link/spotify  link/tuya  postera  session
                                   session/zep  wallet  wallet/postgres

internal/agent                  -> (nothing)
internal/identity               -> (nothing)
internal/knowledge              -> (nothing)
internal/link                   -> (nothing)
internal/postera                -> (nothing)
internal/session                -> (nothing)
internal/wallet                 -> (nothing)

internal/agent/ava              -> agent  wallet
internal/agent/specialist       -> agent  wallet
internal/knowledge/zep          -> knowledge
internal/session/zep            -> session
internal/wallet/postgres        -> wallet
internal/wallet/midtrans        -> wallet
internal/link/gworkspace        -> link
internal/link/spotify           -> link
internal/link/tuya              -> (nothing)
```

Read it and the discipline is visible without trusting a word of prose:

- **Seven feature packages import nothing internal at all.** `session` does not know
  `knowledge` exists. `wallet` does not know it is billing agents.
- **Every adapter points at exactly one contract, its own parent.** No adapter imports
  another adapter, and no contract imports its adapter — so swapping Zep for something
  else means writing a sibling package, not editing a service.
- **`ava` and `specialist` do not import each other**, even though Ava runs
  specialists. The adapter that makes a specialist usable as `ava.SubAgent` lives on
  the *consumer's* side, in
  [`internal/agent/ava/subagent.go`](internal/agent/ava/subagent.go), because
  `ava.SubAgent` is Ava's contract; a specialist has no idea Ava exists.
- **The only convergence point is `cmd/http`.** That is the definition of the pattern:
  modules are independent, composition is centralized, deployment is one binary.

**Choice.** One process, module boundaries enforced by the import graph.
**Alternative.** Split the seven domains into services — the reflex when a system has
this many bounded contexts.
**Why.** Splitting buys independent deploy and independent scaling; it costs network
serialization, partial-failure handling, distributed tracing, N pipelines, and N
runtimes to operate. None of those costs buy anything *here*: an orchestration run
calls three specialists that share one thread, so a split would turn in-process
function calls into three network hops on the critical path of a single user request,
while adding failure modes (partial success, retry storms) the current design simply
does not have. Meanwhile the benefit usually attributed to microservices — *you cannot
accidentally reach into another team's internals* — is obtained here by the import
graph, at zero operational cost. **Trade-off:** everything scales together and a
runaway module can starve its neighbours; Cloud Run's `--max-instances=1` makes that
concrete today. That is a deliberate trade for a system whose load is one conversation
per user at a time, and the extraction path is mechanical when it stops being true —
the wallet, for instance, is a `Ledger` interface plus a `postgres` adapter, so it
becomes a service by replacing that adapter with a client.

## Layers and dependency direction

Dependencies point **inward, in one direction, always**. Nothing on an inner ring
imports an outer one.

```
        ┌───────────────────────────────────────────────────────────────┐
        │  composition root — cmd/http/main.go                          │
        │  the only file that names concrete adapters                   │
        └───────────────┬───────────────────────────────────────────────┘
                        │ constructs & injects
        ┌───────────────▼───────────────────────────────────────────────┐
        │  transport — Handler                                          │
        │  parse request · read identity from context · map errors      │
        │  → status codes.  No business logic. No knowledge of storage. │
        └───────────────┬───────────────────────────────────────────────┘
                        │ calls
        ┌───────────────▼───────────────────────────────────────────────┐
        │  domain — Service / Biller / Guard  (only where logic exists) │
        │  ownership checks · pricing · balanced postings · sentinels   │
        └───────────────┬───────────────────────────────────────────────┘
                        │ depends on an interface it declares itself
        ┌───────────────▼───────────────────────────────────────────────┐
        │  port — Store · Ledger · Connector                            │
        │  provider-agnostic; declared beside its consumer              │
        └───────────────▲───────────────────────────────────────────────┘
                        │ implemented by (import points UP, not down)
        ┌───────────────┴───────────────────────────────────────────────┐
        │  adapter — session/zep · knowledge/zep · wallet/postgres      │
        │  translate vendor types & errors into domain types & sentinels│
        └───────────────┬───────────────────────────────────────────────┘
                        │
        ┌───────────────▼───────────────────────────────────────────────┐
        │  infrastructure — Zep · PostgreSQL · Firestore · Cloud Tasks  │
        │  Gemini · Firebase Auth · Tuya · Google · Spotify             │
        └───────────────────────────────────────────────────────────────┘
```

The inversion is the point, and it is enforced by the graph above: `knowledge/zep`
imports `knowledge`, never the reverse. `grep -r getzep` is the quickest audit, and it
returns an honest, deliberately uneven answer: the two `zep` adapters, `main.go` — and
`internal/agent/ava/handler.go`, which names `zep.RoleTypeSystemRole` to record the
self-awaken turn under a system role. **The memory *reads* are behind a port; the agent
runtime is not.** Putting the ADK session service behind an interface would mean
re-declaring ADK's own `SessionService` contract to gain a swap this system has no
second candidate for — abstraction bought on speculation. The coupling is one import in
one file, and it is cheaper to see it than to hide it.

The agent path is a second stack, orthogonal to the first, and it is worth reading as
its own picture because control flows *outward* through it:

```
HTTP request
   │
   ▼
Handler                 ava/handler.go · specialist/handler.go
   │                    attaches speaker + timezone to context, picks the
   │                    instruction layers for this run, drains the event stream
   ▼
runner.Runner           ADK. one per agent, all sharing one SessionService
   │
   ├─────► SessionService  (adkzep) ── reads/writes the shared Zep thread,
   │                                    injects the session instruction layer
   ├─────► MemoryService   (adkzep) ── long-term recall
   │
   ▼
Agent                   go.avagenc.com/{ava,zee,rafal,yori} — LLM + toolset
   │
   ▼
Tools                   postera tools (Ava only) · sub-agent tools (Ava only)
   │                    · Tuya · Workspace · Spotify toolsets (agentkit)
   ▼
Clients                 tuya.Client · gworkspace.Client · spotify.Client
   │                    resolve the caller's stored grant, then call the vendor
   ▼
External services
```

Two properties fall out of this arrangement:

- **Agents are libraries, not services.** `zee.New(...)` returns an `agent.Agent`. The
  application decides how it is exposed — as its own route, as a tool inside Ava, or
  both. Both happen here, from the *same* constructed agent instance
  (`main.go:199` builds `zeeAgent`; `main.go:218` gives it a route, `main.go:318`
  wraps it as one of Ava's tools).
- **Linking is not the agent's problem.** `rafal` consumes a `*gworkspace.Client`;
  `internal/link/gworkspace` manages the OAuth grant behind that same client. Neither
  imports the other. Connecting an account and using an account are different concerns
  with different lifetimes, and the type system says so.

## The agent architecture

### Four doors into one thread

The same thread is entered four ways, and the difference between them is *not* which
code runs — it is which **speaker** the turn is attributed to and which **instruction
layers** are in effect.

| Entry point | Speaker recorded | Run-layer instruction |
| --- | --- | --- |
| `AvaHandler.HandleHuman` / `HandleVoice` | `human` | none (channel layer differs instead) |
| `AvaHandler.HandleSelfAwaken` | `postera` (system role) | `ran-by-postera-instruction.txt` |
| `SpecialistHandler.HandleHuman` | `human` | `ran-by-human-instruction.txt` |
| `subAgent.Run` (Ava delegating) | `ava` | `ran-by-ava-instruction.txt` |

That table *is* the feature. A specialist woken by a bare `"@zee"` from Ava must not
greet the human as if newly addressed; a specialist addressed directly by the human
must answer the human. Same agent, same thread, same code — different framing, chosen
by the door the turn came through.

### Instructions compose in four layers

Prompt text is never string-concatenated at a call site. It is composed once, and
resolved per run through ADK's state-delta mechanism:

```
internal/agent/instruction.txt       layer 1 · identity of the room, relationship model
        %s  ← filled at build time by ava.Instruction() or specialist.Instruction()
{channel_instruction}                layer 2 · text vs voice — injected per run
{run_instruction}                    layer 3 · who triggered this turn — injected per run
{sess_instruction}                   layer 4 · time awareness, message format — written
                                                by the Zep session service
```

- Layer 1 is `agent.Instruction`, embedded in the same package that already declares
  the three state-delta keys. `ava` and `specialist` both import `agent` for those keys
  anyway, and `agent` imports neither of them — so the shared template costs no new
  node in the import graph and creates no cycle. Each kind fills the `%s` with its own
  text at build time via `fmt.Sprintf`.
- Layers 2 and 3 are `runner.WithStateDelta` values, set on **every** run path. The
  placeholders are non-optional: an unset key would leave a literal `{run_instruction}`
  in the prompt. Where a layer does not apply, it is set to `""` explicitly rather than
  omitted — see `subagent.go:64`.
- Layer 4 is owned by the session service, not this repo: rules about how a persisted
  message is shaped belong to the package that persists it.

The payoff: the voice channel's "never fall silent, the human is on a phone call" rule
lives in exactly one file, and cannot drift from the text channel's opposite rule
("silence is correct, the human can read the thread").

### The event-stream contract

`runner.Run` returns `iter.Seq2[*session.Event, error]`. Three rules hold at every
call site, and the duplication between them is deliberate:

1. **Drain the whole iterator.** Abandoning it leaks the run.
2. **Take text only from `event.IsFinalResponse()`.** Everything before it is tool
   traffic.
3. **A tool failure is not a Go error.** Errors from the iterator are infrastructure
   failures (`502`); a failed tool call comes back as a `FunctionResponse` the model
   reads and reacts to.

Token usage is accumulated on *every* event before the final-response branch, and the
charge is issued from a `defer` — so a run that dies halfway is still billed for what
it burned.

### Timeouts are one budget, chosen together

```
Cloud Run request timeout   300s  ── the real ceiling in production (platform default)
http.Server.WriteTimeout    310s  ── one full run: Ava + several specialists
  genai HTTPOptions.Timeout  90s  ── one model call; a hung provider cannot hold the run
http.Server.ReadHeaderTimeout 10s · ReadTimeout 30s · IdleTimeout 120s
```

A client whose run exceeds this sees the connection closed. That is the budget being
honoured, not a bug — and it is written down so nobody "fixes" it.

Note the ordering: **in production the binding limit is Cloud Run's 300s default, not the
application's 310s.** The deploy does not set `--timeout`, so the platform cuts first.
Raising `WriteTimeout` alone would therefore change nothing; a longer run needs the Cloud
Run timeout raised too. The 10s gap is deliberate slack, so the application is never the
component that severs a response mid-write.

## Postera: prospective memory

Cognitive science splits long-term memory into *episodic* (what happened), *semantic*
(what is known), and **prospective** (remembering to do something later). Almost every
agent framework ships the first two and skips the third — which is why agents are
purely reactive: they exist only between a request and its response.

[`go.naturallyfunny.dev/postera`](https://pkg.go.dev/go.naturallyfunny.dev/postera) is
the third. A *posterum* is a message an agent addresses to a future moment; when the
moment arrives the message is delivered back to the agent, in the same thread, and it
acts. In this repo that arrival is `POST /ava/awaken`, and Ava's instruction for that
door is explicit that she must never reveal the mechanism — from the human's side, she
simply remembered.

```
     Ava, now                                              Ava, later
        │                                                       ▲
        │ Create(ctx, CreateArgs{Message, TriggerAt})           │ POST /ava/awaken
        ▼                                                       │ body = the message
   ┌─────────┐   enqueue    ┌──────────────┐  fires at T   ┌────┴────┐
   │Postarius├─────────────►│ Queue (port) ├──────────────►│ this API│
   └────┬────┘              └──────────────┘               └─────────┘
        │ persist                Cloud Tasks
        ▼
   ┌──────────────┐
   │ Store (port) │  PostgreSQL
   └──────────────┘
```

**The architectural claim worth stopping on: the agent runtime stays stateless.** Ava
holds nothing between requests. What makes her *feel* continuous is not a resident
process or an in-memory scheduler — it is that "remembering" was factored out into two
interfaces:

```go
type Store interface {                    type Queue interface {
    Save(ctx, p Posterum) error               Enqueue(ctx, p Posterum) error
    Get(ctx, id string) (Posterum, error)     Cancel(ctx, id string) error
    Remove(ctx, id string) error          }
    List(ctx, q Query) ([]Posterum, error)
}
```

Consequences that follow directly, and that are the reason this shape is worth
imitating:

- **Statefulness becomes a storage choice, not a runtime property.** Postgres today; an
  in-memory `Store` and a `time.AfterFunc` `Queue` for a single-process deployment or a
  test; Redis, DynamoDB, or a different scheduler tomorrow. **The agent's logic does not
  change in any of those cases** — it still just calls `Create`.
- **The two halves fail independently, so they are separate interfaces.** A database
  write and a scheduler RPC are different failure domains and different swap axes. One
  combined `Backend` would force an adapter author to reimplement both to change either.
- **Horizontal scaling is free.** Any instance can serve the callback, because nothing
  about the memory lives in the instance that created it. This service runs on Cloud
  Run with `--min-instances=0`: the process that scheduled the wake-up is usually *gone*
  by the time it fires.
- **Testing needs no clock manipulation.** The wake path is an HTTP endpoint; the
  scheduling path is an interface. Neither requires waiting for real time to pass.

Postera is deliberately *not* a framework: it does not run a worker, execute the wake,
or enforce authorization. It coordinates "what to remember" with "when to be woken",
and its core package imports only the standard library — the third-party adapters are
opt-in subpackages. This repo supplies the parts it declines to own: authentication on
the callback, the wallet gate, and the agent that decides what to do when it hears from
itself.

One consequence is documented rather than hidden: postera scopes by identity from
context, and that scoping is *filtering, not access control*. It is bypassable by
anyone holding the `Store`. Enforcement therefore lives above it, in
[`identity`](internal/identity) and the wallet guard — which is why `/ava/awaken` is
authenticated even though it is "just" an internal callback.

## The memory triad

Three memory kinds, three packages, each a complete vertical slice with its own
handler. They share the word "memory" and nothing else, which is exactly why they are
not one package.

| Kind | Package | Backed by | Ownership check |
| --- | --- | --- | --- |
| episodic | `internal/session` | Zep threads | **yes** — explicit |
| semantic | `internal/knowledge` | Zep graph | not needed |
| prospective | `internal/postera` | Postgres + Cloud Tasks | delegated to the SDK's context scoping |

The differences in that last column are not inconsistency; they are the checks being
placed where the risk actually is:

- `session.Service` fetches the thread, compares its `UserID` to the caller's, and only
  then deletes — because a session ID is a URL-shaped string that could name anyone's
  thread.
- `knowledge` needs no such check: every store call is *parameterized* by the user ID
  taken from the verified token (`GetByUserID`, `User.Delete`). There is no ID from the
  request to confuse.

Two behaviours to know before touching them:

- **`GET /knowledge` returns the whole graph.** No `limit`, no `cursor`. A graph is
  meaningless in pieces — an edge whose endpoints are on another page cannot be drawn —
  so pagination is the adapter's problem, not the API's. `knowledge/zep` drains Zep's
  50-item pages behind the port, with a page cap that bounds a runaway loop and a guard
  that detects a backend ignoring the cursor.
- **`DELETE /knowledge` deletes everything, sessions included.** It maps to Zep's
  `User.Delete`. This is intended — "forget me" means forget me — and it is called out
  in the code, in `CLAUDE.md`, and here, because it is the kind of behaviour that must
  never be a surprise.

## The wallet

A real double-entry ledger, not a balance column. The essentials:

- **Writing is `Transact(ctx, Spec)` and nothing else.** A `Spec` is ≥ 2 postings that
  sum to zero. There is no `Debit`/`Credit` pair, because a debit without its matching
  credit is precisely the bug double-entry exists to prevent. Charging a run is
  `{user: −n, revenue: +n}`; a top-up is `{user: +n, pending: −n}`; a future P2P
  transfer is two user accounts — same contract, no new method.
- **`int64` micro-rupiah, credit-positive.** Rates are quoted in rupiah per million
  tokens, so `micros = rate × tokens` is exact integer arithmetic with no division and
  no rounding on the write path. Asset-like system accounts read *negative*; that is
  double-entry working, not a bug, and it is stated in the package doc so nobody
  "corrects" it.
- **The transaction is the usage log.** There is no second table. `GET
  /wallet/usage/today` reads the user's entries and computes cost from `entry.Amount`,
  not from the receipt JSON — so the number stays right even if the receipt shape
  drifts.
- **Post-paid, and a charge can push a balance negative.** The gate only asks for
  balance > 0 *before* a run, because the cost is unknowable until the run ends. The
  alternative — refusing to record a charge for tokens already burned — would silently
  lose money and corrupt the ledger. A small negative balance is the honest state.
- **Concurrency by deterministic lock order.** `Transact` sorts postings by account ID
  before taking row locks, so concurrent transactions touching the same accounts queue
  instead of deadlocking. The known cost: every charge briefly serializes on the
  `revenue` row. Postgres row locks are microseconds; the levers if that ever binds
  (sharded revenue sub-accounts, or deriving system balances from `SUM(entries)`) are
  deliberately deferred rather than pre-built.
- **Migrations run in the deploy pipeline, never at boot.** The runtime holds DML
  privileges only. `NewLedger` *validates* the tables exist and fails startup with a
  clear message if a deploy skipped the migration step — a boot failure instead of a
  mysterious first-charge failure.

Invariants any reviewer can check directly in SQL: `SUM(amount) = 0` per `txn_id`;
`accounts.balance = SUM(entries.amount)` per account; `SUM(balance)` over all accounts
= 0.

**Midtrans top-up is complete, tested, and intentionally not wired.** The slice in
[`internal/wallet/midtrans`](internal/wallet/midtrans) implements Snap checkout and the
payment webhook — including the part most implementations get wrong: a signed
notification is *never* trusted for the amount. The signature only proves authenticity
while the server key is secret, so every notification claiming success is re-confirmed
against Midtrans' Core status API, and the ledger is written from what that API
reports. Its wiring in `main.go` is commented out pending a product decision on paid
top-up; the code and its integration test remain so that turning it on is uncommenting,
not rewriting.

## Trust boundaries

Three authenticators, because there are three genuinely different callers — not
because three is a nice number.

| Middleware | Caller | Mechanism |
| --- | --- | --- |
| `FirebaseAuthenticator` | the SPA, as a signed-in user | verifies the ID token via the Admin SDK, puts the UID in context |
| `APIKeyAuthenticator` (postera) | Google Cloud Tasks | constant-time compare of the `api-key` header set at enqueue time |
| `APIKeyAuthenticator` (third party) | the voice client | same mechanism, different secret |

The subtle one is `/ava/awaken`. It sits outside the Firebase group and takes the user
identity from a plain `user-id` header — which would be a total account-takeover hole
if the endpoint were open. So the API key is checked **first**, and only then is the
header read:

```go
mux.Handle("POST "+avaAwakenEndpoint,
    posteraAuthenticator.Authenticate(          // ← proves the caller is our queue
        apiuser.HTTPWithID(                     // ← only now is `user-id` trusted
            walletGuard.RequireBalance(...))))
```

The ordering is the security property, and it is visible in one expression in
`main.go`. That is the argument for explicit wiring in a nutshell: a middleware stack
assembled by a framework or a config file could not be audited by reading a single line.

**Why a shared secret rather than Cloud Run IAM here**, since that is the first thing a
GCP reviewer will ask. Cloud Tasks can attach a Google-signed OIDC token, and Cloud Run
can require IAM authentication — but **that requirement is per-service, not per-route**.
This one service also serves the browser SPA, so switching it off
`--allow-unauthenticated` to protect one callback would lock out every user-facing
endpoint. Splitting the callback into its own service to get route-level IAM would buy
platform-verified identity at the cost of a second deployment, a second image, and a
shared-state boundary between them. The API key gives the property that actually matters
here — *only our queue can reach this route* — for one header compare, so the OIDC token
was dead weight and the option that attached it has been removed rather than left as
configuration that looks like a control but enforces nothing.

Other boundaries worth naming:

- **Identity is platform-level, not product-level.** One Firebase project (`avagenc`)
  and one Firestore database serve all Avagenc products, so linking data is named
  `gworkspace_tokens`, never `avagenc_chat_gworkspace_tokens`. A user connects Google
  once, for the account — not once per product.
- **OAuth state is HMAC-signed and stateless.** `integration.expiry.HMAC(integration ␀
  owner ␀ expiry)`, 15-minute TTL. Binding the owner closes the classic OAuth CSRF
  (a victim completing a flow with the attacker's code) with no server-side state
  store; binding the integration domain-separates the one shared secret, so a
  gworkspace state can never be replayed at the spotify endpoint. Both properties are
  tested ([`state_test.go`](internal/link/state_test.go)).
- **CORS allows exactly one origin, compared literally.** Not a list, not a regex, not
  a wildcard. One backend deployment serves one web origin; dev and production are
  separate deployments. `Vary: Origin` is always set, so a shared cache cannot serve one
  origin's response to another.
- **Disconnect does not revoke the upstream grant.** `DELETE /{integration}/connection`
  forgets the stored refresh token; the app stays listed in the user's Google/Spotify
  account until they revoke it there. This is a real gap, not an oversight — the SDK
  lacks a revoke call today.

## The module family

Three vanity module namespaces, split by *who the code is for* rather than by size:

```
github.com/avagenc/chat            this repo — the application. Depends on both
       │                           namespaces below; nothing depends on it.
       ▼
go.avagenc.com/{ava,zee,rafal,yori}   product-specific agents: persona, toolset,
       │                              domain instructions. Reusable across Avagenc
       │                              products, meaningless outside Avagenc.
       ▼
go.naturallyfunny.dev/{postera,agentkit,api,tuya,gworkspace,spotify,adk}
                                      product-agnostic SDKs: no Avagenc concept
                                      appears in them. Independently useful,
                                      independently versioned, publicly documented.
       ▼
google.golang.org/adk · standard library
```

The direction is strict and verified: **no `go.naturallyfunny.dev` module requires
`go.avagenc.com`** — checked across every one of them in this build's dependency graph.
The generic layer cannot be polluted by product concerns because it cannot even name
them.

Why separate modules rather than one repo with packages:

- **A module boundary is a version boundary.** `postera` is at v0.22.0 and `zee` at
  v0.6.1 because they change for unrelated reasons. In a single module they would share
  a version number and every agent tweak would look like an SDK release.
- **It forces the generic/specific split to be real.** Wanting to reach from `postera`
  into an Avagenc type is not a code-review conversation; it is a dependency cycle the
  toolchain refuses. The pressure that keeps the SDKs clean is mechanical.
- **`internal/` says the rest is not for sharing.** Every package in *this* repo is
  under `internal/` — deliberately, even for well-shaped contracts like `wallet.Ledger`.
  A public package at the root of an application module is an API commitment to nobody:
  sharing across products needs a separate module regardless, so exporting here would
  buy zero reuse and cost a promise to keep. When the wallet earns extraction, it earns
  a module.

**Trade-off, stated plainly:** multiple modules mean a change spanning an SDK and the
app is two commits, two tags, and a `go get`. That is real friction, accepted because
the alternative — one module — makes the boundary advisory, and an advisory boundary
between "generic SDK" and "our product's persona" does not survive a deadline.

## Infrastructure

One stateless Go binary on Cloud Run, in front of managed services. Nothing durable lives
in the container.

```
                     chat.avagenc.com  (SPA, separate hosting)
                             │  HTTPS · CORS: exactly one origin
                             ▼
  Cloud Tasks  ─────────►  Cloud Run  chat-http        ─────►  Gemini API      (model)
  queue "postera"          asia-southeast1                ──►  Zep Cloud       (thread + graph)
  POST /ava/awaken         distroless/static, :8080       ──►  Neon Postgres   (wallet)
                           min-instances 0                ──►  Neon Postgres   (postera)
                           128Mi · 1 vCPU                 ──►  Firestore       (link tokens)
                                                          ──►  Tuya · Google · Spotify
```

### The choice follows from the workload, not from a vendor preference

Three measurable properties of this program decide almost everything below:

1. **It is nearly idle on CPU.** A request's wall-clock is dominated by waiting on Gemini,
   Zep, and third-party APIs. The Go process spends most of a 30-second run blocked on
   I/O. That is why 128Mi and 1 vCPU are enough for an entire agent roster.
2. **It is completely stateless.** The transcript is in Zep, money is in Postgres, grants
   are in Firestore, future wake-ups are in Cloud Tasks. No disk, no in-process session
   store, no background goroutine expected to outlive a response — the self-awaken design
   exists precisely so nothing has to be held in memory until later.
3. **Its traffic is spiky and human-paced.** A user sends a message, then thinks. Between
   conversations, the correct amount of compute to pay for is zero.

A workload that is I/O-bound, stateless, and bursty is the exact shape serverless was
invented for. Renting a machine by the hour to hold sockets open would mean paying
continuously for a process that is, most of the time, doing nothing but waiting.

**Observed cost so far: zero.** This project has been through several architectures —
including an earlier microservices phase with many Cloud Run services across both dev and
production projects — and has stayed inside the GCP free tier throughout. That is not a
claim that it stays free at scale; free tiers end. It is evidence about the *shape* of the
curve: the bill starts at nothing and tracks usage, instead of starting at a monthly
minimum and being paid whether anyone shows up or not.

### Cloud Run rather than VMs, Kubernetes, or AWS compute

**Choice.** Serverless containers, scale-to-zero, request-based billing.
**Alternative.** EC2/GCE with an autoscaling group; GKE/EKS; ECS Fargate or App Runner.
**Why.** Two costs decide it, and the smaller one is the invoice.

The larger cost is *people*. A self-managed cluster means someone owns node pools,
networking, ingress, certificate rotation, autoscaling policy, patching, and an on-call
rotation. For this workload that person would be more expensive than the compute by an
order of magnitude — and would still not produce a platform more reliable or more secure
than the one Google's SRE organisation already operates. Choosing managed here is not
avoiding the work; it is declining to do worse work at higher cost.

The smaller cost is billing shape. Cloud Run charges for CPU and memory *per request*,
and scales to zero between conversations. Because the critical path is model latency
rather than computation, a single instance serves many requests concurrently — Cloud
Run's default is up to 80 simultaneous requests per instance — so as traffic grows, the
cost per request *falls*: the same instance-second is amortised over more concurrent
waits. Reserved capacity has the opposite curve.

Against AWS specifically, since it is the reflex answer: the gap is not that AWS lacks
containers, it is that **AWS has no equally direct request-billed, scale-to-zero
container product**. Fargate does not idle at zero and wake on a request; App Runner
keeps provisioned capacity warm. Getting Cloud Run's behaviour on AWS means assembling
several components — load balancer, target groups, VPC and subnets, task definitions,
IAM roles — each with its own configuration surface and its own line on the bill, to
reach the same outcome that here is one `gcloud run deploy` in a CI step.

The second reason is gravity. Identity (Firebase), the model (Gemini), scheduling (Cloud
Tasks), and the linking datastore (Firestore) are all Google services this product is
genuinely built on. Running compute on AWS would put a cloud boundary between the
application and its own dependencies: cross-cloud egress on every model call, federated
credentials to maintain, and two consoles and two IAM models to reason about during an
incident. Co-locating compute with the services it calls is not vendor loyalty, it is
removing a boundary that would otherwise have to be paid for and secured.

**Where AWS would genuinely win:** an organisation with an existing AWS estate,
negotiated committed-use discounts, and staff whose expertise is already there. Those are
real and they are decisive — for that organisation. They are simply not the situation
here, and adopting them anyway would be inheriting someone else's constraints.

### Firebase Authentication rather than a bespoke identity system

**Choice.** Firebase verifies the credential; this service only reads a verified UID.
**Alternative.** Own user table with password hashing and sessions; or Cognito/Auth0.
**Why.** Authentication done properly is a larger and less forgiving domain than this
entire codebase: credential storage, reset and verification flows, token rotation and
revocation, MFA, account-enumeration resistance, breach detection, social-provider
integrations, and a permanent obligation to respond to each new class of attack. Building
it would add the single highest-consequence surface in the product — the one where a bug
is a breach — in exchange for no user-visible benefit.

What the application keeps is exactly the part that is specific to it: **authorization**.
Ownership checks, the balance gate, and the middleware ordering at `/ava/awaken` are all
here, in code, auditable — see [Trust boundaries](#trust-boundaries). Identity is
delegated; access control is not. And because identity is platform-level, one Firebase
project covers every Avagenc product: a user signs in once for the account, not once per
service.

### Neon Postgres for money, and an open question about Firestore

**Choice.** PostgreSQL for the wallet and for prospective memory, on Neon.
**Alternative.** A document store for everything; or a conventional managed instance
(Cloud SQL / RDS).
**Why Postgres at all.** The wallet is a double-entry ledger whose correctness rests on
things only a relational engine gives directly: multi-row transactions, row locks taken
in a deterministic order, a partial unique index for idempotency, and invariants that can
be *checked* in SQL at any time (`SUM(amount) = 0` per transaction; total across all
accounts = 0). A document store cannot express that check, which means it could not prove
the ledger is intact.

**Why Neon.** It applies the same shape as the compute: Postgres that scales to zero and
bills for use, rather than an instance rented by the hour to sit idle overnight. It also
adds something a conventional instance does not — **database branching**, so a dev branch
is a copy-on-write fork of production data rather than a separately seeded database that
silently drifts. Both databases (wallet and postera, deliberately separate — different
migration lifecycles) live behind one dashboard.

**The open question, stated rather than buried.** Firestore holds only OAuth grants and
Tuya accounts. It is billed per document read and write, whereas Postgres is already in
the stack and would consolidate the datastore count. On cost alone Postgres is probably
the better home for these collections. It has not been migrated because the decision has
not been *costed*: these are low-traffic collections read roughly once per linked-agent
run, so the saving may not repay the migration and the risk of touching credential
storage. The rule for revisiting it is concrete — if linking reads become a visible line
on the Firestore bill, migrate; until then this is a known, priced-in inefficiency rather
than an oversight.

### Cloud Tasks rather than a queue, a cron, or a workflow engine

**Choice.** Google Cloud Tasks as the `Queue` implementation behind Postera.
**Alternative.** SQS delay queues, EventBridge Scheduler, a cron worker polling a table,
or a durable-workflow engine (Temporal, Celery-style workers).
**Why.** The requirement is unusually narrow and worth stating exactly: *call one URL at
one specific future timestamp, at least once, with retries and backoff, and let me cancel
it by name.* Not a stream, not fan-out, not a message bus.

- **SQS delay queues cannot do it at all.** `DelaySeconds` is capped at 900 seconds — 15
  minutes — and [the limit cannot be raised](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-delay-queues.html).
  "Remind me tomorrow at 9" is outside the product's range, and AWS's own documentation
  redirects that use case elsewhere.
- **A cron worker polling a table** reintroduces the always-on process that scale-to-zero
  just removed, and makes scheduling accuracy the application's problem.
- **A durable-workflow engine** is a runtime to operate and a programming model to adopt,
  for a capability that is one scheduled HTTP call.
- **EventBridge Scheduler is the honest AWS counterpart** and would work. The
  differentiators are fit rather than capability: Cloud Tasks names each task
  deterministically from the posterum ID, which makes enqueue idempotent at the platform
  level (a retried `Create` cannot double-schedule) and makes cancellation a direct
  delete by that name; and it lives in the same project and IAM boundary as the service it
  calls back, so the wake-up path never leaves GCP or crosses a billing boundary.

The deeper fit is architectural: Postera's `Queue` port is two methods, `Enqueue` and
`Cancel`. Cloud Tasks maps onto it one-to-one, with no adapter logic bridging a mismatch —
the sign that the primitive and the requirement are actually the same shape.

### Gemini, chosen on context behaviour

**Choice.** `gemini-3.5-flash` for every agent in the roster.
**Alternative.** OpenAI models, or open-weight models (Qwen, Kimi) self-hosted or via a
provider — all of which were tried.
**Why.** This product's central design decision is that
[the transcript is the state](#why-this-shape): every run re-reads a shared thread, and
every agent reasons from messages it did not write. Long-context fidelity is therefore not
a nice-to-have, it is the substrate — a model that degrades over a long thread degrades
the product itself. Across the evaluated set, Gemini held up best on exactly that:
treating a large context as information to be used rather than tolerated. The model is
pinned in `main.go` rather than read from the environment, so changing it is a reviewed
deploy.

### Zep today, with a replacement being built

**Choice.** Zep Cloud for episodic (threads) and semantic (knowledge graph) memory.
**Alternative.** Mem0; Letta/MemGPT; assembling retrieval on the Postgres already present.
**Why.** Zep supplies both memory kinds this product needs as one managed service, with
automatic entity/edge extraction from the conversation rather than a graph the application
has to maintain. Letta/MemGPT was rejected on an architectural mismatch rather than
quality: it wants to own the agent runtime, whereas here agents are libraries and the
runtime is deliberately stateless and disposable.

**Its shortcomings are known and are not hidden here:** pricing, and latency — Zep is a
Python service, which is felt on a path already spending seconds on model calls. The
response is underway rather than theoretical: [`chronica`](https://github.com/naturallyfunny/chronica)
is a from-scratch Go SDK for the session/episodic half, built around the same
port-and-adapter shape used throughout this repo — a `Store` interface with in-memory and
Postgres implementations behind conformance tests.

This is where the architecture pays for itself concretely. Swapping memory backends does
not touch `session.Service`, `knowledge.Service`, or any handler; it means writing a
sibling adapter next to the existing `zep` one and changing one line in `main.go`. The
ports were not drawn in anticipation of a vendor change — they were drawn because
[a feature package should not import its adapter](#design-principles) — but a real
migration is what proves they were drawn in the right place.

### Pipeline and separation of environments

Push to `main` or `dev` selects a GitHub Environment, and each points at a **separate GCP
project**. Prod and dev do not share a project, so Cloud Run services, Artifact Registry,
Firestore, Cloud Tasks, and service accounts are isolated by construction rather than by
naming convention. The deploy job validates that every required secret and variable is
non-empty *before* building — configuration errors fail in CI with a message naming the
missing key, instead of crash-looping a container at boot. Images are tagged with the
commit SHA, so a rollback is redeploying a known digest. The runtime image is
`distroless/static`: no shell, no package manager, nothing to exec if something does get
in.

Deployer and runtime are two different service accounts, deliberately: the deployer can
push images and deploy; the runtime can reach Firestore and enqueue tasks. Neither can do
the other's job.

### Where this is not yet production-grade

Stated plainly, because a reviewer will find these anyway:

- **`--max-instances=1`.** This service is still in development, not carrying production
  traffic, and the cap is a cost ceiling for that phase — not an architectural limit. It
  is less restrictive than it looks (one instance still serves many concurrent requests),
  and because the process holds no state, raising it is a single flag rather than a
  redesign. It should be raised before real traffic arrives.
- **The effective request ceiling is 300s, not 310s.** The Go server sets
  `WriteTimeout: 310s`, but the deploy does not set `--timeout`, so Cloud Run's 300s
  default applies first. Any run needing longer requires raising the Cloud Run timeout
  explicitly; raising `WriteTimeout` alone would do nothing.
- **Secrets are plain Cloud Run environment variables**, not Secret Manager. Adequate now;
  moving to `--set-secrets` for managed rotation and access auditing is a
  configuration-only change that does not touch application code.
- **Bootstrap resources are created by hand.** Artifact Registry, the Cloud Tasks queue,
  service accounts and their bindings, and Firestore are prerequisites with no
  infrastructure-as-code behind them. Rebuilding an environment from nothing is currently
  a documented manual procedure rather than an executable one.

## Design decisions & trade-offs

Each is stated as *what we did*, *the plausible alternative*, and *why this side*.

### Explicit wiring in `main.go`, not a DI container

**Choice.** ~580 lines constructing every dependency by name and passing it in.
**Alternative.** wire/fx/dig, or a `pkg.Build()` factory per feature.
**Why.** This file is the system's only global truth: read it and you know every
dependency, every middleware order, every route. A container moves that knowledge into
generated code or runtime reflection, where an auditor cannot see that the API-key
check precedes the `user-id` read. The previous iteration of this codebase *did* use an
opaque `Build()` factory and was dismantled for exactly this reason.
**Trade-off:** a long `main`. Accepted — it is long in the way a table of contents is
long, and it is the one place where "long" is not complexity but disclosure.

### One route per agent, not `/{agent}`

**Choice.** `POST /ava`, `POST /zee`, `POST /rafal`, `POST /yori`, each registered
individually.
**Alternative.** `POST /{agent}` with a `map[string]*runner.Runner`.
**Why.** The map version is shorter and hides the thing that matters: each agent has
different middleware needs and, in Ava's case, entirely different entry points
(`/ava/voice`, `/ava/awaken`). A dispatcher would immediately grow conditionals keyed
by the path parameter — a switch statement wearing a map costume. Adding an agent is
one wiring block, and that visible cost is a feature: it is where you notice the new
agent needs a balance gate.
**Trade-off:** repetition in `main.go`. Preferred over indirection at the routing layer,
where mistakes are security mistakes.

### Duplicated drain loops instead of a shared helper

**Choice.** Each of the five entry points contains its own ~25-line loop: attach
speaker, run, accumulate usage, `defer` the charge, take the final response.
**Alternative.** A `respondRun(w, runner, opts)` helper, or a middleware.
**Why.** A reviewer *will* flag this, so: the loops differ in speaker, run-layer
instruction, trigger label, response shape, and error surface (HTTP status vs a
returned `error` in the sub-agent case). A helper would need all five as parameters —
at which point it is a function taking a configuration struct that reads exactly like
the code it replaced, but one indirection away from the `runner.Run` call it exists to
wrap. The [principle](#design-principles) is that a wrapper must add something; this
one would only move code.
**Trade-off:** a change to the event-stream contract touches five files. Accepted,
because it touches five files *that each get to decide how to react* — which is what
made the empty-response and voice-channel behaviours cheap to add.

### One shared thread per user, derived server-side

**Choice.** `"chat-" + userID`, written inline at every use; no session ID from header,
path, or body.
**Alternative.** Client-supplied session IDs and multiple threads per user.
**Why.** The product is one continuous relationship, not a list of chats. Deriving the
thread from the verified token means there is no ID to authorize, no way to name
someone else's thread, and no cross-user leak to reason about. It also removes an entire
class of client bug (the SPA losing track of which session it is in).
**Trade-off:** no per-topic threads, and adding them later means introducing an ID plus
the ownership checks its absence currently makes unnecessary. Accepted knowingly.
The literal is written out at each site rather than hidden behind a `sessionID(userID)`
helper — a one-line wrapper over a string concatenation is indirection without
information, and it was in fact removed in commit `596f7f0`.

### Postera is not given a session scope

**Choice.** `postera.New(...)` is wired with `WithHumanFromContext` but deliberately
*not* `WithSessionFromContext`.
**Alternative.** Configure both, "for defence in depth".
**Why.** With one session per user, a posterum's session is always derivable from its
human — so the extra filter can never exclude a row the human filter admits. It would
be a redundant constraint that *looks* like a security control, which is worse than
none: the next reader would assume a boundary exists there. Removed in `5062d0f` for
this reason.

### Sentinel errors per feature, minted where they mean something

**Choice.** `session.ErrNotFound`, `knowledge.ErrNotFound`, `wallet.ErrDuplicateRef` —
each owned by its feature. `ErrForbidden` is minted by services, never by adapters.
**Alternative.** One shared `errors.ErrNotFound` across the app.
**Why.** A shared sentinel makes every handler's `errors.Is` ambiguous about *what* was
not found and re-couples packages that otherwise share nothing. Keeping the
authorization sentinel out of adapters encodes a rule in the type system: storage
cannot decide authorization, it does not have the information.

### Standard-library `ServeMux`, no router dependency

**Choice.** `net/http.ServeMux` with Go 1.22+ method-and-pattern routing.
**Alternative.** chi, echo, gin — chi was in fact used here before (`0a7289b`).
**Why.** What the router was doing — method matching, path parameters, middleware
chaining — is now in the standard library, and `PathValue` covers the one dynamic
segment in the whole API (`/postera/{posterumID}`). Middleware is ordinary
`func(http.Handler) http.Handler` composition, visible at each route. One fewer
dependency in the security-critical path, and no framework idiom to learn.
**Trade-off:** no route groups, so `firebaseAuthenticator.Authenticate(...)` is repeated
per route. That repetition is *why* the security ordering is auditable per line.

### Errors from failed charges are logged, not returned

**Choice.** A failed `Charge` writes a log line; the agent's response still returns 200.
**Alternative.** Fail the request when billing fails.
**Why.** The work was done and delivered; failing the response would give the user
nothing *and* still not collect. The reconciliation invariants make an unbilled run
visible after the fact, which is the right place to catch it. The charge itself uses
`context.WithoutCancel` so a disconnecting client cannot cancel its own bill.
**Trade-off:** revenue can be lost silently if the wallet database is down for a
sustained period. Accepted; the alternative is refusing service for a bookkeeping
failure.

### `time/tzdata` embedded in the binary

**Choice.** `_ "time/tzdata"` is imported in `main.go`.
**Alternative.** Rely on the OS zoneinfo database.
**Why.** The production image is `gcr.io/distroless/static`, which has no zoneinfo. Every
agent run resolves the caller's IANA zone, and `GET /wallet/usage/today` computes local
midnight — both would fail at runtime in the container while passing on a developer's
laptop. Measured cost: 403 KB on a ~60 MB binary (60,506,466 bytes with, 60,093,458
without) to remove an entire class of environment-dependent bug.

### A dev-only entry point, kept out of the repository

**Choice.** `cmd/httpdev` mirrors `cmd/http` exactly except that identity comes from a
`user-id` header instead of a Firebase token, and it is `.gitignore`d.
**Alternative.** An `APP_ENV`-gated bypass inside `cmd/http`.
**Why.** A conditional auth bypass in the production binary is one misconfigured
environment variable away from being an open API. A separate binary cannot be
misconfigured into existence — it is not in the image, and `Dockerfile` builds
`./cmd/http` by name. Keeping it untracked means it also cannot be deployed by
accident from a fork.
**Trade-off:** it can drift from `cmd/http`, since nothing forces them to stay in sync.
Accepted, because the failure mode of drift is "a developer's curl stops working",
while the failure mode of the alternative is "everyone's data is readable".

## Non-goals

- **Not a general agent framework.** No plugin system, no agent registry, no dynamic
  routing. Adding an agent is a wiring block written by a human.
- **Not multi-tenant beyond the user.** There are no organizations, teams, or shared
  threads. Every scope in the system is one Firebase UID.
- **No streaming responses.** A run returns one JSON body when it completes. Streaming
  would change the billing model (partial runs) and the thread-write model (partial
  turns); it is not free, and it has not been needed.
- **No metrics or tracing surface.** Cloud Run's request logs plus `log.Printf` are
  what exist. Instrumenting before there is a question to answer would be decoration.
- **No refunds in policy**, though `refund` exists as a ledger kind from day one so a
  gateway dispute can be recorded correctly if policy changes.

## External constraints

Constraints that come from the platforms themselves, not from this code. Each integration
below is complete and works; what is unfinished is a commercial or legal prerequisite on
the vendor's side.

- **Tuya (zee)** requires a cloud-project partnership to link end-user app accounts, so
  Tuya onboarding is manual today and `internal/link/tuya` exposes status only — there
  is no public OAuth flow to expose.
- **Spotify (yori)** grants extended quota only to registered organizations meeting a
  250k-MAU bar; below that, the app stays in development mode with an allowlist. Playback
  additionally requires the end user to have Premium.
- **Google Workspace (rafal)** is in the *Restricted* scope tier because of
  `gmail.modify`, which requires an annual paid third-party security assessment (CASA)
  for full public production. While unverified, refresh tokens expire after 7 days —
  which directly conflicts with the stored-refresh-token architecture. The documented
  cheapest exit is dropping the restricted scope to fall back to *Sensitive* tier.

These are recorded because they change what "production-ready" means for each
integration, and a reviewer should not have to infer them from a 400 response.

## Verification

Everything below was run in this working tree; the numbers are observed, not estimated.

```console
$ go build ./... && go vet ./...          # clean, no findings
$ gofmt -l .                              # no output
$ go test ./... -count=1 -cover        # packages without tests omitted
ok  github.com/avagenc/chat/internal/knowledge/zep    0.549s  coverage:  29.1% of statements
ok  github.com/avagenc/chat/internal/link             0.307s  coverage: 100.0% of statements
ok  github.com/avagenc/chat/internal/wallet           0.335s  coverage:  38.2% of statements
ok  github.com/avagenc/chat/internal/wallet/midtrans  0.939s  coverage:  10.2% of statements
ok  github.com/avagenc/chat/internal/wallet/postgres  0.615s  coverage:   0.0% of statements
```

**Read those last two numbers correctly.** The wallet's Postgres and Midtrans suites are
integration tests against a real database, per the repo convention of testing behaviour
rather than mocks. They **skip** unless `WALLET_TEST_DB_URL` points at a disposable
database — which is why they report near-zero coverage above. With a database:

```bash
WALLET_TEST_DB_URL='postgres://…/wallet_test' go test ./internal/wallet/... -count=1
```

Each run drops the wallet tables and re-applies `migrations/` from scratch, so it must
never be aimed at a shared database.

What the always-running tests actually pin down:

| Package | Covered | How |
| --- | --- | --- |
| `internal/link` | 100% — `SignState`/`VerifyState` | table-driven: round trip, wrong owner (the CSRF case), cross-integration replay, wrong secret, expiry boundary, rewritten expiry, tampered mac, four malformed inputs |
| `internal/wallet` | `Charge` 95.5%, `Usage.Add` 100% | the billing arithmetic against an in-memory `Ledger` — a second real implementation of the port, not a mock: cached-token split, tool-use prompt, the negative-input clamp, the one-micro floor, receipt provenance, survival of a cancelled context, error wrapping |
| `internal/knowledge/zep` | `drain` 100% | page draining: short page, exact page multiple, cursor advance, a backend that ignores the cursor, the runaway page cap, mid-drain error yielding no partial graph |

The uncovered 4.5% of `Charge` is its `json.Marshal` failure branch, unreachable for a
struct of scalars.

**Known gaps, stated rather than hidden:**

- Handlers, `identity`, and the linking slices have no automated tests. They are thin
  HTTP glue whose failure modes are wiring, and the repo convention rejects mock-based
  handler tests as a way of pretending otherwise. The honest position is that this glue
  is verified by exercising the running service.
- `session/zep` and the field-mapping half of `knowledge/zep` are untested; they are
  straight struct translation, and a fake Zep would test the fake.
- There is no CI test job. [`deploy.yaml`](.github/workflows/deploy.yaml) validates
  configuration, builds, migrates, and deploys — it does not run `go test`. Adding that
  gate is the single highest-value change to this pipeline.

## Layout

```
cmd/http/main.go                  composition root — every dependency, route, and
                                  middleware order, explicit and in one file
Dockerfile                        distroless static image, builds ./cmd/http by name
.github/workflows/deploy.yaml     validate config → build → goose migrate → Cloud Run

internal/agent/
  instruction.go + instruction.txt  the three state-delta keys, and layer 1 — the base
                                  template every agent kind composes into
  ava/handler.go                  human (text) · human (voice) · self-awaken
  ava/subagent.go                 specialist → ava.SubAgent, on the consumer's side
  ava/instruction.go + *.txt      Ava's kind, channel, and run-layer instructions
  specialist/handler.go           the direct-address entry point, shared by all three
  specialist/*.txt                specialist kind + run-layer instructions

internal/identity/                firebase.go (ID token) · apikey.go (constant-time)
internal/session/                 episodic: service.go (port + types + ownership),
  zep/                            handler.go, and the Zep adapter
internal/knowledge/               semantic: same shape; the adapter drains pages
  zep/
internal/postera/handler.go       prospective: HTTP glue over the SDK, no local service
internal/wallet/
  ledger.go                       the double-entry port + domain types + sentinels
  biller.go                       token usage → one balanced transaction
  guard.go                        RequireBalance → 402
  handler.go                      balance + today's usage
  postgres/                       pgx adapter, migrations/, schema validation at boot
  midtrans/                       top-up slice — complete, tested, parked
internal/link/
  state.go                        HMAC OAuth state, shared because it was found shared
  gworkspace/ spotify/ tuya/      one slice per integration

CLAUDE.md                         working agreement: conventions, and the prohibitions
                                  that produced this structure
```

`cmd/httpdev` and `cmd/wallet/{migrate,seed}` exist locally and are `.gitignore`d —
see the [dev entry point decision](#a-dev-only-entry-point-kept-out-of-the-repository).

## Reading the source

In this order, and it is short:

1. **[`cmd/http/main.go`](cmd/http/main.go), top to bottom.** It is the map. By the end
   you know every component, every route, and every middleware order in the system.
2. **[`internal/wallet/ledger.go`](internal/wallet/ledger.go).** The clearest example of
   the repo's contract-and-adapter shape: a port, its domain types, its sentinel, and a
   package doc that explains the sign convention before you can misread it.
3. **[`internal/agent/ava/handler.go`](internal/agent/ava/handler.go) and
   [`subagent.go`](internal/agent/ava/subagent.go).** The same run, entered two ways —
   this is where the four-door model becomes concrete.
4. **The `.txt` instruction files.** They are the product. Reading
   `ran-in-voice-channel-instruction.txt` next to `ran-in-text-channel-instruction.txt`
   shows why the layering exists at all.
5. **[`CLAUDE.md`](CLAUDE.md).** The working agreement the code is held to, including
   the explicit prohibitions — helpful factories, hidden wiring, map dispatch, generic
   names — that this structure is a reaction against.

## License

This repository ships no `LICENSE` file. It is first-party Avagenc application code,
not a library offered for reuse, so default copyright applies: all rights reserved. The
generic building blocks are the ones that *are* published, under their own licenses, at
[`go.naturallyfunny.dev`](https://pkg.go.dev/go.naturallyfunny.dev/postera).
