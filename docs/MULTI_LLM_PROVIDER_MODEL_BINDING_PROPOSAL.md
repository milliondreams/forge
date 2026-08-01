# Blueprint Dependency Inputs and Multi-LLM Binding Proposal

## Decision

Add first-class dependency inputs to blueprint configuration schemas. A
dependency input stores a configured dependency profile key, not a model name,
resolver class, base URL, or resolver properties.

Forge resolves the selected profile, verifies its type against the target
agent dependency, and snapshots the resolver spec into the launched guild.
Rustic UI renders the input as a dropdown using safe catalog metadata returned
by Forge.

For LLMs, each selectable profile is atomic:

```text
profile = provider + model + endpoint configuration + credential requirements
```

Examples:

```text
llm_openai_gpt_5_4
llm_anthropic_sonnet_4_6
llm_vertex_gemini_3_1
llm_local_qwen3_5_2b
```

An agent does not independently select a provider and a model. This prevents
invalid combinations such as sending a GPT model to a local llama.cpp endpoint
or retaining a Vertex configuration while changing only the model string.

The target-state example is
`docs/multi_llm_blueprint.proposed.json`.

## Validated Current State

This proposal was checked against:

- Forge Go in `/Users/rohit/Work/forge/forge-go`.
- Forge Python in `/Users/rohit/Work/forge/forge-python`.
- Rustic AI in `/Users/rohit/Work/rustic-ai`.
- Rustic UI/Studio in `/Users/rohit/Work/rustic-ui`.

### What already exists

- `LaunchGuildFromBlueprintRequest` already accepts
  `dependency_bindings` in `forge-go/api/catalog_dto.go`.
- `applyBlueprintDependencyBindings` already validates a provider against an
  agent dependency and copies the configured `DependencySpec` into the target
  agent or guild.
- `GET /dependencies` already supports a `provided_type` query.
- `GET /catalog/blueprints/{id}/dependencies` already attempts to resolve
  providers for every agent dependency.
- Binding materialization snapshots nested resolver properties, including
  `conf.base_url`.
- `LLMAgent` passes `properties.model` as an optional override. When the agent
  model is absent, `LiteLLM` uses the model configured by its resolver.
- Forge's local model-fit catalog already contains useful display, capability,
  and hardware-fit metadata for local models.

### What prevents the current mechanism from working end to end

1. The embedded Forge OpenAPI does not publish `dependency_bindings` or the
   dependency discovery routes. Rustic UI's generated client therefore cannot
   call them with generated types.
2. Rustic AI's `AgentDependency` does not publish the required dependency
   type. Forge's `resolved_type` field exists only in the Go DTO and focused
   test fixtures; the real `agents.json` entries do not contain it.
3. Forge's execution registry seeding registers agent classes without schemas
   or dependency metadata and uses create-only conflict behavior. It can block
   later registration of richer metadata for the same class.
4. Rustic UI's bundled `agent-dependencies.yaml` has no `provided_type`
   fields. Its configured LLM profiles are therefore invisible to
   type-filtered discovery.
5. Studio creates the user's dependency file only when it does not exist.
   Updating the bundled file does not repair existing user configurations.
6. Several Forge LLM profiles, including Anthropic and Vertex, also omit
   `provided_type`.
7. The configured-dependency API returns raw resolver properties. That is not
   an appropriate general UI contract because future resolver properties may
   contain private endpoints or credentials.
8. Rustic UI's launch form renders only `configuration_schema.properties`,
   marks every property required, and submits only `configuration`.
9. Forge persists execution and messaging projections but drops arbitrary
   `GuildSpec.properties`. Dynamic selection policies would be lost on a
   manager refresh or restart.
10. Forge bootstrap and Rustic AI's guild helper merge the complete
    installation dependency file into the guild dependency map. Unselected
    profiles can therefore travel with every guild even when no agent needs
    them.
11. Forge Python's dynamic `AgentLaunchRequest` handler accepts a complete
    caller-supplied `AgentSpec` and launches it without dependency-profile
    validation.
