# Extractor Pack SDK Fixture Workspace

This directory documents the public-safe SDK/ABI fixture contract used by the
SPEC-034 pack authoring smoke path. The reusable implementation lives in
`internal/extractor/packabi`, `internal/extractor/packbuilder`, and
`tools/extractorpack`.

Generated outputs are written to ignored cache paths, not to this directory:

```bash
go run ./tools/extractorpack hostcall-fixture \
  --out-dir build/extractor/cache/pack_sdk \
  --lock-out build/extractor/cache/pack_sdk/hostcall_fixture.lock.json
```

The fixture pack is neutral and deterministic. It uses only `fixture.invalid`
domains, imports `goaria_host.http_fetch`, calls a fake/local broker in tests,
and returns structured direct item references. It is not a production pack and
does not contain real site logic, credentials, private artifacts, or production
signing keys.
