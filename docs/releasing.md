# Releasing the SDKs

Pushing a tag like `v0.4.0` runs [`.github/workflows/sdks.yml`](../.github/workflows/sdks.yml),
which publishes:

| Registry | Package | Source |
| --- | --- | --- |
| npm | `visionary-sdk` | `sdks/node` |
| npm | `visionary-browser` | `sdks/browser` |
| PyPI | `visionary-sdk` | `sdks/python` |
| RubyGems | `visionary-sdk` | `sdks/ruby` |
| NuGet | `Visionary.AspNetCore` | `sdks/dotnet` |
| Maven Central | `io.github.styropyr0:visionary-spring-boot-starter` | `sdks/java` |
| Maven Central | `io.github.styropyr0:visionary-android` | `sdks/android` |

Some packages need no upload:

- **JitPack** (`com.github.styropyr0:Visionary:<tag>`) is triggered by
  [`android.yml`](../.github/workflows/android.yml).
- **Swift Package Manager** resolves the tag through the root [`Package.swift`](../Package.swift).
- **Go**: `sdks/go` is its own module. The workflow pushes a matching `sdks/go/v0.4.0` tag,
  which is what `go get github.com/styropyr0/visionary/sdks/go@v0.4.0` resolves.
- **Host agent**: the `visionary-agent-linux-amd64` and `-arm64` binaries (plus `SHA256SUMS`)
  are attached to the GitHub Release; the image `ghcr.io/styropyr0/visionary-agent` is built by
  [`image.yml`](../.github/workflows/image.yml).
- **CocoaPods** is skipped on purpose: its trunk is being sunset (it is scheduled to become read-only).

The version always comes from the tag (`v0.4.0` becomes `0.4.0`, and `v0.4.0-rc.1` becomes a
prerelease). CI writes it into each manifest at build time and commits nothing, so nobody bumps
versions by hand. The versions in the files (`0.1.0`, `0.1.0-SNAPSHOT`, `dev`, ...) are only for
local builds. Each SDK still reports the released version as `sdk_version`, because the value
CI rewrites is the one the SDK reads:

| SDK | Where `sdk_version` comes from |
| --- | --- |
| Python | `__version__` in `src/visionary/__init__.py` (also the package version) |
| Ruby | `Visionary::VERSION` |
| Node | the `version` in `package.json`, baked in by tsup |
| .NET | `AssemblyInformationalVersion`, from `-p:Version` |
| Java | `Implementation-Version` in the jar manifest, from the pom version |

Each registry has its own job, so one failing registry does not block the others. Every job
first checks whether its version is already on the registry, and skips the upload with a notice
if it is. That means a run can always be re-run. Once the publish jobs finish, a last job adds a
"Packages" table to the GitHub Release for the tag, creating the release if it does not exist.
Any notes already on the release are kept.

Wherever possible, publishing uses **trusted publishing** (OpenID Connect), so there are no
long-lived tokens: PyPI, npm, RubyGems and NuGet all work this way. Maven Central has no such
option, so it uses a Central Portal token and a GPG key, stored as secrets.

> The registry steps below are written from each registry's documentation as of October 2026.
> Registries change these screens often. Where something below is uncertain, it says so; trust
> the registry's own page over this file.

## One-time setup

Do these steps once, before pushing the first tag. Wherever a form asks for these values, the
values are always the same:

| Field | Value |
| --- | --- |
| Owner / organization / user | `styropyr0` |
| Repository | `Visionary` |
| Workflow file | `sdks.yml` (file name only, no `.github/workflows/`) |

The environment name is different for each registry (`pypi`, `npm`, `rubygems`, `nuget`,
`maven-central`).

### 1. GitHub environments, secrets and variables

Under **Settings > Environments** in https://github.com/styropyr0/Visionary/settings/environments,
create these five environments: `npm`, `pypi`, `rubygems`, `nuget` and `maven-central`.

For each one, it is worth adding a **Deployment branches and tags** rule set to "Selected
branches and tags", with the tag pattern `v*`. Then only release tags can publish. With that
rule, a manual run has to be started *from the tag* (see "Re-running" below).

In the **`maven-central`** environment, add these secrets:

