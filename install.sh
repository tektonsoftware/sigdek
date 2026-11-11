#!/bin/sh
# Installs the SigDek recorder's `sigdek` command on macOS or Linux, without Node.js:
#
#   curl -fsSL https://sigdek.com/install.sh | sh
#
# It downloads the release binary for this machine from
# https://github.com/tektonsoftware/sigdek/releases, checks its SHA-256 against that release's
# checksums.txt, and puts it at ~/.local/bin/sigdek. It never uses sudo, writes only to that
# folder (creating it if needed) and to a temporary folder that it removes, and never edits
# your shell profile.
#
# What the check does: it catches a damaged or wrong download. The checksum comes from the same
# GitHub release as the binary, so it doesn't protect against a tampered release. That's the
# same trust as installing from npm.
#
# Running it again updates to the latest release. Settings:
#   SIGDEK_VERSION=0.1.0      install this version instead of the latest (a leading v is fine)
#   SIGDEK_INSTALL_DIR=<dir>  install into this folder instead of ~/.local/bin
#   SIGDEK_RELEASE_BASE=<url> for tests only: stands in for
#                             https://github.com/tektonsoftware/sigdek/releases, the base under
#                             which /latest and /download/v<version>/ live
#
# Source: https://github.com/tektonsoftware/sigdek/blob/main/install.sh

set -eu

DOCS=https://github.com/tektonsoftware/sigdek#getting-the-sigdek-command

say() { printf '%s\n' "$*"; }
die() {
  printf 'SigDek install failed: %s\n' "$*" >&2
  exit 1
}

# fetch <url> <file>: curl if it's here, otherwise wget. curl retries up to 3 times after a
# timeout or a temporary server error, and counts a download that stalls (under 1 KB/s for a
# minute) as timed out, so a slow download that keeps moving isn't cut off.
fetch() {
  if [ "$GET" = curl ]; then
    curl -fsSL --proto "$PROTO" --retry 3 --connect-timeout 15 --speed-limit 1024 --speed-time 60 -o "$2" "$1"
  else
    wget -q -T 60 -O "$2" "$1"
  fi
}

# The version that $BASE/latest redirects to (a page ending in /tag/v<version>), without the
# GitHub API and its rate limits.
latest() {
  if [ "$GET" = curl ]; then
    url=$(curl -sSIL --proto "$PROTO" --retry 3 --connect-timeout 15 --max-time 60 -o /dev/null -w '%{url_effective}' "$BASE/latest") ||
      die "couldn't reach $BASE/latest to find the latest version."
  else
    url=$(wget -S -T 60 -O /dev/null "$BASE/latest" 2>&1 | sed -n 's/^ *Location: *\([^ ]*\).*/\1/p' | tail -n 1)
  fi
  case "$url" in
    */tag/v*) say "${url##*/tag/v}" ;;
    *) die "couldn't find the latest version at $BASE/latest. Set SIGDEK_VERSION to the version you want, like SIGDEK_VERSION=0.1.0." ;;
  esac
}

sha256() {
  if [ "$SHA" = sha256sum ]; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{ print $1 }'
}

