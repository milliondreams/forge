# Local LLM Speaker-Aware Template Coverage

## Status

Proposal. Companion to
[`SPEAKER_AWARE_MULTI_MODEL_HISTORY_DESIGN.md`](SPEAKER_AWARE_MULTI_MODEL_HISTORY_DESIGN.md).

## Summary

Provide speaker-aware llama.cpp templates for local Qwen, Gemma, and Granite
models used by Rustic Studio. The templates preserve each model family's native
prompt protocol while rendering structured `message.name` values for named
`user` and historical `assistant` contributions.

This work is intentionally separate from canonical history and hosted-provider
adaptation. It has different repository ownership, upstream template risk,
model download requirements, hardware constraints, and reviewers. The hosted
identity project can land without waiting for GGUF selection or desktop
benchmarking.

## Dependency On Canonical Identity

The companion identity design provides:

- Truthful canonical roles.
- Structured names derived from Rustic sender identity.
- A `speaker_identity_mode` resolver setting.
- `native` mode, which preserves names through the local OpenAI-compatible
  request.
- `inline` fallback for local servers whose templates are not verified to
  consume names.

A local profile must select `native` only when its active template is covered by
this design and validated against the exact model family. OpenAI-compatible
transport alone is insufficient evidence of speaker awareness.

## Goals

- Render participant names for local multi-model conversations.
- Preserve native reasoning, tools, multimodal inputs, documents, controls, and
  generation prompts.
- Cover the Qwen profiles currently shipped by Studio.
- Add separate Gemma 3 and Gemma 4 coverage.
- Add separate Granite family coverage where upstream formats differ.
- Package templates and migrate existing Studio configuration additively.
- Validate templates statically and with representative real inference.
- Preserve every user-provided template and model setting.

## Non-Goals

- Implementing canonical speaker identity or history reconstruction.
- Implementing Claude, Gemini, or other hosted-provider adaptation.
- Making every OpenAI-compatible local model use `native` mode.
- Using one generic template across incompatible model families.
- Automatically downloading large models during a Studio upgrade.
- Replacing model-native tool or reasoning protocols with a Rustic protocol.

## Template Contract

Local templates consume OpenAI-shaped message dictionaries from llama.cpp. For
named conversational messages they render:

```text
Speaker: "Model 1"
The claim is correct under the stated assumptions.
```

The speaker value uses the same canonical rules as hosted inline adaptation:

- Render a single-line JSON-encoded string.
- Normalize CR/LF and control characters.
- Apply a conservative maximum length.
- Render only named `user` and conversational `assistant` messages.
- Do not render names for `system`, `tool`, or function-result content.
- Do not render a name on a pure assistant tool call.
- Do not render a name on the empty assistant generation prompt.
- Do not modify `reasoning_content`, thinking blocks, tool arguments, images,
  audio, video, documents, or provider control fields.

Unnamed messages must render byte-for-byte identically to the pinned native
template for equivalent llama.cpp inputs.

## Upstream Tracking Policy

Every custom template must:

- Derive from a pinned upstream model repository revision.
- Record the repository, revision, source path, license, and date in a header
  comment.
- Keep the speaker change isolated in one small macro or rendering helper.
- Preserve native validation and exception behavior.
- Include a generated diff fixture against the pinned native template.
- Be re-audited whenever the bundled model or llama.cpp runtime is upgraded.

Do not copy one family's speaker macro into another template without checking
its content representation, turn protocol, tool syntax, and reasoning fields.

## Qwen Coverage

Studio already packages:

- `qwen3-speaker-aware.jinja`
- `qwen3.5-speaker-aware.jinja`
- `qwen3-coder-next-speaker-aware.jinja`

The existing templates should be audited rather than replaced. Validation must
cover:

- Qwen 3 text roles and ChatML turn delimiters.
- Qwen 3 reasoning extraction and `<think>` handling.
- Qwen 3.5 multimodal image and video markers.
- Qwen 3.5 multi-step tool-call and tool-response formatting.
- Qwen3-Coder-Next tool and coding-specific generation behavior.
- Named user and historical assistant messages.
- An unnamed final assistant generation prompt.