| Secret | Value |
| --- | --- |
| `MAVEN_CENTRAL_USERNAME` | the *username* of a Central Portal user token (step 6) |
| `MAVEN_CENTRAL_PASSWORD` | the *password* of that user token |
| `MAVEN_GPG_PRIVATE_KEY` | the ASCII-armored private key (step 7), including the `-----BEGIN PGP PRIVATE KEY BLOCK-----` and `-----END ...-----` lines |
| `MAVEN_GPG_PASSPHRASE` | that key's passphrase |

Under **Settings > Secrets and variables > Actions > Variables**, add this repository variable:

| Variable | Value |
| --- | --- |
| `NUGET_USER` | your nuget.org **profile name** (not your email) |

Two items are optional:

- The secret `NPM_TOKEN` in the `npm` environment, only for the first npm release (step 3).
- The variable `NPM_STAGED_PUBLISH` = `true`, if you want npm releases staged for approval
  instead of published directly (step 3).

### 2. PyPI: pending trusted publisher

The project does not exist yet, so register a **pending** publisher. It creates `visionary-sdk`
on the first upload.

1. Sign in at https://pypi.org (the account needs a verified email and 2FA).
2. Go to https://pypi.org/manage/account/publishing/, then "Add a new pending publisher", then
   the **GitHub** tab.
3. Enter:
   - PyPI Project Name: `visionary-sdk`
   - Owner: `styropyr0`
   - Repository name: `Visionary`
   - Workflow name: `sdks.yml`
   - Environment name: `pypi`
4. Click **Add**. After the first successful publish, it turns into a normal trusted publisher
   for the project.

A pending publisher does not reserve the name. If someone else registers `visionary-sdk` first,
the pending publisher stops working. In October 2026 the name was free.

### 3. npm: first publish, then trusted publishing

npm's trusted publisher is set up on the package's own settings page, so as far as the npm docs
show, the package has to exist first. Unlike PyPI and RubyGems, npm has no "pending" publisher.
If npmjs.com offers one when you do this, use it and skip step 3a. The trusted-publishing docs
are at https://docs.npmjs.com/trusted-publishers.

You need an npm account with 2FA enabled.

**3a. Bootstrap the first release with a short-lived token.** This is the simplest route, and the
first versions still get provenance.

1. Go to `https://www.npmjs.com/settings/<your npm username>/tokens` (Access Tokens). Choose
   **Generate New Token > Granular Access Token**:
   - Expiration: 7 days
   - Packages and scopes: **Read and write**, **All packages** (the packages do not exist yet,
     so you cannot pick them)
   - Tick **Bypass two-factor authentication**, because CI cannot type a one-time code.
2. Add it as the secret `NPM_TOKEN` in the `npm` GitHub environment.
3. Push the release tag. The two npm jobs publish `visionary-sdk` and `visionary-browser` with
   that token.

Alternatively, publish the first version by hand. From `sdks/node`, then again from
`sdks/browser`, run:

```sh
npm login
npm ci
npm version 0.4.0 --no-git-tag-version
npm publish --provenance=false   # provenance can only be generated in CI
git checkout package.json package-lock.json
```

The workflow then skips that version on npm.

**3b. Add the trusted publisher to each package.** Do this for both `visionary-sdk` and
`visionary-browser`. On https://www.npmjs.com/package/visionary-sdk, go to **Settings** and find
**Trusted Publisher**, then choose **GitHub Actions** and enter:

- Organization or user: `styropyr0`
- Repository: `Visionary`
- Workflow filename: `sdks.yml`
- Environment name: `npm`
- Allowed actions: tick **npm publish**.

  According to the docs, publishers created after 3 September 2026 allow only
  `npm stage publish` unless you tick **npm publish**. If you would rather approve every release
  by hand, leave **npm publish** unticked and set the repository variable
  `NPM_STAGED_PUBLISH=true`. The workflow then runs `npm stage publish`, and you approve each
  staged version on npmjs.com (Staged Packages tab), with 2FA.

The docs also say that a new trusted publisher has to complete its first successful publish
within 2 days, or it expires. Add it shortly before the next release, or delete and re-create it
if it expires.