12. The current Multi-LLM blueprint uses model strings as agent IDs, mixes
    model namespaces, and prepends `vertex_ai/` to unrestricted classifier
    output.
13. `AgentLaunchRequest` uses Pydantic's default `extra="ignore"`. If a
    producer sends the proposed `dependency_selections` field to current code,
    validation succeeds and silently removes the field before the Guild
    Manager sees it.
14. The configured Forge profiles use the noncanonical type string
    `rustic_ai.core.llm.LLM`. The actual annotated class resolves to
    `rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM`.
15. Forge's bundled `agents.json` incorrectly marks `LLMAgentConfig.model` as
    required even though the runtime Pydantic model defines it as optional.

The existing APIs are useful foundations, but they do not yet establish a
working or secure platform contract.

## Platform Vocabulary

### Dependency requirement

An agent processor declares a dependency key and the type it requires:

```json
{
  "dependency_key": "llm",
  "dependency_var": "llm",
  "agent_level": true,
  "required_type": "rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM"
}
```

`required_type` is authoritative agent metadata. Forge should accept the
existing `resolved_type` field as a temporary read alias, but new APIs and
generated registries should use `required_type`.

Rustic AI should derive this value from the annotated processor argument when
possible. For example, `llm: LLM` on a processor using `depends_on=["llm"]`
resolves to
`rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM`. An explicit value must
be supported for forward references or ambiguous handlers. Type matching is
exact string equality; shortened aliases are not interchangeable unless Forge
introduces and validates an explicit alias registry.

### Configured dependency profile

A configured profile provides one dependency type and includes private runtime
properties plus safe catalog metadata:

```yaml
llm_openai_gpt_5_4:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM
  catalog:
    display_name: OpenAI GPT-5.4
    description: OpenAI-hosted general-purpose model
    provider: openai
    capabilities: [chat, tools]
    aliases: [gpt-5.4, gpt]
    selectable: true
    requirements:
      secrets: [OPENAI_API_KEY]
  properties:
    model: gpt-5.4
```

The profile key is an opaque, stable identifier. `properties` are used only
for server-side materialization and are never returned by the public catalog
API.

For a local profile:

```yaml
llm_local_qwen3_5_2b:
  class_name: rustic_ai.litellm.agent_ext.llm.LiteLLMResolver
  provided_type: rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM
  catalog:
    display_name: Qwen 3.5 2B Balanced
    provider: local
    capabilities: [chat, coding, reasoning]
    aliases: [qwen-2b, qwen-3.5-2b]
    selectable: true
  properties:
    model: openai/rustic/qwen3.5-2b-balanced
    conf:
      base_url: http://localhost:55262/v1
```

Forge should enrich local profile responses with the existing model-fit result
rather than create a second local-model catalog.

### Blueprint dependency input

A blueprint dependency input is a JSON Schema property annotated with
`x-rustic-dependency`. Unknown `x-` annotations remain valid JSON Schema and
ordinary schema tooling can ignore them.

Static agent binding:

```json
{
  "model_1": {
    "type": "string",
    "title": "Model 1",
    "description": "LLM used by Model 1",
    "x-rustic-dependency": {
      "selection": "single",
      "required_type": "rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM",
      "filters": {
        "capabilities": ["chat"]
      },
      "target": {
        "kind": "agent_dependency",
        "agent_id": "model-agent-1",
        "dependency_key": "llm"
      }
    }
  }
}
```

Runtime allowlist for dynamic agents:

```json
{
  "dynamic_models": {
    "type": "array",
    "title": "Models available to add later",
    "items": {
      "type": "string"
    },
    "uniqueItems": true,
    "minItems": 1,
    "x-rustic-dependency": {
      "selection": "multiple",
      "required_type": "rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM",
      "filters": {
        "capabilities": ["chat"]
      },
      "target": {
        "kind": "runtime_catalog",
        "catalog_key": "dynamic_models",
        "dependency_key": "llm"
      }
    }
  }
}
```

