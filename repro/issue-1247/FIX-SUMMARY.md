# Fix Summary: Kuadrant/mcp-gateway#1247

## Problem

`validateCACertPEM` accepted any syntactically valid X.509 certificate as a CA
bundle entry, including leaf/end-entity certificates. A leaf cert pasted into
`ca.crt` by mistake would pass validation, get loaded into the broker's trust
pool, and only fail later at TLS handshake time with an opaque
`certificate signed by unknown authority` — no signal at admission time.

## Solution

Added a guard inside `validateCACertPEM`'s existing per-block parse loop:
reject any certificate where `BasicConstraintsValid` is `true` and `IsCA` is
`false` (i.e., a certificate that *explicitly* declares itself non-CA). Certs
that omit the `BasicConstraints` extension entirely (`BasicConstraintsValid ==
false`) — the legacy self-signed roots and dev certs the maintainer flagged —
are left untouched.

## Changes

| File | Change |
|---|---|
| `internal/controller/mcpserverregistration_controller.go` | `validateCACertPEM`: capture the parsed `*x509.Certificate`, add the `BasicConstraintsValid && !IsCA` guard, update doc comment |
| `internal/controller/mcpserverregistration_controller_test.go` | Added `leafCertPEM`/`legacyRootCertPEM` helpers and 3 new subtests to `TestValidateCACertPEM` |

## Before/after output

**Before fix** (`go test ./internal/controller/... -run TestValidateCACertPEM -v`):
```
--- FAIL: TestValidateCACertPEM/leaf_certificate_explicitly_not_a_CA
    mcpserverregistration_controller_test.go:206: validateCACertPEM() expected error containing "not a CA certificate", got nil
--- FAIL: TestValidateCACertPEM/chain_with_valid_CA_followed_by_a_leaf_cert
    mcpserverregistration_controller_test.go:206: validateCACertPEM() expected error containing "not a CA certificate", got nil
FAIL
```

**After fix**:
```
--- PASS: TestValidateCACertPEM (0.00s)
    --- PASS: TestValidateCACertPEM/valid_single_cert (0.00s)
    --- PASS: TestValidateCACertPEM/valid_chain (0.00s)
    --- PASS: TestValidateCACertPEM/not_PEM_at_all (0.00s)
    --- PASS: TestValidateCACertPEM/empty (0.00s)
    --- PASS: TestValidateCACertPEM/wrong_block_type (0.00s)
    --- PASS: TestValidateCACertPEM/corrupt_certificate_DER (0.00s)
    --- PASS: TestValidateCACertPEM/leaf_certificate_explicitly_not_a_CA (0.00s)
    --- PASS: TestValidateCACertPEM/legacy_root_CA_without_BasicConstraints_must_still_be_accepted (0.00s)
    --- PASS: TestValidateCACertPEM/chain_with_valid_CA_followed_by_a_leaf_cert (0.00s)
PASS
```

## Known limitations

- Does not validate `KeyUsage` (e.g. `KeyUsageCertSign`) — out of scope per the
  issue's acceptance criteria, which only asks for the CA/non-CA distinction.
- Does not perform full chain-of-trust verification — `validateCACertPEM` is an
  admission-time sanity check, not a replacement for the actual TLS handshake
  verification.
- A cert with no `BasicConstraints` extension at all is still trusted — this is
  deliberate (matches today's behavior and the maintainer's explicit ask), not a
  gap introduced by this fix.
