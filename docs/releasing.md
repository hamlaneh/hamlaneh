# Releasing Hamlaneh

Unpatched self-hosted instances are this category's biggest failure mode (PLAN.md §6.6), and
open source cuts both ways on patching: attackers diff public commits and weaponize fixes within
days. Everything in this file follows from those two sentences. A release has to be something an
operator can *prove* came from us, and a security fix has to be able to reach every instance in
the same hour the advisory does.

## What a release is

A tag matching `v*` runs `.github/workflows/release.yml`, which produces:

| Asset | What it is |
|---|---|
| `hamlaneh-<version>-<os>-<arch>.tar.gz` / `.zip` | the server binary, cross-compiled for linux, darwin and windows |
| `hamlaneh-<version>.spdx.json` | the SBOM |
| `SHA256SUMS` | one line per file above, including the SBOM |
| `SHA256SUMS.sigstore.json` | the cosign bundle over `SHA256SUMS` |
| `ghcr.io/hamlaneh/hamlaneh/server:<version>` | the server image, signed by digest, SBOM attached |

**One signature, not one per file.** `SHA256SUMS` names every artifact, so tampering with any
artifact breaks its checksum and tampering with the checksums breaks the signature. That is the
whole mechanism, and `deploy/verify-release.sh` is the whole check.

**Signing is keyless.** cosign exchanges the workflow's GitHub OIDC token for a short-lived
Fulcio certificate whose identity is *this workflow file at this tag*, and logs it in Rekor. No
signing key exists, so there is no signing key to steal, rotate, or lose. What replaces "trust
this key" is "trust that this repository's release workflow ran at this tag" — which is what
`--certificate-identity-regexp` pins.

**SBOM format is SPDX** (`spdx-json`). CycloneDX is the richer format for vulnerability tooling
and would have been defensible; SPDX wins because the SBOM exists for the reason §6.6 gives —
enterprises ask — and that ask arrives through procurement, where SPDX is the ISO/IEC 5962:2021
standard the NTIA minimum-elements guidance names. It is generated from the source tree rather
than the final image on purpose: the shipped server embeds the web bundle and the MLS wasm, so
`go.mod`, `package-lock.json` and `Cargo.lock` together are the honest dependency picture.
Scanning the distroless image would report the Go modules and silently drop the other two.

## Cutting a release

1. `main` is green. Not "green except for". A release from a red `main` is a release nobody
   verified.
2. Update `SECURITY.md`'s **Supported versions** section if the set of patched versions changed.
3. Tag and push:

   ```sh
   git tag -s v1.4.0 -m "v1.4.0"
   git push origin v1.4.0
   ```

4. The workflow builds, generates the SBOM, signs, **runs `deploy/verify-release.sh` against its
   own output**, and creates the GitHub release **as a draft**. If the pipeline and the
   operator's verifier ever disagree about layout, naming, or signing identity, the release
   fails here rather than in somebody's terminal.
5. Verify the draft yourself (next section).
6. Publish the draft. For a security release, that click is the same act as publishing the
   advisory — see below.

The draft step is deliberate. It is the only point in the process where a human can look at what
is about to be handed to every instance, and for a coordinated-disclosure release it is what
lets the fix and the advisory land together instead of the fix landing first.

## What the maintainer verifies before publishing

Download the draft's assets into an empty directory, then:

```sh
gh release download v1.4.0 --dir ./v1.4.0
deploy/verify-release.sh --version v1.4.0 --dir ./v1.4.0
```

That checks, in order:

- the cosign signature over `SHA256SUMS`, against **this repository's release workflow at this
  tag** — not merely "some valid Sigstore signature";
- that every file present is named by the signed `SHA256SUMS` (an artifact smuggled in alongside
  the release, with its checksum line deleted, is otherwise invisible to `sha256sum -c`);
- that every downloaded artifact matches its signed checksum;
- that the SBOM shipped and is covered by the signature.

Then check by hand what a script cannot judge: that the release notes name the right changes, and
that the binaries are the platforms you meant to ship.

The image is verified separately, against the same identity:

```sh
cosign verify \
  --certificate-identity-regexp '^https://github\.com/hamlaneh/hamlaneh/\.github/workflows/release\.yml@refs/tags/v1\.4\.0$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/hamlaneh/hamlaneh/server:v1.4.0

cosign verify-attestation --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/hamlaneh/hamlaneh/\.github/workflows/release\.yml@refs/tags/v1\.4\.0$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/hamlaneh/hamlaneh/server:v1.4.0
```

