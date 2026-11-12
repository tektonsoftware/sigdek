[Back to the README](../README.md)

# Installing the sigdek command

`npx sigdek …` works anywhere Node.js 18 or later is installed, with nothing to install first. To install the `sigdek` command instead, and type `sigdek` where the setup steps say `npx sigdek`:

| Where | Install |
|---|---|
| Any OS, with Node.js | `npm install -g sigdek` |
| macOS and Linux, without Node.js | `curl -fsSL https://sigdek.com/install.sh \| sh`, or without curl, `wget -qO- https://sigdek.com/install.sh \| sh` (wget's certificate checking depends on how it was built; prefer curl) |
| Windows, Scoop | `scoop bucket add sigdek https://github.com/tektonsoftware/sigdek` then `scoop install sigdek` |
| Go 1.24 or later | `go install github.com/tektonsoftware/sigdek/cmd/sigdek@latest` |
| Anything else | Download the binary for your platform from [GitHub releases](https://github.com/tektonsoftware/sigdek/releases) and check it against `checksums.txt` |

The install script and Scoop need no administrator.

**winget isn't available yet.** The release workflow submits each release to winget as a pull request to Microsoft's package repository, and the first submissions are waiting for Microsoft's review. Until one is merged, `winget install TektonSoftware.SigDek` finds no package; use Scoop, npm or the binary from GitHub releases. Once SigDek is in winget, each new release will reach it after Microsoft merges that release's pull request.

## The install script

The install script ([`install.sh`](../install.sh)) puts `sigdek` in `~/.local/bin`, without sudo, and tells you the line to add to your shell profile if that folder isn't on your `PATH`. Run it again to update. It checks the binary's SHA-256 against the release's `checksums.txt`.

Releases are immutable: once a release is published, its files and tag can't be changed, and GitHub attaches an attestation to each. The checksum still comes from the same release as the binary, so it catches a damaged or wrong download, not a release that was bad when it was published. The script itself reaches you through sigdek.com's redirect to the release. [SECURITY.md](../SECURITY.md) says how to check a release's attestation.

### Reading it first

To read the script before you run it:

```sh
curl -fsSLo install.sh https://sigdek.com/install.sh
less install.sh
sh install.sh
```

### Settings

Settings go after the pipe, on the `sh` that runs the script: `curl -fsSL https://sigdek.com/install.sh | SIGDEK_VERSION=0.1.0 sh` installs a given version, and `SIGDEK_INSTALL_DIR=<folder>` installs into another folder.

## What each channel installs

Every channel except `go install`, which compiles from source, installs the same binaries, built once per release.

- The npm package is a small launcher that runs the one for your platform from an `@tektonsoftware/sigdek-*` package.
- The Scoop manifest (`bucket/`) lives in this repository and is updated by the release workflow, which also opens the winget pull request (see above).
- The workflow writes a Homebrew formula (`Formula/`) too, but Homebrew isn't an install option yet: without prebuilt bottles, Homebrew on macOS refuses it when the Command Line Tools are out of date.
