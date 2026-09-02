# Secrets & OAuth

Forge brokers every credential used by an agent. Agents do not read Forge's
secret or OAuth stores. Forge resolves the exact requirements for each agent
and injects only the declared values into that process.

## Declaration ownership

Agent-class requirements live in `forge-agent-registry.yaml`. Requirements
introduced by a provider live on its profile in `agent-dependencies.yaml`:

```yaml
requirements:
  secrets:
    - key: OPENAI_API_KEY
      env: OPENAI_API_KEY
      label: OpenAI API Key
      optional: false
  oauth:
    - provider: google
      env: GOOGLE_ACCESS_TOKEN
      label: Google Account
      scopes: [requested.scope]
      optional: false
```

`key` identifies an organization-scoped stored secret. `env` is the variable
the selected agent receives. `label` is display text only. OAuth providers and
their permitted configuration are defined separately in
`oauth-providers.yaml`.

Generic classes such as `LLMAgent` do not declare every provider key. Selecting
an OpenAI, Gemini, or local profile contributes only that profile's
requirements. Local profiles therefore remain credential-free.

Forge rejects scalar declarations, unknown fields, invalid environment names,
duplicate or conflicting declarations, unknown OAuth providers, and scopes not
permitted by provider configuration when it starts.

## Launch preflight

Rustic UI first sends the intended launch configuration to:

```text
POST /rustic/catalog/blueprints/{blueprint_id}/guilds/preflight
```

The response contains opaque requirement IDs, user-facing labels, status,
agent/profile provenance, and opaque remediation actions. It never exposes
secret-store keys, environment targets, OAuth storage keys, or values.

Secret actions create or replace the captured organization-scoped secret.
OAuth actions request only the captured scopes and Forge derives its callback
URL. Launch then requires the matching short-lived preflight ID and executable
plan fingerprint. Forge rematerializes and reevaluates the launch, so browser
state cannot bypass credential checks.

## Runtime injection

Preflight and spawn use the same resolver. Requirements are recomputed from the
stored agent and Forge-owned selected-profile provenance. Static values are
resolved only by the organization-scoped key
`secret:<organization>|<key>`. OAuth access tokens are obtained through the
OAuth manager using `oauth:<organization>|<provider>` and injected under each
declaration's `env`.

Optional missing credentials are omitted. A missing mandatory credential
rejects spawn. Forge does not fall back to an unscoped environment variable or
to `AgentSpec.resources.secrets`.

!!! warning "Environment injection boundary"
    An injected value is available to the selected agent process and its child
    processes. Keep agent requirements narrow and supervise untrusted workloads
    with an appropriate isolation runtime.

## OAuth behavior

Forge performs Authorization Code + PKCE, holds pending flows for ten minutes,
persists tokens in the configured token store, and refreshes expiring access
tokens. `oauth-providers.yaml` owns endpoints, PKCE/DCR behavior, and allowed
scopes; each agent/profile requirement owns the smaller scope set needed for
that launch.

The ongoing management routes are:

```text
GET    /rustic/oauth/organizations/{org_id}/providers
POST   /rustic/oauth/organizations/{org_id}/providers/{provider_id}/authorize
GET    /rustic/oauth/organizations/{org_id}/providers/{provider_id}/callback
GET    /rustic/oauth/organizations/{org_id}/providers/{provider_id}/status
DELETE /rustic/oauth/organizations/{org_id}/providers/{provider_id}
```

Launch remediation uses separate opaque preflight-action routes, so the browser
does not choose a provider or scope set after preflight.

See [Securing Secrets & OAuth](../guides/securing-secrets.md) for deployment
configuration.
