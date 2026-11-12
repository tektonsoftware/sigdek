[Back to the README](../README.md)

# Developing SigDek

Go 1.24 or later. The recorder uses only Go's standard library.

```sh
go vet ./...
go test ./...                    # unit tests plus end-to-end tests of the compiled binary
go build -o sigdek ./cmd/sigdek
export SIGDEK_BIN=$PWD/sigdek    # launchers use your build; start Claude Code or Cursor from this shell
```

`SIGDEK_BIN` works for anyone, which is also a way to switch the recorder off; see [Can be switched off](recording.md#can-be-switched-off-by-anyone-using-the-machine).

To try a build without touching your own setup, also set `SIGDEK_HOME` to a temporary folder: it keeps the log, the device and the rules away from `~/.sigdek`.

## Releases

Push a tag like `v0.3.0`. The release workflow stops at once if the repository is private, since nobody could download the release and npm would publish it without provenance. Otherwise it:

- builds six targets (macOS, Linux and Windows, each on amd64 and arm64) and publishes them with `checksums.txt` and `install.sh`;
- publishes the npm packages with provenance, using the `NPM_TOKEN` secret when it is set and npm's trusted publishing when it isn't;
- when the tag is the highest published version, makes it the latest release, pins both plugins, the Homebrew formula and the Scoop manifest to it, and submits it to winget (when the `WINGET_TOKEN` secret is set).