main() {
  BASE=${SIGDEK_RELEASE_BASE:-https://github.com/tektonsoftware/sigdek/releases}
  BASE=${BASE%/}
  # curl downloads only over HTTPS, redirects included (a test's base may be plain http).
  case "$BASE" in
    https://*) PROTO='=https' ;;
    *) PROTO='=http,https' ;;
  esac

  if command -v curl >/dev/null 2>&1; then
    GET=curl
  elif command -v wget >/dev/null 2>&1; then
    GET=wget
  else
    die "this needs curl or wget to download SigDek."
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    SHA=sha256sum
  elif command -v shasum >/dev/null 2>&1; then
    SHA=shasum
  else
    die "this needs sha256sum or shasum to check the download."
  fi

  S=$(uname -s)
  M=$(uname -m)
  case "$S" in
    Darwin) OS=darwin ;;
    Linux) OS=linux ;;
    *) die "this script installs SigDek on macOS and Linux only, and this machine runs $S. For other ways to install it, see $DOCS" ;;
  esac
  case "$M" in
    x86_64 | amd64) ARCH=amd64 ;;
    arm64 | aarch64) ARCH=arm64 ;;
    *) die "there's no SigDek build for $M processors. See $DOCS" ;;
  esac
  # A shell running under Rosetta on an Apple silicon Mac still gets the Apple silicon build.
  if [ "$OS" = darwin ] && [ "$ARCH" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null)" = 1 ]; then
    ARCH=arm64
  fi

  if [ -n "${SIGDEK_VERSION:-}" ]; then
    VERSION=${SIGDEK_VERSION#v}
  else
    VERSION=$(latest) || exit 1
  fi
  case "$VERSION" in
    [0-9]*[!0-9A-Za-z.+-]* | [!0-9]* | '') die "\"$VERSION\" isn't a SigDek version, like 0.1.0." ;;
  esac

  if [ -n "${SIGDEK_INSTALL_DIR:-}" ]; then
    DIR=$SIGDEK_INSTALL_DIR
  elif [ -n "${HOME:-}" ]; then
    DIR=$HOME/.local/bin
  else
    die "HOME isn't set. Set SIGDEK_INSTALL_DIR to the folder to install into."
  fi
  case "$DIR" in
    /*) ;;
    *) DIR=$(pwd)/$DIR ;;
  esac
  DIR=${DIR%/}
  TARGET=$DIR/sigdek
  [ ! -d "$TARGET" ] || die "$TARGET is a folder, so sigdek can't be installed there. Set SIGDEK_INSTALL_DIR to another folder."

  NAME=sigdek_${VERSION}_${OS}_${ARCH}
  FROM=$BASE/download/v$VERSION
  # A template, because macOS's `mktemp -d` alone ignores TMPDIR.
  T=${TMPDIR:-/tmp}
  TMP=$(mktemp -d "${T%/}/sigdek-install.XXXXXX") || die "couldn't make a temporary folder in $T."
  say "Installing SigDek $VERSION ($OS/$ARCH) in $DIR."

  fetch "$FROM/checksums.txt" "$TMP/checksums.txt" ||
    die "couldn't download $FROM/checksums.txt. Check that v$VERSION is a SigDek release: $BASE"
  WANT=$(awk -v n="$NAME" '$2 == n || $2 == "*" n { print $1; exit }' "$TMP/checksums.txt")
  case "$WANT" in
    *[!0-9a-f]* | '') WANT= ;;
  esac
  [ "${#WANT}" -eq 64 ] || die "the checksums.txt of release v$VERSION has no SHA-256 for $NAME, so nothing was installed."

  if [ -f "$TARGET" ] && [ -x "$TARGET" ] && [ "$(sha256 "$TARGET")" = "$WANT" ]; then
    say "SigDek $VERSION is already installed there."
  else
    fetch "$FROM/$NAME" "$TMP/$NAME" || die "couldn't download $FROM/$NAME, so nothing was installed."
    GOT=$(sha256 "$TMP/$NAME")
    [ "$GOT" = "$WANT" ] || die "the downloaded $NAME doesn't match its SHA-256 in checksums.txt (got $GOT, expected $WANT), so nothing was installed."
    say "Checked $NAME against the release's checksums.txt."
    # Copy into the install folder under a temporary name, then rename over any earlier copy
    # in one step, so a sigdek that's running or starting never sees half a file.
    mkdir -p "$DIR" || die "couldn't create $DIR. Set SIGDEK_INSTALL_DIR to a folder you can write to."
    STAGE=$DIR/.sigdek.install.$$
    if ! { cp "$TMP/$NAME" "$STAGE" && chmod 755 "$STAGE" && mv -f "$STAGE" "$TARGET"; }; then
      die "couldn't write $TARGET. Set SIGDEK_INSTALL_DIR to a folder you can write to."
    fi
    STAGE=
  fi

  OUT=$("$TARGET" version </dev/null 2>&1) || die "$TARGET is installed but didn't run: $OUT"
  say "Installed $TARGET: $OUT"

  # Which sigdek a new shell would run.
  FOUND=$(command -v sigdek 2>/dev/null || true)
  FOUND=${FOUND%/sigdek}
  FOUND=${FOUND%/}
  case ":${PATH:-}:" in
    *":$DIR:"* | *":$DIR/:"*) ONPATH=yes ;;
    *) ONPATH= ;;
  esac
  RUN=$TARGET
  if [ "$FOUND" = "$DIR" ]; then
    RUN=sigdek
  elif [ -n "$ONPATH" ] && [ -n "$FOUND" ]; then
    say ""
    say "Another sigdek, in $FOUND, comes before $DIR on your PATH, so the command sigdek runs that one. Remove it, or run this one as $TARGET."
  else
    SHOWN=$DIR
    if [ -n "${HOME:-}" ]; then
      case "$DIR" in "$HOME"/*) SHOWN="\$HOME/${DIR#"$HOME"/}" ;; esac
    fi
    LINE="export PATH=\"$SHOWN:\$PATH\""
    case "${SHELL:-}" in
      */zsh) PROFILE=.zshrc ;;
      */bash) if [ "$OS" = darwin ]; then PROFILE=.bash_profile; else PROFILE=.bashrc; fi ;;
      */fish) PROFILE=.config/fish/config.fish LINE="fish_add_path \"$SHOWN\"" ;;
      *) PROFILE=.profile ;;
    esac
    say ""
    say "$DIR isn't on your PATH. To add it, put this line in ~/$PROFILE, then open a new terminal:"
    say "  $LINE"
    say "Until then, run sigdek by its full path, $TARGET."
  fi

  say ""
  say "Next, connect this machine to your team:"
  say "  $RUN login"
}

TMP=
STAGE=
cleanup() {
  [ -z "$STAGE" ] || rm -f "$STAGE"
  [ -z "$TMP" ] || rm -rf "$TMP"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

# Everything runs from here, the last line, so a script cut short in download does nothing.
main "$@"