The field value is a profile key or list of profile keys. It is safe to place
in the ordinary configuration bag. The blueprint never templates that key into
an agent's `model`, ID, resolver class, or base URL.

`required_type` makes the blueprint portable and inspectable, but it is not the
trust anchor. Forge resolves the target agent dependency from the registered
agent metadata and rejects the blueprint if the two required types differ.

## API Contract

### Safe dependency catalog

Publish the existing route in OpenAPI and change its public response to a safe
catalog DTO:

```http
GET /catalog/dependencies?provided_type=rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM&capability=chat
```

```json
[
  {
    "key": "llm_openai_gpt_5_4",
    "display_name": "OpenAI GPT-5.4",
    "description": "OpenAI-hosted general-purpose model",
    "provided_type": "rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM",
    "provider": "openai",
    "capabilities": ["chat", "tools"],
    "aliases": ["gpt-5.4", "gpt"],
    "availability": {
      "status": "ready",
      "reasons": []
    }
  }
]
```

Do not return `class_name`, resolver `properties`, `base_url`, API keys,
credential paths, or arbitrary configuration values.

Availability is distinct from configuration:

- `ready`: requirements are present and any required local service is healthy.
- `needs_configuration`: required secrets or settings are missing.
- `unavailable`: the local runtime or required capability is not available.
- `unknown`: Forge cannot safely determine readiness.

Authorization must filter profiles by organization and user visibility when
dependency configuration becomes scoped. The current file-backed catalog is
installation-scoped.

### Blueprint-aware launch inputs

Add:

```http
GET /catalog/blueprints/{blueprint_id}/launch-inputs
```

The endpoint parses the schema annotations, resolves authoritative agent
requirements, applies blueprint filters, and returns options:

```json
{
  "dependency_inputs": {
    "model_1": {
      "title": "Model 1",
      "required": true,
      "selection": "single",
      "required_type": "rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM",
      "target": {
        "kind": "agent_dependency",
        "agent_id": "model-agent-1",
        "dependency_key": "llm"
      },
      "options": [
        {
          "key": "llm_local_qwen3_5_2b",
          "display_name": "Qwen 3.5 2B Balanced",
          "provider": "local",
          "capabilities": ["chat", "coding", "reasoning"],
          "availability": {
            "status": "ready",
            "reasons": []
          }
        }
      ]
    }
  }
}
```

This is the launch UI's primary endpoint. The generic dependency endpoint is
useful for administration and blueprint authoring but should not make the UI
reconstruct blueprint binding semantics.

### Launch request

Rustic UI submits dependency selections as ordinary configuration:

```json
{
  "guild_name": "My Multi-LLM Guild",
  "user_id": "user-1",
  "org_id": "org-1",
  "configuration": {
    "model_1": "llm_openai_gpt_5_4",
    "model_2": "llm_local_qwen3_5_2b",
    "dynamic_models": [
      "llm_openai_gpt_5_4",
      "llm_local_qwen3_5_2b"
    ]
  }
}
```

Forge derives bindings from the schema. Rustic UI does not submit a second,
potentially inconsistent model-to-provider map.

Keep the existing `dependency_bindings` request field for programmatic and
legacy callers. If an explicit binding overlaps a schema-derived binding,
Forge must require equality or reject the request. Schema-derived dependency
inputs are the preferred blueprint contract.

## Blueprint Validation

At blueprint creation or update, Forge must:

1. Validate the ordinary JSON Schema.
2. Parse every `x-rustic-dependency` annotation.
3. Require a stable, literal target agent ID. Mustache expressions are not
   valid dependency targets.
4. Require the target agent and dependency key to exist in the registered
   agent metadata.
5. Require the annotation's `required_type` to equal the registered
   `required_type`.
6. Require `single` inputs to use JSON Schema type `string`.
7. Require `multiple` inputs to use an array of unique strings.
8. Validate referenced runtime catalog keys.
9. Reject duplicate inputs targeting the same agent dependency unless they are
   explicitly declared aliases.
