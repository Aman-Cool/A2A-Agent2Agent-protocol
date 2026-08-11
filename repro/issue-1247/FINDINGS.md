# Findings: Kuadrant/mcp-gateway#1247 — validateCACertPEM accepts non-CA certs

## Root cause

`validateCACertPEM` (`internal/controller/mcpserverregistration_controller.go:766`,
pre-fix) only checks that each PEM block parses as a syntactically valid
`x509.Certificate`. It never inspects `IsCA` or `BasicConstraintsValid`. A leaf
cert (e.g. a server's own TLS cert, pasted into `ca.crt` by mistake) therefore
passes validation, gets written to the trust bundle via
`reconcileCACertBundle` (`ca_cert_bundle.go:49`), and is loaded into the broker's
trust pool. It only fails later, at actual TLS handshake time, with the opaque
`certificate signed by unknown authority` — no signal at admission time.

## Options evaluated

| Option | Verdict | Reason |
|---|---|---|
| `cert.BasicConstraintsValid && !cert.IsCA` guard | **Chosen** | Rejects only certs that *explicitly* declare non-CA; legacy certs that omit `BasicConstraints` (`BasicConstraintsValid == false`) pass through untouched. Satisfies both acceptance criteria. |
| Bare `!cert.IsCA` check | Rejected | `IsCA` defaults to `false` when the extension is absent — would reject legitimate legacy root CAs. This is the exact approach `david-martin` already ruled out in the PR #1224 review thread. |
| Strict CA validation + new opt-in CRD field for "trust this legacy root anyway" | Rejected | New API surface (CRD field, docs, webhook) for a `priority/low` issue — disproportionate diff for the problem size. |
| Warn via status condition instead of rejecting | Rejected | Issue #1247's acceptance criteria explicitly requires rejection ("Invalid CA bundles are rejected during validation"), not a warning. |

## Precedent analysis

- **Same-issue precedent**: issue **#1244** was the original filing of this exact
  problem (opened same day as the PR #1224 review comment that raised it).
  Contributor `ANAMASGARD` proposed the identical `BasicConstraintsValid && !IsCA`
  shape in a comment, but the issue was closed as a duplicate of #1247 (already
  assigned) before any PR was opened — so the approach has maintainer-adjacent
  validation but no shipped code to mirror.
- **Pattern precedent**: searched `git log --all -S "BasicConstraintsValid"` and
  `-S "IsCA"` across the whole repo history — no existing helper or call site in
  this codebase already performs this check. The only other `isCA:` hit was an
  incidental YAML example in a cert-manager design doc (PR #1008), not related
  validation logic. This is a fresh but minimal addition, not a duplicate of
  existing logic.
- **Authoritative precedent — Go's own x509 verifier** (`$GOROOT/src/crypto/x509/verify.go:496`):
  `if certType == intermediateCertificate && (!c.BasicConstraintsValid || !c.IsCA)`.
  This strict check is scoped to `intermediateCertificate` only (line 473) — Go's
  own chain builder does **not** require `BasicConstraintsValid`/`IsCA` for a
  `rootCertificate`, since a root is explicitly pre-trusted by whoever configured
  it regardless of what stamp it carries. The CA bundle in this codebase plays
  the *root* role (admin-supplied trust anchors loaded into the pool), not the
  intermediate role, so our guard's leniency toward certs missing
  `BasicConstraints` matches how Go's own TLS stack already treats these same
  certs at actual handshake time — not just a maintainer preference.
- **Real-world precedent — HashiCorp Vault** (`sdk/helper/keysutil/policy.go`,
  `ValidateAndPersistCertificateChain`): uses the identical guard,
  `cert.BasicConstraintsValid && !cert.IsCA`, to reject non-CA certs from
  positions 1+ in a certificate chain while tolerating certs that never declared
  either way. Confirmed via `gh search code`, cross-checked against ~15 other Go
  projects (Kubernetes, Teleport, notation-core-go, etc.) that instead use the
  *stricter* `!BasicConstraintsValid || !IsCA` form — appropriate for their
  use case (validating a fully well-formed explicit sub-CA/intermediate), which
  is a different problem from ours (accepting a leniently-configured root trust
  anchor). Vault's use case matches ours exactly; the stricter idiom elsewhere
  is not a counter-signal, it's solving a different problem.
- **Maintainer constraint** (PR #1224, review thread `r3519179566` → `r3519312881`):
  `david-martin` explicitly rejected a bare `IsCA` check because "older root CAs
  and dev certs often omit it entirely," and asked for a follow-up to "figure out
  the right behaviour there" — which is exactly this issue.

## Repro results

Reproduced via a temporary in-package test (`internal/controller/repro_1247_test.go`,
removed once replaced by the permanent Phase 6 tests). Empirically confirmed the
round-trip behavior of `BasicConstraintsValid`/`IsCA` through
`x509.CreateCertificate` → `x509.ParseCertificate` for three cert shapes:

| Cert shape | `BasicConstraintsValid` | `IsCA` | Guard fires? |
|---|---|---|---|
| Valid CA (existing `testCACertPEM` helper) | `true` | `true` | No |
| Leaf cert (the bug) | `true` | `false` | Yes — rejected |
| Legacy root, no `BasicConstraints` | `false` | `false` | No — accepted |

## Implementation status

Implemented, tested, fully green:

- 9/9 subtests pass in `TestValidateCACertPEM` (6 pre-existing + 3 new), including
  with `-race`
- `go vet`, `golangci-lint` (`0 issues`), `kube-api-linter` (`0 issues`), `cspell`
  all clean
- Full repo test suite (`make test-unit`) passes with no regressions
- Diff is minimal: 11 lines changed in the source file (doc comment + 4-line
  guard), 63 lines of new test coverage

## Git workflow / next steps

- Local commit not yet made (source + tests on branch
  `fix/1247-validate-ca-cert-non-ca`, uncommitted at time of writing)
- Not pushed to fork; no PR opened — both require explicit user authorization
  per workspace convention
