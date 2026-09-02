# Agent and Dependency Credential Requirements

Status: Implemented for secrets and OAuth
Audience: Forge, Rustic UI, and deployment operators
Last updated: 2026-09-01

## Contract

Forge is the only authority for declaring, evaluating, and injecting agent
credentials. Credential declarations do not belong in `AgentSpec`, blueprint
JSON, Rustic AI, Uniko, or the browser.

- Credentials inherent to an agent class are declared in
  `forge-agent-registry.yaml`.
- Credentials introduced by a selected provider are declared on that profile
  in `agent-dependencies.yaml`.
- OAuth provider endpoints, PKCE/DCR behavior, and the provider's allowed scope
  configuration remain in `oauth-providers.yaml`.
- A blueprint selects profiles only with `x-rustic-profile`. A `single`
  selection is a profile-key string; a `multiple` selection is an array of
  unique profile-key strings.

The same strict requirement shape is used in both catalogs:

```yaml
requirements:
  secrets:
    - key: SERP_API_KEY
      env: SERP_API_KEY
      label: SERP API Key
      optional: false
  oauth:
    - provider: google
      env: GOOGLE_ACCESS_TOKEN
      label: Google Account
      scopes:
        - requested.scope
      optional: false
```

`key` is the organization-scoped secret-store key. `env` is the process
environment variable and defaults to `key` for secrets. OAuth declarations
must provide `env`. `label` is presentation text only. `optional` defaults to
false.

Scalar requirements, unknown fields, invalid environment names, duplicate or
conflicting declarations, unknown OAuth providers, and scopes not permitted by
the provider configuration are startup errors. Project IDs, regions, endpoint
URLs, deployment names, and model names are ordinary configuration—not
secrets—and stay in profile properties or launch configuration.

## Ownership examples

- `SERPAgent` declares `SERP_API_KEY` in the agent registry.
- `ClaudeCodeAgent` declares `ANTHROPIC_API_KEY` in the agent registry.
- Provider-neutral `LLMAgent`, `ReActAgent`, and `MemoryAgent` declare no
  provider credentials.
- `llm_gemini` declares `GEMINI_API_KEY`.
- OpenAI chat, embedding, and Uniko profiles declare `OPENAI_API_KEY`.
- Local LLM, local embedding, and local Uniko profiles declare no credentials.

A generic agent never declares the union of every provider's keys. Its bundled
usage must select the provider through a dependency profile.

## Resolution and injection

Forge persists selected profile keys as internal profile provenance when it
materializes a guild. For each agent, one resolver combines:

1. the agent class requirement from the registry; and
2. the requirements on that agent's selected profiles.

Secrets deduplicate by storage key. OAuth requirements deduplicate by provider
and union their scopes. A requirement is mandatory if any source makes it
mandatory. Conflicting labels or environment targets fail closed. Resolution
retains agent and profile provenance so the launch UI can explain why a
credential is needed.

Preflight and spawn call this same resolver. Spawn recomputes requirements from
the stored guild and Forge-owned profile provenance; it does not trust a
browser-supplied credential list. Static secrets are read only from the
organization-scoped store. OAuth access tokens are read through Forge's OAuth
manager and injected using the declared `env`. Optional missing values are
omitted; mandatory missing values reject spawn with a typed credential error.

Raw credential values are never placed in guild specs, persisted profile
provenance, launch metadata, fingerprints, logs, or API responses. Ambient or
global environment variables are not a fallback secret source.

## Mandatory launch preflight

`GET /rustic/capabilities` returns contract version `1` and advertises:

- `launch_requirements_v2`
- `dependency_profiles_v1`

Studio requires that floor. Missing or incomplete capabilities are a deployment
configuration error, not a signal to use another launch path.

Before launch, Studio sends the intended stable guild ID, name, user,
organization, and profile-key configuration to:

```text
POST /rustic/catalog/blueprints/{blueprint_id}/guilds/preflight
```

Forge runs the authoritative launch materializer and returns an opaque
preflight ID, executable-plan fingerprint, ten-minute expiry, readiness, and
aggregated requirements. Public requirement entries contain only an opaque ID,
kind, label, optional flag, status, provenance, OAuth scope information, and an
opaque remediation action. Secret-store keys, OAuth provider storage keys, and
environment targets stay server-side.

Secret actions set or replace the captured organization-scoped secret. OAuth
actions use only the provider and scopes captured in the preflight record, and
Forge derives the callback URL server-side. After remediation Studio runs a new
preflight.

Launch requires the exact `preflight_id` and `fingerprint`. Forge rematerializes
and reevaluates the request. Missing fields return `422`; expired, mismatched,
changed, or no-longer-ready preflights return `412` with the current requirement
snapshot. Preflight records are bounded, process-local, expire after ten
minutes, and are intentionally not portable across Forge instances or restarts.

## Studio flow

The app-launch wizard has three steps:

1. **Configure** selects profiles by stable key. Any change invalidates the
   previous preflight and clears transient credential inputs.
2. **Requirements** displays configured, missing, optional,
   insufficient-scope, and unavailable-provider states with agent/profile
   provenance. Secret values are submitted through password fields and cleared
   immediately. OAuth opens the system browser and Studio refreshes preflight.
3. **Ready** summarizes the exact profiles and satisfied requirements, then
   launches with the matching preflight ID and fingerprint.

The existing Secrets and Integrations pages remain the ongoing-management
surfaces. Desktop and hosted UI use the same `/rustic` contract.

## Boundaries

This credential preflight covers secrets and OAuth only. MCP tools, network and
filesystem approvals, distributed preflight storage, asynchronous launch, and
hosted token validation are separate workstreams. Dependency profile
availability remains a runtime/worker concern; organization credential state is
reported by preflight and does not make a profile disappear from the catalog.
