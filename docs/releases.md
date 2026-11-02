# CI and releases

## Workflow layout

- `.github/workflows/ci.yml`: read-only checks on pull requests, `main`, manual
  dispatch, and a weekly schedule. It is also reusable by the release workflow.
- `.github/workflows/release.yml`: validates a pushed version tag, runs the full
  CI workflow against that exact tagged commit, then publishes using GoReleaser.
- `.goreleaser.yaml`: builds and archives Linux, macOS, and Windows binaries for
  amd64 and arm64, with CGO disabled, SHA-256 checksums, and commit-based notes.
- `.github/dependabot.yml`: weekly Actions and Go dependency update PRs.

Tests use fixtures and mocked transports. No flight-provider secrets are needed.
Only the publishing job has `contents: write`; it uses the built-in
`GITHUB_TOKEN`, so no personal access token is required. Checkout credentials are
not persisted. Actions are pinned to commit SHAs.

The Go compiler for release binaries is the latest stable version. The minimum
supported version is Go 1.24.0, declared in `go.mod`. The Linux minimum-version
job reads that file and tests source compatibility with automatic toolchain
upgrades disabled.

## Create a release

Merge the release changes into `main` and wait for **CI passed**. From a clean
working tree, select the desired version and push its annotated tag:

```sh
git switch main
git pull --ff-only origin main
git tag -a v0.1.0 -m "Aerolatch v0.1.0"
git push origin v0.1.0
```

This starts the **Release** workflow. The tag must point to a commit contained in
`main`. After the checks pass, GoReleaser publishes a GitHub Release containing:

- `aerolatch_0.1.0_linux_amd64.tar.gz`
- `aerolatch_0.1.0_linux_arm64.tar.gz`
- `aerolatch_0.1.0_darwin_amd64.tar.gz`
- `aerolatch_0.1.0_darwin_arm64.tar.gz`
- `aerolatch_0.1.0_windows_amd64.zip`
- `aerolatch_0.1.0_windows_arm64.zip`
- `checksums.txt`

Archives contain the executable, README, screenshot, and usage documentation.
Release notes are generated from commits since the previous tag; the first
release uses the available commit history.

Use a tag such as `v0.2.0-rc.1` for a prerelease. It is marked as a prerelease and
does not become the latest stable release. Use a new version tag for each release;
published tags should stay associated with their original commits.

Watch progress in the repository's **Actions** tab or with GitHub CLI:

```sh
gh run list --workflow release.yml
gh release list
```

If a run fails because of a transient service problem, rerun the failed jobs from
Actions. If a code or configuration fix is needed, merge the fix and use a new
version tag.

## Validate locally without publishing

Install [GoReleaser](https://goreleaser.com/install/) **v2.19.0**, matching the
workflows, and use the latest stable Go. From the repository root:

```sh
go mod download
go mod verify
go vet ./...
go test -race -count=1 -timeout=5m ./...
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
goreleaser check
goreleaser release --snapshot --clean --skip=publish
```

Check formatting with `gofmt -l .`; it should print no files. Snapshot packaging
builds all six targets into the ignored `dist/` directory without creating tags,
uploading assets, or publishing a release. It can run before the first version
tag exists. `--clean` replaces the previous `dist/` output.

On Linux, verify every generated archive:

```sh
(cd dist && sha256sum --check checksums.txt)
```

The `release-snapshot` artifact from CI contains the same platform/archive layout
and can be downloaded to try binaries before a release.

## Maintenance

The **CI passed** job fails if any prerequisite fails or is skipped. Select this
check in a GitHub ruleset or branch protection rule to require the entire CI
suite before merging. Repository rules are configured separately from workflow
files.

Weekly runs also check for newly disclosed reachable vulnerabilities using the
current Go vulnerability database. The pinned `govulncheck` and `actionlint`
versions are recorded in CI and the commands above. Keep these versions aligned
when updating them. Keep the pinned GoReleaser version aligned in both workflows
and this document, and validate a snapshot after changing it. Dependabot updates
Action references, not tool version inputs or versions embedded in shell commands.
