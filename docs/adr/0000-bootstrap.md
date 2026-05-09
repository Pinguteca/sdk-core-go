# ADR 0000: Bootstrap conventions inherited from sdk-scaffold

- **Status:** Accepted
- **Date:** 2026-05-09
- **Deciders:** Pinguteca SDK team
- **Affects:** every cross-cutting tool inherited from the upstream template

## Context

This repository was bootstrapped from `Pinguteca/sdk-scaffold` (Copier).
The cross-cutting layer (mise, Cocogitto, prek, Renovate, GitHub Actions,
Octo STS, ADR scaffolding) is upstream-managed and updated via
`copier update`. The decisions below are *inherited* — they were made in
the scaffold and are recorded here so this repo's contributors can find them
without having to dig through the template repo.

## Decision

1. **Tool versions live in `mise.toml` / `mise.ci.toml`.** No tool is
   installed via curl-bash, system package manager, or per-developer
   instructions. Pin once, install everywhere.
2. **Conventional Commits enforced via Cocogitto** (`cog.toml`). Commit
   messages drive the changelog, the next version, and the release
   trigger. `mise run bump` cuts a tag when there are unreleased commits.
3. **Pre-commit hooks via prek** (`prek.toml`): secret scan on every
   commit, large-file guard, line-ending fixer, GHA pinning on push.
4. **Renovate** consumes the org-wide preset at
   `github>Pinguteca/renovate-config`. Per-repo Renovate
   tweaks should go to that preset, not here, unless they are project-specific.
5. **Per-workflow OIDC via Octo STS.** `.github/chainguard/*.sts.yaml`
   defines one identity per workflow that needs write or special-permission
   access. `GITHUB_TOKEN` is never used for releases or auto-commits.
6. **All RNG routes through cryptographic sources** (e.g. `crypto/rand`
   in Go) for FIPS 140-3 compatibility. See language-specific ADRs for detail.
7. **SLSA Level 3 provenance** on every release. Source archive
 are signed with cosign
   keyless and accompanied by a CycloneDX SBOM and a provenance file.

## Consequences

### Positive

- New contributors run `mise install && prek install` and have a working
  environment in under a minute.
- Releases are reproducible: same commits, same versions, same tag.
- Supply chain posture is uniform across every Pinguteca SDK repo.

### Negative

- Bumping a shared default requires a `copier update` round trip — small
  drift between scaffold and repo accumulates if updates are skipped for long.
- The mise lockfile must be committed; without it the "pin once" promise
  breaks for collaborators.

### Neutral

- Project-specific ADRs are added alongside this one (numbered 0002+),
  using the same template structure.

## Revisit when

- A new cross-cutting tool replaces one of the above (e.g. uv replacing
  pipx, or a different secret scanner outperforming Kingfisher). Open a
  PR against `Pinguteca/sdk-scaffold`.
- This repo grows constraints the scaffold cannot accommodate (e.g. a
  proprietary build system that fights mise). At that point, fork the
  template or override locally with a documented deviation.

## References

- [sdk-scaffold](https://github.com/Pinguteca/sdk-scaffold)
- [Cocogitto](https://docs.cocogitto.io/)
- [Octo STS](https://github.com/octo-sts)
- [Sigstore gitsign](https://github.com/sigstore/gitsign)
