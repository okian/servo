# Releasing

How a version of servo is cut, published and verified. The workflows are
[cut-release.yml](.github/workflows/cut-release.yml) and
[release.yml](.github/workflows/release.yml); this page is the maintainer's side of them, and
the user's side of what they produce.

A release is two things at once. It is a **module version**: a tag the Go module proxy serves to
`go get` and `go install`, recorded for good in the checksum database the first time anyone asks
for it. And it is a **GitHub release**: one archive per operating system and architecture, holding
the `servo` and `servo-vet` binaries, a checksums file, a build-provenance attestation per archive,
and the version's CHANGELOG.md section as the notes. The rules below exist because the first of
those can never be taken back.

## Cutting a release

1. Rename `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md) to `## [X.Y.Z] - <date>`, choosing
   the version by the rules in its Versioning section, and merge that to master.
2. Run the workflow from master:

   ```
   gh workflow run cut-release.yml -f version=vX.Y.Z
   ```

   (or **Actions → Cut a release → Run workflow**).

cut-release.yml checks, in order, before any tag exists:

- the run is on master, and the version is a semantic version that does not exist yet and comes
  after the latest release (a release cuts master's head; a backport is not something it makes);
- the version's major matches go.mod's module path (`/v3` for every `v3.x.y`);
- CHANGELOG.md has the version's section (a pre-release such as `v3.5.0-rc.1` may use
  `[Unreleased]` instead);
- [go.yml](.github/workflows/go.yml) has passed on the commit — both supported toolchains, every
  example, `servo check`, lint. If go.yml is still running, it waits, up to 30 minutes;