10. Validate configured default values when present, but do not require a
    portable blueprint to provide defaults for every required launch field.

The last rule changes the current behavior in which the blueprint's default
`configuration` must independently satisfy the complete schema. `required`
should be enforced against the merged configuration at launch.

## Launch Materialization

Forge performs these steps after merging and validating launch configuration:

1. Resolve every selected profile from the authorized configured-dependency
   catalog.
2. Check `provided_type == required_type`.
3. Apply capability and optional profile allowlist filters.
4. Reject missing, disabled, unavailable, or duplicate selections as defined
   by the input.
5. For `agent_dependency`, copy the profile's runtime `DependencySpec` into
   `agent.dependency_map[dependency_key]`.
6. For `runtime_catalog`, snapshot every selected profile under generated
   internal guild dependency keys.
7. Store only the mapping and safe public metadata in
   `GuildSpec.properties.dependency_selections`.
8. Materialize legacy dependencies only for dependency keys actually required
   by the guild's agents, additional dependencies, and resolver injection
   graph. A requirement with no explicit selection continues to resolve the
   configured profile having the same key, such as `llm` or `filesystem`.
9. Do not merge unrelated configured profiles into the guild dependency map.
10. Strip catalog-only metadata before starting Python resolver code.
11. Persist the complete guild properties and dependency map.

Snapshot semantics are intentional. Editing the installation's dependency file
after guild launch must not silently change an existing guild's model or
endpoint. Secrets can still be resolved from the environment or secret manager
at runtime.

Selective materialization is also intentional. The configured dependency file
is a control-plane catalog, not a guild-level dependency map. This replaces
Forge's current merge-all behavior while keeping non-annotated blueprints
compatible through requirement-key fallback.

An example persisted dynamic selection:

```json
{
  "properties": {
    "dependency_selections": {
      "dynamic_models": {
        "required_type": "rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM",
        "profiles": {
          "llm_openai_gpt_5_4": {
            "dependency_key": "__selection_dynamic_models_0",
            "display_name": "OpenAI GPT-5.4",
            "aliases": ["gpt-5.4", "gpt"]
          }
        }
      }
    }
  },
  "dependency_map": {
    "__selection_dynamic_models_0": {
      "class_name": "rustic_ai.litellm.agent_ext.llm.LiteLLMResolver",
      "properties": {
        "model": "gpt-5.4"
      }
    }
  }
}
```

## Dynamic Agent Contract

Keep the existing
`rustic_ai.core.agents.system.models.AgentLaunchRequest`. Add one optional,
backward-compatible field:

```json
{
  "agent_spec": {
    "name": "Dynamic Model",
    "description": "Dynamically selected LLM responder",
    "class_name": "rustic_ai.llm_agent.llm_agent.LLMAgent",
    "listen_to_default_topic": true,
    "act_only_when_tagged": true,
    "properties": {
      "send_response": true
    }
  },
  "dependency_selections": {
    "llm": {
      "catalog_key": "dynamic_models",
      "selector": "gpt-5.4"
    }
  }
}
```

In Rustic AI core this can be represented as:

```python
class DependencySelection(BaseModel):
    model_config = ConfigDict(extra="forbid")

    catalog_key: str
    selector: str


class AgentLaunchRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")

    agent_spec: AgentSpec
    dependency_selections: dict[str, DependencySelection] = Field(
        default_factory=dict
    )
```

The new field is backward-compatible for conforming existing requests.
Rejecting unknown fields is an intentional hardening change: current Pydantic
behavior silently discards misspelled or unsupported control-plane fields.
Compatibility tests must identify any existing producer that relies on sending
unknown fields before enabling `extra="forbid"`.

Forge's existing `GuildManagerAgent.launch_agent` handles the optional field:

1. Resolve `catalog_key` only from runtime catalogs persisted in the guild's
   launch snapshot.
2. Match `selector` against profile key, display name, and aliases using
   normalized case-insensitive comparison.
3. Reject unknown or ambiguous selectors and deduplicate repeated selections.
4. Verify the selected profile provides the dependency type required by the
   requested agent class and dependency key.
