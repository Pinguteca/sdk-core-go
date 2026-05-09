# sdk-core-go design docs

Documentation about the **library itself**: rationale behind defaults, rejected alternatives, and the conditions under which a decision should be revisited.

End-user docs (how to wire the interceptors into a Connect client, configuration recipes) live in the top-level `README.md`. Anything in this directory is for contributors and auditors.

## Contents

- `adr/` — Architecture Decision Records. Each ADR captures one cross-cutting decision in MADR-lite form: Context / Decision / Consequences / Revisit-when.

## When to add an ADR

Add one when:

- A default would be surprising to a new contributor (e.g. why we picked this RNG, this jitter strategy, this auth flow).
- A tool or library was rejected and someone might propose it again later.
- A compliance posture (FIPS, SLSA, etc.) shapes the implementation.

Do not ADR routine bumps, refactors, or implementation details that are obvious from the code.
