# PR Readiness: Kuadrant/mcp-gateway#1247

## Draft PR description (follows `.github/PULL_REQUEST_TEMPLATE.md`, no-ai-slop checked)

> ## What does this PR do?
>
> `validateCACertPEM` now rejects certificates that explicitly declare `BasicConstraints CA:FALSE` (e.g. a server's own leaf TLS cert pasted into `ca.crt` by mistake), while still accepting certificates that omit `BasicConstraints` entirely, since older root CAs and dev certs often don't set it.
>
> Fixes #1247
>
> ## Why
>
> Previously a leaf cert would pass validation and get loaded into the trust pool. It only failed later, at actual TLS handshake time, with `certificate signed by unknown authority`. No signal at admission time. Raised in the #1224 review thread (comment r3519179566); this issue tracks the follow-up discussed there.
>
> ## Testing
>
> Added 3 new subtests to `TestValidateCACertPEM`: leaf cert rejected, legacy root without `BasicConstraints` still accepted, and a chain with a valid CA followed by a leaf cert still rejected. All 9 subtests pass, including with `-race`. Full `make test-unit` suite green, no regressions.

**What changed from the first draft**: removed one em dash in the "Why" section
(split into two shorter sentences); everything else was already terse and
specific with no filler, so left untouched.

## Precedent evidence table

| Reference | What it shows |
|---|---|
| PR #1224, review comment `r3519179566` | Original bug report from `Patryk-Stefanski`, exact `file:line` |
| PR #1224, review comment `r3519312881` | `david-martin`'s explicit constraint: bare `!IsCA` breaks legacy roots |
| Issue #1244 (closed duplicate) | `ANAMASGARD`'s proposed `BasicConstraintsValid && !IsCA` shape — same approach, never shipped |
| `go doc crypto/x509.Certificate` | Confirms `BasicConstraintsValid` semantics ("indicates whether IsCA... is valid") |

## Anticipated reviewer Q&A

**Q: Why not just check `cert.IsCA`?**
A: Already discussed and ruled out in the PR #1224 thread — `IsCA` defaults to
`false` when `BasicConstraints` is absent, so a bare check would reject
legitimate legacy root CAs. The `BasicConstraintsValid` guard only fires when
the cert *explicitly* says non-CA.

**Q: Does this affect the per-server `caCertSecretRef` path too?**
A: Yes, intentionally. `validateCACertPEM` is shared between the gateway-level
`caCertBundleRef` and per-server `caCertSecretRef` validation, so the fix covers
both call sites with one change, no duplication needed.

**Q: What about certs with `BasicConstraints` present but no `KeyUsageCertSign`?**
A: Out of scope for this issue. The acceptance criteria asks specifically for
the CA/non-CA distinction; `KeyUsage` validation would be a separate,
follow-up concern if it comes up.

**Q: Test coverage — is a chain case covered?**
A: Yes — `chain with valid CA followed by a leaf cert` confirms a bundle
containing one valid CA plus one leaf is still rejected as a whole, matching
the function's existing fail-fast-per-block behavior.

## Pre-PR checklist (from `.github/PULL_REQUEST_TEMPLATE.md` + `CONTRIBUTING-CHECK.md`)

- [x] `make check-gofmt` exits 0
- [x] `make check-goimports` exits 0
- [x] `make check-newlines` exits 0
- [x] `make vet` exits 0
- [x] `make test-unit` exits 0 (full suite, not just touched package)
- [x] `npx --yes cspell` clean on changed files
- [x] `golangci-lint` clean (0 issues) — CONTRIBUTING-CHECK.md notes this was
      "known broken" upstream as of 2026-04-29 due to a deleted dependency; ran
      clean this time, so either fixed upstream or CI will confirm
- [x] `kube-api-linter` clean (0 issues)
- [x] `go test -race` clean on the changed tests
- [x] Comments minimal, terse, lowercase first word, only where the *why* isn't
      obvious (matches root `CLAUDE.md` style)
- [x] No unrelated changes — single function touched, doc comment updated,
      test file only
- [ ] CodeRabbit walkthrough review — only possible once the PR is actually open
- [ ] `agent-skills:review` skill run — same, PR must be open first