5. Copy the snapshotted `DependencySpec` to
   `agent_spec.dependency_map[dependency_key]`.
6. Replace the provisional generated ID and fixed name with a NATS-safe stable
   ID and unique display name derived from the resolved profile. Prefer the
   catalog display name; append a deterministic profile-key suffix if that name
   is already used in the guild.
7. Persist the fully materialized agent before launching it.
8. Treat an identical existing materialized spec as idempotent and the same ID
   with a different spec as a conflict.
9. Record launch lifecycle and failures through the existing Forge manager
   state and metastore path.

The selector cannot introduce a profile outside the launch-time allowlist.
The Splitter output must not supply a resolver class, resolver properties,
model, base URL, or dependency map. Forge replaces its provisional identity,
so unrestricted LLM text never becomes an agent ID or NATS subject.

Step 6 is load-bearing. `UserProxyAgent` indexes tags by both agent ID and
agent name. If multiple dynamic agents retain the fixed provisional name
`Dynamic Model`, or if two profiles materialize to the same display name,
later entries overwrite earlier entries and only one agent is reachable by
that name. Forge must guarantee guild-local uniqueness and complete identity
replacement before persistence, launch, and guild-refresh announcement.

This is deliberately not a new message type or a new system agent. It extends
the request already consumed by the existing Forge Guild Manager.

### Why not emit `dependency_map` directly?

`AgentSpec.dependency_map` already overrides the guild dependency map at
runtime, so a trusted blueprint could mechanically emit a complete resolver
spec today. The proposal intentionally does not use that shortcut. It would
put resolver class selection, model names, endpoint configuration, and
potentially private properties into a message assembled from classifier
output. It would also bypass the launch-time profile allowlist and duplicate
server-side catalog validation in JSONata.

The cross-repository `dependency_selections` extension is justified because it
keeps the dynamic message declarative: the message names an approved selector,
while Forge remains the only component allowed to materialize resolver
configuration.

## Multi-LLM Blueprint

The proposed blueprint makes these concrete changes:

- Preserve `classifier_agent` as the `User Input Analyser`.
- Preserve `splitter-agent` as the existing `SplitterAgent`.
- Use stable IDs `model-agent-1` and `model-agent-2` for the starter LLM
  agents.
- Give the `User Input Analyser` its own dependency input, so its LLM is
  selected explicitly instead of inherited accidentally.
- Model 1 and Model 2 are dependency inputs bound to each starter agent's
  `llm` dependency.
- Dynamic models are a multi-select dependency input snapshotted as a runtime
  catalog.
- Starter and dynamic LLM agents omit `properties.model` and `base_url`; the
  selected resolver profile supplies both.
- The `User Input Analyser` extracts model selectors and publishes its
  `ChatCompletionResponse` to `SPLIT`.
- `SplitterAgent` emits one extended `AgentLaunchRequest` per selector.
- The existing Forge Guild Manager validates, materializes, persists, and
  launches each dynamic agent.
- The analyser-to-split step is explicitly a `content_based_router` because it
  changes topics, format, and payload.
- Guild Manager acknowledgements produce a schema-valid synthetic
  `ChatCompletionResponse`, including response identity, model, timestamp,
  choice index, and finish reason.
- The current literal `@User Input Analyser` content router remains in this
  proposal so the dependency work does not also redesign tagging semantics.
  Moving this to structured recipient tagging is a separate change.
- There is no unconditional `vertex_ai/` prefix.
- There are no model strings in agent IDs.
- The shared-memory wrapper and incorrect user-role response storage are
  removed. Memory can be added later with explicit user-request and assistant
  response routes.

The target blueprint can be accepted, launched, and run by the current server,
but it silently produces incorrect dependency behavior. Current
`AgentLaunchRequest` validation removes `dependency_selections`; Forge
bootstrap then merges the installation's default `llm` dependency into the
guild. The analyser, both starter agents, and every dynamic agent can therefore
resolve to the same default model while appearing to represent different
selections.