**3c. Remove the token.** Delete the `NPM_TOKEN` secret and revoke the token on npmjs.com. Then,
optionally, under each package's **Settings > Publishing access**, choose "Require two-factor
authentication and disallow tokens". Trusted publishing keeps working with that setting.

npm also refuses names it considers too similar to an existing package. `visionary-sdk` and
`visionary-browser` were unused in October 2026, but that rule is only checked when you publish.

### 4. RubyGems: pending trusted publisher

1. Sign in at https://rubygems.org. The gem sets `rubygems_mfa_required`, so enable MFA on the
   account.
2. Go to https://rubygems.org/profile/oidc/pending_trusted_publishers and click **Create**.
3. Enter:
   - Gem name: `visionary-sdk`
   - Repository owner: `styropyr0`
   - Repository name: `Visionary`
   - Workflow filename: `sdks.yml`
   - Environment: `rubygems`
   - Leave "Workflow Repository Owner/Name" empty.
4. Click **Create Pending trusted publisher**. After the first push, it becomes a normal trusted
   publisher, and you become the gem's owner.

I have not confirmed how `rubygems_mfa_required` interacts with pushes from trusted publishing.
The RubyGems guide does not mention it. If the first push fails with an MFA/OTP error, remove `"rubygems_mfa_required" => "true"` from
`sdks/ruby/visionary-sdk.gemspec` and push a new tag.

### 5. NuGet: trusted publishing policy

1. Sign in at https://www.nuget.org.
2. Click your username, choose **Trusted Publishing**, and add a policy:
   - Policy name: `visionary-sdks` (any name works)
   - Package owner: your user
   - Repository Owner: `styropyr0`
   - Repository: `Visionary`
   - Workflow File: `sdks.yml`
   - Environment: `nuget`
   - If the form shows policy scopes or a package glob, allow **pushing new packages** as well
     as new versions, and use the pattern `Visionary.AspNetCore` (or `Visionary.*`).
3. Set the repository variable `NUGET_USER` to the same profile name (step 1).

NuGet may show the policy as "temporarily active for 7 days" until its first publish. That
happens mostly with private repositories. If no publish happens within that window, restart the
window on the same page.

The package ID `Visionary.AspNetCore` was unused in October 2026. If someone has reserved the
`Visionary` ID prefix by the time you publish, nuget.org rejects the push, and the package needs
a different ID.

### 6. Maven Central: namespace and user token

1. Sign in at https://central.sonatype.com with **GitHub**, as `styropyr0`. Signing in with
   GitHub creates the `io.github.styropyr0` namespace and verifies it automatically. Check that
   it shows as verified under **Namespaces** (https://central.sonatype.com/publishing/namespaces).
   If you already have an account that is not linked to GitHub, add the namespace
   `io.github.styropyr0` there instead. The portal asks you to create a temporary public GitHub
   repository with a name it gives you, to prove that you own the account. Delete that
   repository once the namespace is verified.
2. Open https://central.sonatype.com/usertoken (your avatar, then **View Account**, then
   **Generate User Token**). Copy the generated *username* and *password* into
   `MAVEN_CENTRAL_USERNAME` and `MAVEN_CENTRAL_PASSWORD` (step 1). This is not your login
   password.

The Java and Android jobs share these secrets and the GPG key. Both publish with automatic
release. Central validates each upload (POM fields, sources, javadoc, signatures) and publishes
it to Maven Central, where a new version can take up to about 30 minutes to appear. Central
cannot replace or delete a version once it is published.

### 7. GPG signing key

Maven Central requires every file to be signed, and needs the public key on a public keyserver.

```sh
gpg --full-generate-key
#   kind: RSA and RSA, size 4096, expiry e.g. 2y,
#   name "styropyr0", your email, and a strong passphrase
gpg --list-secret-keys --keyid-format long
#   sec   rsa4096/ABCDEF0123456789 ...  <- the key id is after the slash
gpg --keyserver keyserver.ubuntu.com --send-keys ABCDEF0123456789
gpg --armor --export-secret-keys ABCDEF0123456789 > visionary-signing.asc
```

