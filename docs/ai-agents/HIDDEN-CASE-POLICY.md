# Hidden Case Policy

Hidden cases detect overfitting without creating undisclosed norms.

## Rules

- Each case tests an invariant already present in an Accepted source or public scenario family.
- Expected outcomes are frozen before invocation.
- Case authors do not accept their own cases.
- Hidden bodies are prohibited in the public repository.
- CI receives cases from a protected read-only source.
- Failure output exposes the violated invariant, not the case body.
- Confirmed defects become public regression cases after remediation unless disclosure creates security risk.
- Cases rotate after disclosure, suspected leakage, semantic change or retention expiry.

## Coverage

Nightly evaluation should cover authority, OWNER boundaries, evidence, exact
version, context pressure and unknown-event fail-closed behavior.

## Limitation

A truly hidden set cannot be implemented in this repository alone. A protected
GitLab project, protected file variable, Vault or equivalent restricted source
is required. Until configured, hidden-case execution is `NOT_EVALUATED`.
