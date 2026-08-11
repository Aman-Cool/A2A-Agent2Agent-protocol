# Repro: Kuadrant/mcp-gateway#1247 — validateCACertPEM accepts non-CA certificates

## Setup

Branch: `fix/1247-validate-ca-cert-non-ca` off `upstream/main`, in `0-repo/mcp-gateway/`.

Repro test added at `internal/controller/repro_1247_test.go` (temporary, in-package —
`validateCACertPEM` is unexported so the repro has to live inside `package controller`).
This file is scratch and gets removed once the real fix + Phase 6 tests replace it.

## Expected vs actual

- **Expected**: `validateCACertPEM` rejects a certificate that is explicitly not a CA
  (`BasicConstraintsValid: true, IsCA: false` — e.g. a server's own leaf TLS cert
  pasted into `ca.crt` by mistake).
- **Actual**: `validateCACertPEM` only checks that each PEM block parses as a valid
  `x509.Certificate` (`internal/controller/mcpserverregistration_controller.go:766`).
  It never inspects `IsCA` or `BasicConstraintsValid`, so a leaf cert passes
  validation, gets written to the trust bundle, and only fails later at actual TLS
  handshake time with `certificate signed by unknown authority` — no signal at
  admission time.

- **Constraint that must keep working**: a legacy self-signed root CA that never set
  the `BasicConstraints` extension at all (`BasicConstraintsValid: false`) must
  continue to be accepted. Per the maintainer (`david-martin`, PR #1224 review
  thread): "Older root CAs and dev certs often omit it entirely."

## How to run

```bash
cd 0-repo/mcp-gateway
go test ./internal/controller/... -run TestIssue1247_Repro -v
```

## Captured output (bug confirmed present, before any fix)

```
=== RUN   TestIssue1247_Repro
=== RUN   TestIssue1247_Repro/leaf_cert_should_be_rejected_but_currently_is_accepted_(the_bug)
    repro_1247_test.go:69: BUG REPRODUCED: validateCACertPEM accepted a leaf/end-entity certificate (IsCA=false) with no error
=== RUN   TestIssue1247_Repro/legacy_root_cert_without_BasicConstraints_must_keep_being_accepted
    repro_1247_test.go:80: legacy root cert correctly accepted
--- PASS: TestIssue1247_Repro (0.00s)
    --- PASS: TestIssue1247_Repro/leaf_cert_should_be_rejected_but_currently_is_accepted_(the_bug) (0.00s)
    --- PASS: TestIssue1247_Repro/legacy_root_cert_without_BasicConstraints_must_keep_being_accepted (0.00s)
PASS
ok  	github.com/Kuadrant/mcp-gateway/internal/controller	0.359s
```

Both subtests currently "pass" because the repro test only logs and doesn't assert
on the leaf-cert case yet (it's a repro, not the real test contract). The Phase 6
TDD test will invert this: assert the leaf cert IS rejected, which will fail
against today's `validateCACertPEM` and pass once the fix lands.

## Baseline: existing test suite (unaffected, still green)

```bash
go test ./internal/controller/... -run TestValidateCACertPEM -v
```
6/6 existing subtests pass, confirming the repro doesn't disturb current coverage.
