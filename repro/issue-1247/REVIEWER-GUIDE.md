# Reviewer Guide: Kuadrant/mcp-gateway#1247

No `CODEOWNERS` file in this repo, so reviewer identification is by recent
authorship + involvement in the originating discussion.

## Likely reviewer: `david-martin`

- Recent author of the touched file (`internal/controller/mcpserverregistration_controller.go`,
  confirmed via `git log --format="%an" -- <file>`)
- Directly authored the review comment (PR #1224, `r3519312881`) that stated the
  exact constraint this fix satisfies ("would break legitimate self-signed certs
  that don't set BasicConstraints.IsCA... Older root CAs and dev certs often omit
  it entirely")
- Closed the duplicate issue #1244 and confirmed #1247 was "already assigned" —
  already aware this work was in progress

## Scoped diff for this reviewer

```bash
git diff upstream/main -- internal/controller/mcpserverregistration_controller.go internal/controller/mcpserverregistration_controller_test.go
```

Only these two files change. No CRD, RBAC, config type, or e2e test changes —
this is a pure validation-logic fix inside one existing function.

## What to point them at first

1. The 4-line guard in `validateCACertPEM` (doc comment explains the
   `BasicConstraintsValid`/`IsCA` distinction inline)
2. The 3 new subtests in `TestValidateCACertPEM`, particularly
   `legacy root CA without BasicConstraints must still be accepted`, since that's
   the test that directly encodes their own stated constraint from the PR #1224
   thread
