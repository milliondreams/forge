# Keychain-first development reset

Forge now stores managed secrets, OAuth tokens, and OAuth dynamic-client
credentials only in the operating-system keychain. Secret names are tracked in
the Forge database so catalog and existence checks do not read keychain values.

There is intentionally no migration for credentials created by pre-release
builds. Before testing this build:

1. Stop Forge and Rustic Studio.
2. Remove the local Forge data directory (or select a new `--forge-home`) so the
   secret metadata index starts empty.
3. In the OS credential manager, remove development entries under the service
   used by the application: `rustic-studio` for Studio or `rustic-ai-forge` for
   standalone Forge unless `FORGE_KEYCHAIN_SERVICE` was customized.
4. Start Studio or Forge and configure secrets again through the Forge API.

On macOS, use Keychain Access. On Windows, use Credential Manager. On Linux,
use the desktop Secret Service/keyring application for the active session.
Avoid bulk command-line deletion because those services may also contain
unrelated user credentials.

The secure launch defaults are:

```text
forge server --secret-providers keychain
forge client --secret-providers keychain --dependency-config <path>
```

Development-only read fallbacks must be explicitly requested, for example
`--secret-providers keychain,env`. Forge emits a prominent warning whenever
`env`, `dotenv`, or `file` is present. Managed writes still go only to the
keychain.
