# Securing Secrets & OAuth

Forge is the credential broker for spawned agents. Configure catalog
requirements, secure persistence, and OAuth at the Forge deployment boundary;
do not put raw values in blueprints or agent specs.

## Declare requirements

An agent-class credential belongs in `forge-agent-registry.yaml`:

```yaml
rustic_ai.serp.agent.SERPAgent:
  requirements:
    secrets:
      - key: SERP_API_KEY
        env: SERP_API_KEY
        label: SERP API Key
        optional: false
```

A provider credential belongs on its `agent-dependencies.yaml` profile:

```yaml
llm_openai:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM
  catalog:
    display_name: OpenAI
    provider: openai
    capabilities: [chat, tools]
    selectable: true
  requirements:
    secrets:
      - key: OPENAI_API_KEY
        env: OPENAI_API_KEY
        label: OpenAI API Key
        optional: false
  properties:
    model: gpt-5.4
```

Use only structured entries. Secret `env` defaults to `key`; OAuth `env` is
required. Keep regions, projects, endpoints, deployments, and model IDs in
ordinary profile properties. A generic `LLMAgent` should have no provider
credentials of its own.

## Store static secrets

Studio's Requirements step and organization Secrets page call Forge's managed
secret API. Forge stores each value under the organization-scoped identity
`secret:<organization>|<key>` and its read APIs return names/status only, never
values.

The default production store is the operating-system keychain. The in-memory
store is test-only. A custom deployment may supply an equivalent
organization-aware manager/store, but agent spawn does not fall back to an
unscoped server environment variable.

## Configure OAuth

Provider mechanics live in `oauth-providers.yaml`:

```yaml
providers:
  google:
    display_name: Google
    description: Connect a Google account
    auth_url: https://accounts.google.com/o/oauth2/v2/auth
    token_url: https://oauth2.googleapis.com/token
    scopes:
      - requested.scope
    use_pkce: true
```

The provider's `scopes` are an allowlist. Agent and dependency declarations
request their own subset. Unknown providers or undeclared scopes fail Forge
startup rather than silently disabling a catalog entry.

Set `FORGE_OAUTH_PROVIDERS_CONFIG` to use a non-default provider file and
`FORGE_OAUTH_TOKEN_STORE` to select the token store. Forge uses Authorization
Code + PKCE and supports configured dynamic client registration. Providers
that require a client ID and secret ask for them when the user connects; those
credentials are retained with the token entry for refresh.

Forge derives launch-remediation callback URLs from its configured public base
URL. Do not accept arbitrary callback overrides at the deployment edge. Set the
Forge public/base URL to the externally reachable HTTPS origin before enabling
OAuth.

## Preflight and least privilege

Rustic UI selects dependency profiles and asks Forge to preflight the exact
launch. Forge combines agent-class and selected-profile requirements,
deduplicates shared credentials, unions OAuth scopes, and reports status with
provenance. A profile stays selectable when its credential is missing; the
Requirements step explains and remediates the missing item.

Launch requires the returned preflight ID and fingerprint. Spawn recomputes the
same requirements and injects each value only into affected agents. Optional
missing values are omitted; mandatory missing values reject spawn.

Operational rules:

- protect all credential-management and preflight routes with the same user and
  organization authorization policy as launch;
- terminate TLS at a trusted deployment boundary;
- keep manager/control APIs private;
- never log request bodies on credential routes;
- use the narrowest OAuth scopes and rotate static API keys regularly;
- treat agent environment variables as sensitive process data.

See [Secrets & OAuth](../features/secrets-oauth.md) for the runtime contract and
[Rustic UI with External Forge](rustic-ui-external-forge.md) for deployment
topology.
