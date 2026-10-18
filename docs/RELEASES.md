# Releases and updates

Releases are built by GitHub Actions (`.github/workflows/release.yml`) when a
`v*` tag is pushed. Running sites notice new releases on their own and tell
the administrator; the operator installs one with `sudo sap13ns update`.
Every release is signed: a binary only installs an update whose checksums
are signed by the key it was built with.

## One-time setup: the signing key

Generate an Ed25519 key pair on a trusted machine:

```sh
go run ./scripts/releasesign keygen -out sap13ns-signing.key
```

It writes the private key to `sap13ns-signing.key` (mode 0600) and prints the
public key. Store both in the repository (Settings > Secrets and variables >
Actions), or with the GitHub CLI:

```sh
gh variable set SAP13NS_UPDATE_PUBKEY --body '<public key>'
gh secret set SAP13NS_SIGNING_KEY < sap13ns-signing.key
```

| Name                    | Kind     | Contents                                                     |
| ----------------------- | -------- | ------------------------------------------------------------ |
| `SAP13NS_UPDATE_PUBKEY` | variable | base64 public key, compiled into every binary                |
| `SAP13NS_SIGNING_KEY`   | secret   | base64 private key, signs `SHA256SUMS` during the release    |

Keep an offline copy of the private key and delete it from the workstation.
Without the variable or the secret the release workflow fails, and it also
fails if they are not a matching pair.

If the key is lost or leaked, generate a new pair and replace both. Sites
running a binary built with the old public key cannot verify releases signed
with the new one: update those once by hand (download the tarball from the
release page, check it with `sha256sum -c SHA256SUMS`, replace the binary).

## Cutting a release

```sh
git tag -a v1.4.0 -m v1.4.0
git push origin v1.4.0
```

The workflow checks that the tag is `vMAJOR.MINOR.PATCH`, builds the frontend
assets (`npm ci && npm run build`), runs `go vet` and `go test ./...`, builds
static binaries (CGO off, `-trimpath`) with the version and the public key
embedded, then publishes a GitHub release with generated notes and these
assets:

| Asset                                        | Contents                                    |
| -------------------------------------------- | ------------------------------------------- |
| `sap13ns_1.4.0_linux_amd64.tar.gz`           | `sap13ns`, plus `LICENSE`/`README.md` if present |
| `sap13ns_1.4.0_linux_arm64.tar.gz`           | same                                         |
| `sap13ns_1.4.0_darwin_amd64.tar.gz`          | same                                         |
| `sap13ns_1.4.0_darwin_arm64.tar.gz`          | same                                         |
| `SHA256SUMS`                                 | `sha256sum` of every tarball                 |
| `SHA256SUMS.sig`                             | base64 Ed25519 signature over `SHA256SUMS`   |

The version in the file names has no `v`. `sap13ns update` depends on these
names, so do not rename or re-upload assets by hand. A tag with a suffix
(`v1.4.0-rc.1`) is published as a prerelease, which sites ignore. You can
edit the release notes afterwards; the admin panel shows them.

To check a release by hand:

```sh
sha256sum -c --ignore-missing SHA256SUMS
go run ./scripts/releasesign verify -pub '<public key>' SHA256SUMS SHA256SUMS.sig
```

## How a site learns about a new release

A running server asks
`https://api.github.com/repos/vicktor/sap13ns/releases/latest` a random 0 to
10 minutes after it starts, then every 24 hours (or one hour after a failed
check). It keeps the answer in memory only. When a newer version exists, the
admin panel says so and links to the release notes. Drafts, prereleases and
local builds (version `dev`) never trigger the notice.

To turn the check off (no outbound requests at all), add this to
`/etc/sap13ns/sap13ns.env` and restart:

```sh
SAP13NS_UPDATE_CHECK=false
```

## Installing an update

Take a backup first, then update and restart:

```sh
sudo systemctl start sap13ns-backup     # optional but recommended
sudo sap13ns update
sudo systemctl restart sap13ns
```

`sap13ns update` needs `sudo` because it replaces `/usr/local/bin/sap13ns`.
It needs outbound HTTPS to `api.github.com`, `github.com`,
`objects.githubusercontent.com` and `release-assets.githubusercontent.com`.
It:

1. refuses if the binary was built without the public key (no unsigned updates);
2. downloads `SHA256SUMS` and `SHA256SUMS.sig` and verifies the signature;
3. downloads the tarball for this platform and checks its SHA-256;
4. extracts only the `sap13ns` file into `sap13ns.new` next to the binary,
   with the same mode and owner;
5. runs `sap13ns.new version` and checks it reports the release version;
6. keeps the current binary as `sap13ns.old` and atomically renames
   `sap13ns.new` over `sap13ns`.

If any step fails the installed binary is left as it was. The running server
keeps the old code until it is restarted; it applies any new database
migrations when it starts.

## Rolling back

The previous binary is kept as `sap13ns.old` (one generation):

```sh
sudo mv /usr/local/bin/sap13ns.old /usr/local/bin/sap13ns
sudo systemctl restart sap13ns
```

This only swaps the binary. If the new version migrated the database on
start, the old one may not work with the newer schema; restore the backup
taken before the update (`sap13ns backup restore`) in that case.