This is more dangerous than a create-time rejection because the guild can
answer confidently without signaling that selection was ignored. Do not
publish or seed the target blueprint until strict request parsing,
schema-derived static binding, runtime-catalog materialization, and Forge Guild
Manager selection resolution are deployed together.

### Target blueprint validation

| Check | Result |
| --- | --- |
| JSON syntax | Pass |
| Unique, literal, NATS-safe static agent IDs | Pass |
| Every static dependency target names an existing blueprint agent | Pass |
| Single and multiple input cardinality matches JSON Schema type | Pass |
| Starter LLM agents omit model and base URL overrides | Pass |
| Default local profile keys exist in Forge configuration | Pass |
| Default local Forge profiles declare the canonical LLM provided type | Fails today; migration required |
| Default local profiles exist in Studio configuration | Pass |
| Default local Studio profiles declare the canonical LLM provided type | Fails today; migration required |
| LLMAgent registry publishes required types | Fails today; Rustic AI metadata change required |
| LLMAgent registry marks optional `model` as optional | Fails today; registry regeneration required |
| `User Input Analyser` remains an `LLMAgent` | Pass |
| `SplitterAgent` class and `AgentLaunchRequest` route exist | Pass |
| `AgentLaunchRequest.dependency_selections` exists | Fails today; Rustic AI core change required |
| Unknown `AgentLaunchRequest` fields are rejected | Fails today; fields are silently ignored |
| Forge Guild Manager resolves dependency selections | Fails today; Forge Python change required |
| Dynamic identity is replaced before guild refresh | Fails today; Forge Python change required |
| Synthetic launch response validates as `ChatCompletionResponse` | Pass |
| OpenAPI publishes dependency inputs and bindings | Fails today; Forge OpenAPI work required |
| Forge persists arbitrary guild properties | Fails today; store migration required |

## Repository Changes

### Rustic AI core and API

- Add `required_type` to `AgentDependency`.
- Derive it from processor parameter annotations and support an explicit
  override.
- Include it in `AgentRegistry` serialization and regenerate agent metadata.
- Regenerate the `LLMAgent` property schema so optional
  `LLMAgentConfig.model` is not listed in `required`.
- Define and document `x-rustic-dependency` as a portable blueprint extension.
- Add safe configured-dependency profile metadata to the Python configuration
  loader if the legacy Rustic AI API must support the same Studio launch UX.
- Add a resolver regression proving that an absent agent model uses the
  resolver model and retains `conf.base_url`.
- Add optional `dependency_selections` to the existing `AgentLaunchRequest`;
  do not introduce a parallel launch request type.
- Set `extra="forbid"` on `AgentLaunchRequest` and `DependencySelection` so
  unsupported or misspelled launch controls fail validation instead of being
  dropped.

Rustic AI runtime `DependencySpec` should remain limited to `class_name` and
`properties`; catalog metadata is control-plane data.

### Forge Go

- Extend configured dependency parsing with catalog metadata and validation.
- Migrate every LLM `provided_type` to the canonical
  `rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM` identifier.
- Add startup/CI validation for `provided_type`, catalog metadata, duplicate
  aliases, and LLM model/profile consistency.
- Reconcile the execution registry and agent introspection catalog. Catalog
  registration must upsert or merge richer schemas and dependency metadata
  instead of allowing an empty create-only seed to win.
- Parse and validate dependency input annotations.
- Add the safe catalog and blueprint launch-input endpoints.
- Materialize single bindings and runtime catalogs.
- Replace merge-all dependency loading with requirement-driven materialization
  while preserving legacy dependency-key fallback.
- Preserve arbitrary `GuildSpec.properties` in `GuildModel`.
- Keep profile snapshots across manager refresh and restart.
- Publish every route and DTO in the embedded OpenAPI and regenerate
  `api/contract/gen.go`.
- Keep the old raw configured-dependency response internal or remove raw
  properties from it before making it public.

### Forge Python