The audit must compare each file against the native template embedded in the
GGUF actually used by Studio, not only a similarly named Hugging Face tokenizer.

## Gemma Coverage

### Gemma 3

Add a Gemma 3 speaker-aware template for the currently shipped 4B, 12B, and 27B
instruction profiles. Preserve Gemma 3's role delimiters, image handling, tool
behavior where supported, and generation prompt.

### Gemma 4

Add a separate Gemma 4 template for E2B/E4B and only extend it to other Gemma 4
variants after compatibility is proven. Gemma 4 must not reuse the Gemma 3
template.

The current canonical Gemma 4 template includes family-specific thinking,
tool-call loops, turn closures, input validation, and multimodal handling. Base
the implementation on Google's applicable pinned artifact, such as:
<https://huggingface.co/google/gemma-4-E4B-it/blob/main/chat_template.jinja>.

Validation must include:

- Text-only conversation.
- Image plus text input.
- Audio input for variants that support it.
- Thinking enabled and disabled.
- Historical reasoning content where required by the model.
- Single and repeated tool calls.
- Tool results followed by normal generation.
- Named assistant history without adding identity to the generation prompt.

## Granite Coverage

Add separate templates for Granite generations and modalities where their
upstream formats differ. At minimum evaluate:

- Granite 3 instruction models already practical on supported Studio hardware.
- Granite 4 text models.
- Granite 4 vision models only if Studio exposes a matching profile.

Preserve native available-tools sections, tool-call syntax, documents,
controls, reasoning, citations, system defaults, and multimodal formatting. Use
the exact IBM model artifact as the source, for example:
<https://huggingface.co/ibm-granite/granite-4.0-micro/blob/c4bc6289c6407b9e24ff22332c5fcb55d99e6040/chat_template.jinja>.

Do not claim one Granite template supports every Granite model solely because
llama.cpp labels them with the same general chat format.

## llama.cpp Integration

Package templates under:

```text
electron-resources/llama/templates/
```

Reference the correct file from each verified `models.ini` section using:

```ini
chat-template-file = <family-template>.jinja
```

llama.cpp supports model-specific template files through server model presets:
<https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md>.

The server startup path must resolve template paths relative to the packaged
runtime consistently in development and production builds. Startup should fail
with a clear model/profile error when a configured bundled template is missing,
rather than silently reverting to an embedded template with different speaker
behavior.

## Studio Model Profiles

Update `electron-resources/llama/models.ini` only after the applicable template
passes validation.

Existing Qwen profiles continue using their family-specific files. Add Gemma
and Granite `chat-template-file` values only to verified sections. First-use
download profiles for Gemma 4 and Granite require a separate model-selection
decision based on:

- Upstream licensing and redistribution constraints.
- GGUF source and quantization quality.
- RAM and disk requirements.
- Prompt-processing and generation speed on the supported desktop baseline.
- llama.cpp feature support for the exact template, tools, reasoning, image,
  and audio protocol.
- Stability of the model ID and download artifact.

Large models remain opt-in and must not be downloaded by migration.

## Additive Studio Migration

Extend the existing model-template migration with an explicit mapping from
known model section IDs to template filenames.

The migration must:

- Add `chat-template-file` only when the property is absent.
- Preserve any user-supplied template, including an absolute custom path.
- Preserve model paths, Hugging Face repositories, quantization choices,
  context sizes, reasoning settings, sampling settings, and startup behavior.
- Match known section/model IDs, not broad aliases such as `4b`, `qwen`, or
  `granite`.
- Be idempotent across repeated Studio startups.
- Copy newly packaged template files without deleting custom files.
- Never switch a customized profile from `inline` to `native` automatically.

For bundled dependency profiles, set `speaker_identity_mode: native` only after
the corresponding model migration guarantees a verified template. Existing
custom profiles remain `auto`/`inline` unless the user explicitly opts into
native template behavior.

## Forge Impact

No Forge API, catalog, blueprint, or persistence schema changes are required.
Forge and Studio bundled dependency YAML may mark a verified local profile as
`native` under resolver `conf`. Forge snapshots that runtime setting with the
selected LLM dependency.

If template coverage and dependency-profile updates ship in different release
artifacts, the release process must ensure the Studio template is available
before a bundled profile can select `native`.

