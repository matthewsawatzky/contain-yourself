# GitHub release process

The repository is arranged so development files are not copied into an
installation. CI tests source changes; tagged releases publish runtime images
and a small deployment bundle.

Normal development does not create a release. Push branches and pull requests
as usual. A semantic version tag is the deliberate promotion step that makes
new prebuilt Compose assets available.

## Initial repository setup

1. Create or select the GitHub repository and push this directory as its root.
2. Keep GitHub Actions enabled with permission to publish packages and
   releases.
3. Ensure the GHCR controller, worker, and WSLAN packages are public for
   anonymous installs.
4. Protect `main` and require the CI workflow if appropriate.

CI runs formatting, race tests, vet, shell validation, Compose validation, and
a release-bundle build. Dependabot tracks Go modules and GitHub Actions.

## Publish

Use the interactive helper:

```bash
./scripts/next-release.sh
```

It reads the latest reachable semantic version tag and asks whether the next
release is major (`x.0.0`), minor (`0.x.0`), or patch (`0.0.x`). Before
creating an annotated tag, it requires a clean `main` whose commit exactly
matches `origin/main`, displays the calculated version, and asks for final
confirmation.

For automation or a preview:

```bash
./scripts/next-release.sh minor --dry-run
./scripts/next-release.sh patch --yes
```

`RELEASE_REMOTE` can select a remote other than `origin`.

The release workflow:

- tests the tagged commit;
- builds Linux AMD64 and ARM64 controller, worker, and WSLAN images;
- publishes the exact version tag under
  `ghcr.io/matthewsawatzky/contain-yourself-{controller,worker,wslan}`;
- creates image provenance attestations;
- packages the production Compose file and default config catalogue;
- generates a repository/version-specific directory setup script;
- publishes a terminal-aware bootstrap for the one-line install path;
- retains the managed installer as a compatibility asset;
- publishes a GitHub release with generated notes.

The setup script and Compose file always select a semantic release tag. The
release policy must never republish an existing container tag; enable GitHub's
immutable-release setting where available. Installations do not deploy from
`main` or use a mutable `latest` container tag.

For a local packaging check:

```bash
./scripts/release.sh v0.1.0 dist
```

GitHub's workflow follows its documented GHCR publishing model and uses the
repository `GITHUB_TOKEN`; no personal registry password is required.