- [gorelease](https://pkg.go.dev/golang.org/x/exp/cmd/gorelease), comparing the exported API with
  the previous release, agrees the version fits the change: a removal needs a new major, and an
  addition cannot ship as a patch.

Then it creates an annotated tag on the commit it checked and starts release.yml on the tag. (A
tag created with the workflow's own token starts no workflow by itself, hence the explicit start.)
release.yml checks the tag once more, waits for go.yml if it has to, builds every archive with the
newest patch release of the Go line in its `go-version`, tests the module under that same
toolchain, attests the archives, verifies every attestation, and publishes. The release is created
as a draft and made public only once every asset is attached.

### Pushing a tag by hand

Still works: a pushed `v*` tag starts release.yml directly. The tag must be on master, on a commit
go.yml has already passed (release.yml waits for go.yml on that commit, and stops if it failed or
never ran — `gh workflow run go.yml --ref <tag>` gives it a run), and CHANGELOG.md at that commit
must have the version's section. None of that is checked before the tag exists, which is the
reason cut-release.yml is the way to release.

### Re-running a release

If a release run fails after the tag exists — a runner outage, a failed upload — run release.yml
again *on the tag*:

```
gh workflow run release.yml --ref vX.Y.Z
```

A draft left behind by the failed run is replaced. A published release is never rebuilt: its
checksums and attestations may already be in use. A run started on a branch is refused, because
the provenance attestation records the run's own ref as the archives' source.

## Never re-tag: retract and release the next patch

A tag is never moved, deleted and pushed again, not even minutes after it was pushed. The module
proxy keeps the first commit it saw under that version, and the checksum database makes that
permanent, so a moved tag hands `go install` one build and the release page another under the same
name. Everything wrong with a published version is fixed by a newer one.

If a version must not be used, retract it in the go.mod of the next one:

```
// go.mod, in the release that fixes it
retract v3.5.0 // Generates code that does not compile for routes in another package main.
```

A range works too: `retract [v3.5.0, v3.5.2]`. Retracted versions stay downloadable, so existing
builds keep working, but `go get` and `go list -m -u` warn about them, and `@latest` skips them.
Edit the retracted release's notes to point at the fix rather than deleting the release.

## Immutable releases

GitHub's immutable releases (a repository setting, off today) lock a published release's assets and
its tag. release.yml already publishes the way that requires: everything is attached to a draft
first, and the draft is made public in one step. What immutability removes is the last resort the
workflow used to describe — deleting a published release and running release.yml for its tag
again. With immutability on, a published release's tag can never carry another release, so the
only fix for a bad release is the one above: retract it and release the next patch. A draft left
by a failed run is not yet published, and is still replaced as usual.

## Verifying a download

Every archive has a line in `servo_vX.Y.Z_checksums.txt`, and a signed
[SLSA build-provenance attestation](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations)
naming the workflow, tag and commit that built it on a GitHub-hosted runner:

```
sha256sum --ignore-missing -c servo_vX.Y.Z_checksums.txt
gh attestation verify servo_vX.Y.Z_linux_amd64.tar.gz --repo okian/servo \
  --signer-workflow okian/servo/.github/workflows/release.yml \
  --source-ref refs/tags/vX.Y.Z
```

The two extra flags pin the attestation to the release workflow and to the tag; release.yml runs
exactly this command against every archive before it publishes. Releases before v3.5.0 have
checksums but no attestations.

**The archives install the CLI; they do not replace Go.** servo loads the module it generates for
through the `go` command, so `go` has to be on PATH wherever either binary runs. The recommended
install is the version pinned in your own go.mod — `go get -tool github.com/okian/servo/v3/cmd/servo`,
then `go tool servo` — so every developer and CI runner uses the same one. The archives are for
places where building servo is unwanted: a CI image that only runs `servo check`, a container
stage that copies a binary in.

**macOS.** The darwin binaries are only ad-hoc signed, not notarized, so a copy downloaded with a
browser carries the quarantine attribute and Gatekeeper refuses to open it. Remove the attribute
after verifying the download (`xattr -d com.apple.quarantine servo servo-vet`), download with
`curl` or `gh release download`, which set no quarantine, or install with `go install` or
`go get -tool`, which build locally.

## Rebuilding a binary bit for bit

The binaries are reproducible: anyone can rebuild one from the tag and get the same bytes. The
archives themselves are not — tar and zip record timestamps — so compare the binaries inside them.
The recipe is release.yml's build step:

```
git clone https://github.com/okian/servo.git && cd servo
git checkout vX.Y.Z

# The toolchain the release notes name ("built with go1.27.N"), and no other
# Go setting: `go env -changed` should print nothing else.
export GOTOOLCHAIN=go1.27.N GOFLAGS= GOWORK=off
export CGO_ENABLED=0 GOOS=linux GOARCH=amd64   # the archive's system and architecture;
                                               # GOARM=6 or 7 for the linux_armv6/armv7 ones
go build -trimpath -ldflags='-s -w' -o servo ./cmd/servo
go build -trimpath -ldflags='-s -w' -o servo-vet ./cmd/servo-vet

mkdir published && tar -xzf servo_vX.Y.Z_linux_amd64.tar.gz -C published
sha256sum servo servo-vet published/servo published/servo-vet
```

The clone matters: the go command stamps the module version from the tag on HEAD, and a checkout
with stray files in it stamps `+dirty`, so build from a fresh clone, not from a module download or
a working copy. The host does not matter: v3.4.1's linux/amd64 binaries, rebuilt this way on an
arm64 Mac, match the published ones byte for byte.

## When Go ships a release

servo supports the two most recent Go releases, matching Go's own support window. When a new one
ships (the nightly canary in go.yml will have been running it since the day it did):

- in go.yml, the `test` and `examples` matrices drop the oldest version and add the new one, and
  the coverage `include` entry and the examples job's `!= '<oldest>'` condition move up with them;
- the root go.mod's `go` line, and every example's except the tutorial's, rise to the older of the
  two supported versions;
- every job pinned to the newest line — `go-version: '1.27'` today, in release.yml,
  cut-release.yml, vulncheck.yml, tutorial.yml and go.yml's single-version jobs — moves to the
  new one.

Dependabot proposes updates to every pinned action, module and gem weekly or monthly; review those
like any other change.
