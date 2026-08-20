# Extractor Pack Supply-Chain Fixture

This directory contains a deterministic public fixture pack used only by supply-chain verification tests. It contains no proprietary site logic, private source, credentials, release assets, or production signing material.

## Fixture ZIP contract

Extractor pack assets are ZIP files with exactly these entries at the archive root:

- `manifest.json`
- `payload.wasm`
- `manifest.sig`

The verifier rejects directories, absolute paths, `..` traversal, duplicate entries, symlinks, missing entries, and extra files. `manifest.sig` is an Ed25519 signature over the raw `manifest.json` bytes. The signed manifest's `payload_sha256` field covers the raw `payload.wasm` bytes.

## Lock fields

`fixture.lock.json` uses schema version `1` and stores lowercase hex Ed25519 public keys in `public_keys`. The fixture uses a local `asset_path` and must be verified only with the explicit local-file/fixture mode (`--allow-file`). Production lock entries use public HTTPS `asset_url` values instead.

The fixture private key is deterministically derived for tests and must never be treated as production key material.