## What an operator (or the updater) runs

The same script, plus the version already installed:

```sh
deploy/verify-release.sh --version v1.4.0 --dir ./download --installed v1.3.2
```

Exit codes are distinct so an updater can tell the two failures apart:

| Code | Meaning |
|---|---|
| 0 | verified |
| 1 | verification failed — signature, checksum, coverage or SBOM |
| 2 | usage error |
| 3 | refused: older than the installed version, and `--force` was not given |

### Anti-rollback, and why it is not paperwork

An attacker positioned to feed the updater a release **does not need to forge a signature**. An
old release of ours is already validly signed by us, and serving one is how a patched instance
gets walked back onto a vulnerability we already fixed — a downgrade attack, and the reason
ROADMAP.md's Phase 4 gate names it as its own negative test.

So `verify-release.sh` refuses a version older than the installed one. Nothing about the
signature is wrong in that case; the refusal is a policy the signature cannot express.
`--force` is the only way past, and it exists because a maintainer occasionally has to roll an
instance back on purpose.

Version ordering is implemented in the script rather than shelled out to `sort -V`, which is not
semver: `sort -V` places `1.2.3-rc.1` *above* `1.2.3`, so rolling a release back to its own
release candidate would read as an upgrade.

The installed version comes from `--installed` or `$HAMLANEH_INSTALLED_VERSION`. If neither is
set the script says so and skips the check — it does not guess.

## Coordinated disclosure (PLAN.md §6.6, SECURITY.md)

The sequence, in order. The point of the order is that nothing public appears until everything is
ready, because a public commit is a working exploit for anyone reading the diff.

1. **Report arrives** privately, via GitHub private vulnerability reporting. Acknowledge within
   3 business days (SECURITY.md's stated commitment).
2. **Assess and plan** within 14 days. Decide severity, affected versions, and whether the fix
   needs an embargo at all — most bugs do not.
3. **Fix privately.** Use a GitHub Security Advisory's private fork. Not a branch on the public
   repository, not a public PR, not a public issue: the commit is the exploit. The fix carries a
   regression test like any other, and the test is as private as the fix until release day.
4. **Prepare the advisory** in draft alongside the fix: affected versions, severity, workaround
   if one exists, credit for the reporter unless they decline. Request the CVE through GitHub at
   this point, not after.
5. **Cut the release** as above. The draft release now sits alongside the draft advisory, both
   verified, neither public.
6. **Release day — publish both, together.** Publish the GitHub release and the advisory. The
   patch commit becomes public at the same moment its advisory does, which is the narrowest
   window this process can produce.
7. **Push the update.** Security patches go out on the auto-update channel, to everyone,
   simultaneously, free — never paywalled, never delayed for non-payers (PLAN.md §6.9). This is
   not negotiable and does not depend on tier.
8. **Notify.** Anyone who asked to be told, plus the release notes, plus SECURITY.md's supported
   versions table.

A fix that is already public — because it was reported publicly, or is already being exploited —
skips the embargo. Speed beats sequence at that point: ship, then write the advisory.

## What is not yet true

Stated here rather than left to be discovered, because a release process that quietly does less
than it claims is worse than one that claims less.

- **No tag has ever been pushed.** The keyless half of this pipeline — Fulcio issuing a
  certificate, Rekor logging it, and `--certificate-identity-regexp` matching the resulting
  identity — has never run. `deploy/verify-release.test.sh` exercises real cosign against a
  locally generated key pair, which proves the script's logic and cosign's signature checking,
  and does not prove the identity flags are right. The first real tag proves that, and the
  workflow's self-verification step is where it will fail if they are wrong.
- **There is no auto-updater yet.** `verify-release.sh` is the gate an updater will call; the
  updater itself is unbuilt. The ROADMAP gate's "auto-update applies a signed release" half is
  not met by anything in this repository.
- **The server does not report its own version.** `hamlaneh-server` has no `--version` flag, so
  the installed version has to be supplied to the script by its caller. Wiring that is a change
  in `server/`.
- **The image is `linux/amd64` only.** Go cross-compiles the binaries for free; the image build
  runs Rust, Node and Go, and building it for arm64 under emulation is expensive enough to want
  a real decision rather than a default.
- **No `latest` tag is published**, deliberately. A moving tag is how instances silently change
  version, which is the thing anti-rollback exists to prevent. `deploy/docker-compose.yml` still
  builds the image locally rather than pulling a published one; switching it is an install-flow
  change and belongs with the updater work.
