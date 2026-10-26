# Security policy

gate4ai is a guard between AI agents and your computer, so its own security matters more than its features. This page says how to report a vulnerability and how releases are made verifiable.

## Reporting a vulnerability

Report privately through [GitHub Security Advisories](https://github.com/gate4ai/gate4ai/security/advisories/new) (preferred) or by email to security@gate4.ai. Please do not open a public issue for a vulnerability.

What helps: the version (`gate4ai --version`), the OS, the steps to reproduce, and what an attacker gains (another OS user reads data, an agent bypasses a policy or approval, an agent reads a secret, a remote party reaches the Web UI, …). We answer within 7 days, keep you informed, and credit you in the release notes unless you prefer not to be named.

Supported versions: the latest release (`https://gate4.ai/latest`). Older releases get fixes only by updating; `curl -fsSL https://gate4.ai/install.sh | sh` updates in place and keeps your data.

## What counts

- An agent (local CLI/MCP or a cloud agent through the relay) performing an action the policy denies or that needs approval, without the owner's approval; approving its own request; reading a secret from the vault or a password typed into a site form.
- Another OS user or a remote party reading gate4ai data (policies, journal, keys), connecting to the owner socket, to Xvfb or to Chrome's CDP.
- Reaching the Web UI without the password; bypassing the Telegram confirmation; the relay reading traffic (it is TLS passthrough and must stay so).
- A connector from the store (WASM) reaching the network or files outside its manifest.
- A tampered release or installer passing the checks below.

Out of scope: a user running gate4ai with `allow` for everything and an agent doing what was allowed; denial of service against your own daemon; findings that need root on the same computer.

## Verifying a release

Every release is built by `.github/workflows/release.yml` on a `v*` tag, nothing is built by hand.

- **Checksums and signature.** `checksums.txt` is signed with cosign (keyless, Sigstore). The installer verifies the signature when `cosign` is installed; by hand:

  ```sh
  cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
    --certificate-identity-regexp '^https://github.com/gate4ai/gate4ai/\.github/workflows/release\.yml@refs/tags/v' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
  sha256sum -c checksums.txt --ignore-missing
  ```

- **Build provenance (SLSA).** Each archive has a GitHub artifact attestation:

  ```sh
  gh attestation verify gate4ai_<version>_linux_amd64.tar.gz -R gate4ai/gate4ai
  ```

- **Container image.** `ghcr.io/gate4ai/gate4ai:<version>` is signed with cosign: `cosign verify ghcr.io/gate4ai/gate4ai:<version> --certificate-identity-regexp '^https://github.com/gate4ai/gate4ai/' --certificate-oidc-issuer https://token.actions.githubusercontent.com`.

- **Reproducible builds.** Binaries are built with `-trimpath`, `CGO_ENABLED=0` and `mod_timestamp` from the commit; `go build` from the tag gives the same binary.

Third-party GitHub Actions are pinned by full commit SHA, dependencies are watched by Dependabot, and the repository publishes an [OpenSSF Scorecard](https://securityscorecards.dev/viewer/?uri=github.com/gate4ai/gate4ai).

## Design notes for reviewers

The threat model, the trust boundaries (owner socket vs agent socket, agent keys, approvals, the relay as TLS passthrough, WASM connectors without network) and the known limits are in [docs/specification.md](docs/specification.md) («Безопасность самого шлюза») and [docs/headless-server.md](docs/headless-server.md) («Изоляция»).