- Extend the existing Forge `GuildManagerAgent.launch_agent` handler to
  validate and materialize optional dependency selections.
- Resolve selectors only against the guild's snapshotted runtime catalog.
- Refactor ordinary and dependency-selected launches through one
  persistence-first lifecycle helper.
- Retain the existing `User Input Analyser` and `SplitterAgent`; do not add
  another launcher agent.
- Never accept a user-supplied resolver spec, model, base URL, or dynamic agent
  ID on this path.

### Rustic UI/Studio

- Regenerate the API client from Forge OpenAPI.
- Fetch blueprint launch inputs when opening the launch dialog.
- Convert resolved dependency options into Uniforms select fields while
  retaining profile keys as form values.
- Support single and multi-select dependency inputs.
- Respect the schema's actual `required` array rather than requiring every
  property.
- Show provider, capability, local fit, and availability information.
- Disable unavailable options and explain missing prerequisites.
- Submit only ordinary configuration selections.
- Replace the hardcoded, obsolete dependency resolver mapping in the blueprint
  authoring form with catalog-backed dependency input authoring.
- Version and migrate the user's `agent-dependencies.yaml`; `flag: "wx"` alone
  cannot backfill `provided_type` or catalog metadata.
- Package one canonical versioned dependency profile template rather than
  maintaining divergent Forge and Studio copies.

## Configuration Migration

1. Add `provided_type` to every configured dependency, not only LLM entries.
2. Add catalog metadata to selectable profiles.
3. Split provider-wide profiles when they expose multiple models. Version 1
   uses one model per profile.
4. Mark embedding-only profiles with capabilities that exclude `chat`.
5. Migrate existing Studio user files by merging missing metadata without
   overwriting user-edited resolver properties.
6. Preserve unknown user profiles and report invalid entries in diagnostics.
7. Record a configuration schema version in the user file or adjacent state.

The bundled Multi-LLM defaults use existing local Qwen profile keys so Studio
can launch without cloud credentials after migration.

## Security and Ownership

- Dependency properties are control-plane secrets/configuration and are never
  catalog response fields.
- Profile availability is evaluated server-side.
- Blueprint declarations cannot override registered required types.
- Direct API clients are subject to the same schema-derived validation as the
  UI.
- Dynamic requests can select only profiles snapshotted at guild launch.
- Dynamic selections are valid only for dependency keys declared by the
  requested agent class.
- Dynamic `AgentSpec` values are constrained by the blueprint's Splitter
  configuration, and Forge derives identity and dependency configuration from
  trusted catalog data.
- Unselected configured profiles are not copied into the guild spec.
- Guild launch snapshots profile configuration for reproducibility.
- Organization visibility and authorization belong in the dependency catalog,
  not in client-side filtering.
- Audit events should record profile keys and versions/fingerprints, never
  secret properties.

## Implementation Sequence

1. Add required dependency types to Rustic AI agent metadata and fix Forge
   catalog seeding/upsert. Use the canonical fully qualified LLM type and
   regenerate the LLMAgent schema with optional `model`.
2. Add `provided_type`, safe catalog metadata, validation, and Studio
   configuration migration.
3. Add safe dependency and blueprint launch-input APIs to Forge OpenAPI.
4. Add schema-derived static dependency materialization and persistence of
   complete guild properties.
5. Regenerate the Rustic UI client and implement generic dependency fields.
6. Convert the two static Multi-LLM agents and verify local/OpenAI/Vertex
   snapshots.
7. Add runtime catalog materialization and the optional dependency-selection
   path to the existing strict `AgentLaunchRequest` and Forge Guild Manager.
8. Update the existing `User Input Analyser` and `SplitterAgent` configuration
   and activate the proposed blueprint.
9. Remove or deprecate the old raw model string and unrestricted
   `AgentLaunchRequest` flow from the Multi-LLM seed.

Each phase should leave existing non-annotated blueprints working.

## Test Plan

### Contract and metadata

