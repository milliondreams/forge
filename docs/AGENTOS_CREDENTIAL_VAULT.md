# AgentOS Credential Vault

Forge uses an internal encrypted credential vault only when strict AgentOS mode
is enabled. Native Forge behavior is unchanged: native deployments continue to
select memory or operating-system keychain stores through the existing
environment variables.

## Startup contract

`agentos-init` creates a profile-owned mode-`0700` directory and passes its
absolute path in `FORGE_AGENTOS_CREDENTIALS_DIR`. Forge validates AgentOS
prerequisites, opens the vault before starting databases or listeners, and fails
closed if the directory or vault is unsafe. The AgentOS status endpoint reports
`credentialBackend: "forge-vault"`.

One vault instance backs:

- organization secrets under `secret:<org>|<name>`;
- OAuth token entries under `oauth:<org>|<provider>`;
- dynamic OAuth client credentials under `oauth-client:<provider>`;
- the secret provider injected into the embedded AgentOS client.

The vault path is removed from Bubblewrap child environments, and AgentOS state
is masked before the selected agent dependency environment is mounted. Agents
receive only values Forge explicitly resolves for their launch.

## Storage format

The directory contains a mode-`0600` `master.key` document and a mode-`0600`
`vault.bin` document. The payload is a single JSON key/value map encrypted with
AES-256-GCM and a fresh random nonce on every write. Format version, algorithm,
and random vault identity are authenticated as additional data.

Writes serialize in process, encrypt a complete next snapshot, write and sync a
new exclusive temporary file, atomically replace `vault.bin`, and sync the
parent directory. The in-memory state changes only after durable replacement.
Startup rejects missing halves, unexpected types or symlinks, unsafe modes,
oversized input, unsupported metadata, mismatched identities, and authentication
failure. An initialization marker permits cleanup only if first initialization
was interrupted before Forge accepted any credential.

## Security boundary

This design prevents accidental plaintext persistence and detects corruption or
tampering. The profile-local key and ciphertext are co-located for unattended
boot, so it does not protect credentials from guest root, a host account that
can read the complete profile directory, or an agent that escapes Bubblewrap.
A future host-unsealed key or encrypted profile disk can strengthen at-rest
protection without changing the Forge management APIs.

There is no GNOME Keyring migration path because AgentOS has not shipped. D-Bus,
Secret Service, GNOME Keyring, libsecret, and their desktop dependency closure
are prohibited from the AgentOS image.