## Static Template Tests

For every template:

- Run llama.cpp template analysis.
- Run `/apply-template` against canonical fixture conversations.
- Compare unnamed output with the pinned upstream template.
- Verify exactly one marker for each named conversational contribution.
- Verify no marker for system/tool messages or generation prompts.
- Verify marker-like user content is preserved and not interpreted recursively.
- Verify CR/LF, quotes, control characters, and long participant names.
- Verify string and multimodal content.
- Verify tools, reasoning, and family-specific control tokens.
- Verify prefix preservation when appending a tool result or conversation turn.

Template fixtures should be checked into Rustic UI beside the migration and
packaging tests, with upstream revisions recorded in fixture metadata.

## Studio Tests

- Fresh `models.ini` contains the intended template for each verified profile.
- Migration adds missing template settings to untouched known sections.
- Migration preserves custom templates and all unrelated settings.
- Migration is idempotent.
- Development and packaged paths both resolve every bundled template.
- A missing configured template produces a clear startup/profile error.
- Local dependency profiles select `native` only for verified template-backed
  models.
- Unknown/custom local profiles default to `inline` under `auto` when using a
  custom OpenAI-compatible base URL.

## Real Inference Tests

Run at least one practical model from each supported family:

- Existing Qwen 3.5 0.8B and 2B models.
- A small Gemma 3 model already offered by Studio.
- Gemma 4 E2B or the smallest supported Gemma 4 instruction model.
- The smallest practical supported Granite instruction model.

For each representative model:

1. Start the exact packaged llama.cpp runtime and model preset.
2. Confirm `/props` reports the expected template/model.
3. Use `/apply-template` to inspect the prepared prompt.
4. Send two named assistant contributions with contradictory claims.
5. Ask the model to attribute each claim.
6. Exercise reasoning and a tool call where supported.
7. Exercise image/audio input where the profile claims those capabilities.
8. Confirm no server crash, malformed prompt, leaked generation marker, or
   self-reproducing template syntax.

Behavioral attribution is a smoke test, not the sole contract. Deterministic
template output assertions remain the primary regression protection.

## End-to-End Guild Test

After the hosted identity design and one local family implementation are both
available:

1. Launch a fresh Multi-LLM guild with history enrichment.
2. Assign distinct local models to Model 1 and Model 2.
3. Have Model 1 answer a user question.
4. Tag Model 2 and ask it to evaluate Model 1's answer.
5. Verify Model 2's provider input identifies the human and Model 1.
6. Verify the Studio UI shows canonical messages without template markers in
   stored content.
7. Repeat with one hosted and one local participant.

## Rollout

1. Pin and audit the existing Qwen template sources.
2. Add deterministic Qwen template and migration tests.
3. Select representative Gemma and Granite GGUF artifacts.
4. Author and statically validate one template family at a time.
5. Run packaged real-inference tests on the supported desktop baseline.
6. Add `models.ini` entries and additive migrations only after validation.
7. Set the corresponding bundled dependency profiles to `native`.
8. Run local-only and hosted/local Multi-LLM guild tests.

Each family can land independently. A failed or delayed Gemma/Granite decision
must not block the existing Qwen audit or the hosted identity project.

## Acceptance Criteria

- Every `native` local profile has a packaged, verified speaker-aware template.
- Unknown or unverified local profiles use `inline` rather than silently losing
  names.
- Unnamed template output matches the pinned native template.
- Named user and historical assistant messages have exactly one safe marker.
- Generation prompts, tools, reasoning, and multimodal protocols remain valid.
- Existing custom templates and model settings survive Studio upgrades.
- Template paths work in development and packaged Studio builds.
- Representative Qwen, Gemma, and Granite inference completes successfully on
  supported hardware.
- The Multi-LLM guild attributes prior local-agent responses correctly.

## Open Questions

- Which Gemma 4 GGUF and quantization meet the supported desktop baseline?
- Which Granite model provides the best useful quality/performance tier?
- Should Gemma 4 audio capability be exposed immediately or after separate
  llama.cpp stability testing?
- Should Studio expose template provenance and verification state in model
  diagnostics?
- Should bundled template files be checksummed so accidental edits fail the
  packaging build?
