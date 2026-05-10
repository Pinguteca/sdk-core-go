# ADR 0012: Multi-module release tooling

- **Status:** Accepted
- **Date:** 2026-05-10
- **Deciders:** SDK team
- **Affects:** `cog.toml`, `.github/workflows/release-go.yml`,
  `.github/workflows/scheduled-release.yml`, `mise.toml` SBOM task,
  every sub-module's published tag.
- **Implements:** RFC 0002 multi-module layout for the release path.
  Companion naming follows RFC 0004.

## Context

ADR 0010 split the repo into a root module plus seven companion
sub-modules. The Go ecosystem versions sub-modules independently via
tags of the form `<dir>/vX.Y.Z` (e.g. `hedge/v1.0.0`,
`transport/mtls/pkcs12/v0.3.2`). The current release pipeline assumes
single-module tags:

- `cog.toml` only emits a global tag (`vX.Y.Z`) even though
  `generate_mono_repository_package_tags = true`. The
  `[packages]` table is empty, so cocogitto has nothing to enumerate.
- `release-go.yml` triggers on `tags: ["v*"]`, which never matches
  sub-module tags because those start with the directory name, not
  `v`.
- The artifact name `sdk-core-go-<tag>.tar.gz` collides on `/`
  characters (the `/` in `hedge/v1.0.0` would produce a path-shaped
  filename).
- The SBOM task runs `syft scan dir:.` once per release, which
  describes the whole repo regardless of which module is being
  tagged.

Releasing a companion today would either silently no-op (the
workflow does not fire) or pollute the global release with
inaccurate metadata.

## Decision

1. **Enumerate every sub-module in `cog.toml` `[packages]`.** Each
   companion declares its directory and the bump rule
   `cocogitto` needs to detect a release. Conventional commits
   scoped to a package's name (`feat(hedge): ...`,
   `fix(otel): ...`) drive that package's bump. A commit with no
   matching scope bumps the root module.

2. **Workflow tag patterns expand.** `release-go.yml` triggers on
   both root tags and sub-module tags:

   ```yaml
   on:
     push:
       tags:
         - "v*"
         - "*/v*"
         - "*/*/v*"
   ```

   The `*/v*` and `*/*/v*` globs cover one- and two-level companion
   directories (e.g. `hedge/v1.0.0` and
   `transport/mtls/pkcs12/v0.3.2`). Adding more depth requires
   another glob entry; today no companion sits deeper than two
   levels.

3. **Workflow derives the package scope from the tag.** A first
   step parses `${{ github.ref_name }}` into:

   - `PACKAGE_DIR` - directory of the package being released
     (e.g. `hedge`, `transport/mtls/pkcs12`, or `.` for root).
   - `PACKAGE_SLUG` - filesystem-safe form of the tag with `/`
     replaced by `-` (e.g. `hedge-v1.0.0`,
     `transport-mtls-pkcs12-v0.3.2`, or `v1.0.0` for root).
   - `VERSION` - the trailing `vX.Y.Z` portion.

   These env vars feed every later step.

4. **Source archive uses `PACKAGE_SLUG` in its filename.** The
   archive itself is whole-repo (single tarball per release); the
   slug just disambiguates filenames so multiple companion
   releases on the same day do not collide. Format:
   `sdk-core-go-<slug>.tar.gz`.

5. **SBOM is per-package.** A new `release:sbom` task takes
   `PACKAGE_DIR` as input and runs syft against that directory's
   own `go.mod` graph. Output:
   `out/sbom/sdk-core-go-<slug>.cdx.json`. Root releases scan the
   root module; companion releases scan the companion. Cross-
   module deps land in the companion's SBOM correctly because
   syft follows the manifest the directory points to.

6. **Cosign signing stays per-tag.** Same flow as today (sign
   `checksums.txt`), the only change is the input filename set.

7. **SLSA provenance stays per-tag.** Same generator workflow,
   subjects derive from the per-tag checksums.

8. **GitHub Release name and notes match the tag.** No custom
   formatting; `softprops/action-gh-release` with
   `generate_release_notes: true` already names the release after
   the tag, which means a companion release shows up as
   `hedge/v1.0.0` in the releases UI. That is the desired
   behaviour because the Go module path is the canonical reference
   anyway.

9. **Scheduled-release workflow stays as `cog bump --auto`.** With
   `[packages]` populated, `cog bump --auto` walks each package,
   detects the right bump per package, and pushes one tag per
   package that has new commits. The push triggers `release-go.yml`
   per tag. No change to `scheduled-release.yml` itself.

10. **Per-package changelogs deferred.** Today's `CHANGELOG.md` at
    the repo root remains the single changelog. cocogitto's
    per-package changelog mode is opt-in and adds noise (one
    `CHANGELOG.md` per directory). Revisit when consumers ask.

## Consequences

### Positive

- Companion tags actually trigger a release. The pipeline becomes
  the contract that ADR 0010 declared.
- Per-package SBOMs accurately describe the dep graph each
  consumer sees, instead of a kitchen-sink scan of the whole repo.
- Filename slug avoids `/`-in-filename problems on every step
  that touches `out/`.
- `cog bump --auto` becomes the single release entry point for
  any module in the repo.

### Negative

- The `[packages]` section is hand-maintained. Adding a new
  companion requires editing `cog.toml` plus the new sub-module's
  `go.mod` plus `go.work`. Three places to keep in sync; missing
  the cog entry means the new companion never gets a bump.
- Tag-pattern globs in the workflow are heuristic. A fourth-level
  directory (none today) would need another glob entry.
- The slug substitution is asymmetric: the tag stays
  `hedge/v1.0.0` but artifacts read `hedge-v1.0.0`. Documented in
  the workflow comments; release notes still show the tag form.

### Neutral

- Per-package signing of separate checksum files is possible but
  not adopted. One signed checksums file per release tag covers
  everything in `out/`. Consumers who want a per-artifact
  signature can derive it from the cosigned bundle.

## Alternatives considered

- **Single global release per scheduled run.** Bump root only,
  ignore companion versions. Rejected: defeats the multi-module
  split.
- **A separate workflow per companion.** Cleaner separation but
  multiplies CI files by the companion count. Rejected; one
  workflow with parsing logic is cheaper to maintain than seven
  copies.
- **Custom release tooling instead of `cog bump --auto`.**
  Considered. Cocogitto's monorepo support already covers the
  conventional-commit-scoped bump model that the team's commit
  messages already follow. Rejected for the moment.

## Revisit when

- A fourth directory level is introduced. Add another glob to the
  workflow and update this ADR.
- Consumers ask for per-package changelogs. Switch to cocogitto's
  per-package changelog generator.
- A second SDK (e.g. `sdk-core-dotnet`) ships with its own
  release tooling. The shared shape may be worth promoting to an
  RFC for cross-language consistency.
- The `[packages]` table grows to a size where hand-editing is
  error-prone. Consider generating it from the `go.work` file's
  `use` directives.

## References

- ADR 0010 (multi-module repository split).
- RFC 0002 (layered SDK architecture).
- RFC 0004 (companion naming convention).
- Cocogitto monorepo guide:
  https://docs.cocogitto.io/guide/mono_repo/
- AWS SDK Go v2 multi-module release pattern:
  https://github.com/aws/aws-sdk-go-v2
- SLSA generator action:
  https://github.com/slsa-framework/slsa-github-generator