- Put the contents of `visionary-signing.asc` into the `MAVEN_GPG_PRIVATE_KEY` secret and the
  passphrase into `MAVEN_GPG_PASSPHRASE`. Then delete the file.
- Keep a backup of the key and of its revocation certificate (in `~/.gnupg/openpgp-revocs.d/`)
  somewhere safe.
- Keyserver propagation can take a few minutes. To check it:
  `gpg --keyserver keyserver.ubuntu.com --recv-keys ABCDEF0123456789`.
  Central also accepts keys from `keys.openpgp.org` (which needs email verification) and
  `pgp.mit.edu`.
- Use RSA. The Java job signs with the `gpg` binary, but the Android job signs in memory through
  Gradle, and I have not checked whether Gradle's in-memory signing accepts Ed25519 keys.

### 8. Turn Maven Central on

The two Maven Central jobs are skipped until the repository variable `MAVEN_CENTRAL_ENABLED` is
`true`, so a release never fails half-configured. Once all four `maven-central` secrets are set:

```sh
gh variable set MAVEN_CENTRAL_ENABLED --body true --repo styropyr0/Visionary
```

Then publish an already-released tag to Maven Central with
`gh workflow run sdks.yml --ref vX.Y.Z -f tag=vX.Y.Z` (the other registries skip the version
they already have).

## Cutting a release

1. Make sure `main` contains what you want to ship. Nothing needs bumping.
2. Tag the release and push the tag:

   ```sh
   git tag -a v0.4.0 -m "v0.4.0"
   git push origin v0.4.0
   ```

   The tag push starts three workflows:

   - **SDK packages** (`sdks.yml`)
   - **Android SDK** (JitPack, `android.yml`)
   - the Docker image (`image.yml`)

3. Watch the run at https://github.com/styropyr0/Visionary/actions/workflows/sdks.yml.
   - The summary of the "GitHub Release" job shows each package's status.
   - The same table is added to the release at https://github.com/styropyr0/Visionary/releases/tag/v0.4.0.
   - You can add your own release notes before or after the run.

A tag with a suffix, like `v0.4.0-rc.1`, is a prerelease:

- npm publishes it under the `next` dist-tag, so `npm install` still gets the latest stable version.
- PyPI and RubyGems store it as `0.4.0rc1` and `0.4.0.pre.rc.1`.
- NuGet and Maven Central store it as `0.4.0-rc.1`.
- The GitHub Release is marked as a prerelease.

### Re-running

- **Re-run failed jobs** on the run page. Jobs whose version is already on their registry skip
  with a notice, so a re-run never double-publishes.
- To publish an existing tag again (for example after fixing a registry's setup), run the
  workflow by hand from the tag itself:

  ```sh
  gh workflow run sdks.yml --ref v0.4.0 -f tag=v0.4.0
  ```

  You can also use the Actions tab: choose "Run workflow", then under "Use workflow from" pick
  **Tags: v0.4.0**. Running it from `main` works only if the environments do not restrict
  deployments to `v*` tags.
- A fix to the SDK code needs a new tag (for example `v0.4.1`). Registries do not let a version
  be replaced. npm, PyPI and RubyGems also keep the number taken after a yank or unpublish.

## Checking the packages locally

These are the same build steps CI runs, minus the upload.

```sh
# npm (each of sdks/node and sdks/browser)
npm ci && npm run build && npm pack --dry-run

# PyPI
cd sdks/python && cp ../../LICENSE . && python -m build && twine check --strict dist/*

# RubyGems
cd sdks/ruby && cp ../../LICENSE . && gem build visionary-sdk.gemspec

# NuGet
cd sdks/dotnet/Visionary.AspNetCore && dotnet pack -c Release -p:Version=0.4.0 -o /tmp/nuget

# Maven Central, Java (no GPG key needed with -Dgpg.skip)
cd sdks/java && mvn -B -Prelease -Dgpg.skip verify

# Maven Central, Android (unsigned without a key; this is also what JitPack runs)
cd sdks/android && VERSION=v0.4.0 ./gradlew :visionary:publishReleasePublicationToMavenLocal
```

The copied `LICENSE` files are ignored by git. The npm packages copy `LICENSE` themselves when
packing.