- Agent registry exports `LLMAgent.llm.required_type`.
- The exported LLM required type equals
  `rustic_ai.core.guild.agent_ext.depends.llm.llm.LLM`.
- Every LLM profile uses that same canonical `provided_type`.
- The generated LLMAgent property schema does not require `model`.
- Forge rejects a blueprint annotation whose required type differs from the
  registered agent requirement.
- Every selectable profile has `provided_type` and safe catalog metadata.
- Capability filters require the requested capabilities to be a subset of the
  profile capabilities.
- Studio migration adds missing metadata without overwriting user properties.
- Public catalog responses never contain resolver properties or base URLs.
- OpenAPI and generated Go/TypeScript clients remain in sync.

### Static launch

- Local Qwen selection snapshots its exact model and local base URL.
- OpenAI selection snapshots its model and has no local base URL.
- Vertex selection snapshots its model, project, and location configuration.
- Agent `properties.model` remains absent and the resolver model is used.
- Missing, wrong-type, unavailable, or unauthorized profiles are rejected.
- Explicit legacy bindings cannot conflict with schema-derived bindings.
- Stable agent IDs remain valid for NATS subjects and stream names.
- Guild properties and dependency snapshots survive a database round trip and
  manager restart.
- Unrelated configured profiles are absent from the persisted and spawned
  guild spec.

### Rustic UI

- Single and multi-select fields are populated from launch-input options.
- Defaults are applied when available.
- Required and optional fields follow JSON Schema.
- Unavailable profiles are disabled with a reason.
- The launch request contains profile keys only.
- No resolver properties are present in browser state or network responses.

### Dynamic launch

- Current-code regression fixture proves an unsupported
  `dependency_selections` field would otherwise be silently discarded.
- Updated `AgentLaunchRequest` rejects every unknown top-level field.
- `DependencySelection` rejects every unknown selection field.
- Existing conforming `AgentLaunchRequest(agent_spec=...)` producers remain
  valid after strict parsing is enabled.
- Only profiles selected in `dynamic_models` can be launched.
- Aliases are matched case-insensitively and ambiguity is rejected.
- Duplicate aliases across the selected runtime catalog are rejected at
  guild launch, before `User Input Analyser` can request them.
- Duplicate requested names launch once.
- Unknown names return a user-visible error without persisting an agent.
- Materialized dynamic agents have the same effective resolver spec as static
  agents using the same profile.
- Materialized dynamic agents have unique stable IDs and unique catalog-derived
  names before persistence and guild refresh.
- Duplicate catalog display names receive deterministic suffixes and remain
  independently taggable.
- Every materialized dynamic agent is reachable by both its ID tag and name
  tag.
- Identical retries are idempotent; conflicting specs are rejected.
- Persistence failure prevents launch.
- Launch failure records `error`.
- Restart preserves the dynamic agent's model and endpoint.

### Multi-LLM message flow

- A structured tag for Model 1 invokes only Model 1.
- A structured tag for Model 2 invokes only Model 2.
- Literal `@Model 1` text without a recipient tag does not bypass
  `act_only_when_tagged`.
- A message containing `@User Input Analyser` is routed to `CLASSIFY` and
  addressed to `classifier_agent`.
- `User Input Analyser` output is routed to `SPLIT`.
- The analyser response route deserializes as `FunctionalTransformer` and is
  declared with `style: "content_based_router"`.
- `SplitterAgent` emits one extended `AgentLaunchRequest` per parsed selector.
- The existing Forge Guild Manager launches only selectors resolved from the
  guild's `dynamic_models` snapshot.
- LLM responses route to `user_message_broadcast`.
- Guild Manager launch acknowledgements route to the user.
- The transformed Guild Manager acknowledgement validates as
  `ChatCompletionResponse`.
- No classifier response is exposed as an assistant answer.

## Acceptance Criteria

The feature is complete when a single generic launch form can discover and
render typed dependency inputs, Forge can validate and snapshot selections
without exposing private resolver configuration, and the Multi-LLM guild
produces equivalent effective LLM configuration for static and dynamically
created agents from the same profile.
