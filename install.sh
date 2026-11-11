#!/bin/sh
# Installs the SigDek recorder's `sigdek` command on macOS or Linux, without Node.js:
#
#   curl -fsSL https://sigdek.com/install.sh | sh
#   wget -qO- https://sigdek.com/install.sh | sh     (on a machine without curl)
#
# It downloads the release binary for this machine from
# https://github.com/tektonsoftware/sigdek/releases, checks its SHA-256 against that release's
# checksums.txt, and puts it at ~/.local/bin/sigdek. It never uses sudo, writes only to that
# folder (creating it if needed) and to a temporary folder that it removes, and never edits
# your shell profile. (Where wget is wget2, as on recent Fedora, wget2 may also update its own
# caches in your home folder.)
#
# Releases are immutable: once a release is published, its files and tag can't be changed, and
# GitHub attaches an attestation to each. The checksum still comes from the same release as the
# binary, so it catches a damaged or wrong download, not a release that was bad when it was
# published. The script itself reaches you through sigdek.com's redirect to the release.
#
# Running it again updates to the latest release. Settings go after the pipe, on the sh that
# runs the script:
#
#   curl -fsSL https://sigdek.com/install.sh | SIGDEK_VERSION=0.1.0 sh
#
#   SIGDEK_VERSION=0.1.0      install this version instead of the latest (a leading v is fine)
#   SIGDEK_INSTALL_DIR=<dir>  install into this folder instead of ~/.local/bin
#   SIGDEK_RELEASE_BASE=<url> for tests only: stands in for
#                             https://github.com/tektonsoftware/sigdek/releases, the base under
#                             which /latest and /download/v<version>/ live
#
# Source: https://github.com/tektonsoftware/sigdek/blob/main/install.sh

tell() { printf '%s\n' "$*"; }

die() {
  printf 'SigDek install failed: %s\n' "$*" >&2
  exit 1
}

# fetch <url> <file>: one try at downloading url into file. curl downloads only over HTTPS,
# redirects included (a test's base may be plain http), and counts a download that stalls
# (under 1 KB/s for a minute) as failed, so a slow download that keeps moving isn't cut off.
# GNU Wget gives up after 60 seconds without data, and keeps its HSTS list in the temporary
# folder. It still follows a redirect to plain http: its --https-only covers only recursive
# downloads. Other wgets (BusyBox, wget2) get plain flags. Whether a wget checks certificates
# depends on how it was built (some BusyBox builds don't).
fetch() {
  if [ "$GET" = curl ]; then
    curl -fsSL --proto "$PROTO" --connect-timeout 15 --speed-limit 1024 --speed-time 60 -o "$2" "$1"
  elif [ -n "$GNU" ]; then
    wget -nv --tries=1 -T 60 ${HTTPS:+--https-only} ${HSTS:+"--hsts-file=$TMP/wget-hsts"} -O "$2" "$1"
  else
    wget -q -T 60 -O "$2" "$1"
  fi
}

# follow <url>: one try at following url's redirects. curl writes the address it ends at
# to $TMP/latest; wget prints each response's headers, Location among them. An HTTP error
# still counts as an answer here: latest looks at where the redirect went.
follow() {
  if [ "$GET" = curl ]; then
    curl -sSIL --proto "$PROTO" --connect-timeout 15 --max-time 60 -o /dev/null -w '%{url_effective}' "$1" >"$TMP/latest"
  elif [ -n "$GNU" ]; then
    wget -nv -S --tries=1 -T 60 ${HTTPS:+--https-only} ${HSTS:+"--hsts-file=$TMP/wget-hsts"} -O /dev/null "$1" || [ $? -eq 8 ]
  else
    wget -S -T 60 -O /dev/null "$1" || [ $? -eq 8 ]
  fi
}

# persist <command> <url> [<argument>...]: runs the command up to 3 times, whatever the failure,
# 2 seconds apart, with its messages in $TMP/err. On failure it returns the last try's exit
# status, with that try's reason in $WHY.
persist() {
  n=1
  while :; do
    "$@" 2>"$TMP/err" && return 0
    code=$?
    # The last line curl or wget wrote, without control characters (it can quote the server).
    WHY=$(awk 'NF { l = $0 } END { print l }' <"$TMP/err" | tr -d '[:cntrl:]')
    [ -n "$WHY" ] || WHY="$GET exited with status $code"
    [ "$n" -lt 3 ] || return "$code"
    tell "Couldn't download $2 ($WHY). Trying again." >&2
    n=$((n + 1))
    sleep 2
  done
}

# download <url> <file> <after an HTTP error>: downloads url into file, or stops and says
# whether the server answered with an error (then the third argument ends the message) or the
# download itself failed.
download() {
  persist fetch "$1" "$2" && return 0
  code=$?
  # curl's and wget's exit status for an HTTP error response.
  if { [ "$GET" = curl ] && [ "$code" -eq 22 ]; } || { [ "$GET" = wget ] && [ "$code" -eq 8 ]; }; then
    die "couldn't download $1 ($WHY)$3"
  fi
  die "the download of $1 failed ($WHY), so nothing was installed. Check the network connection and run this again."
}

# latest: sets VERSION to the version that $BASE/latest redirects to (a page ending in
# /tag/v<version>), without the GitHub API and its rate limits.
latest() {
  persist follow "$BASE/latest" ||
    die "couldn't reach $BASE/latest to find the latest version ($WHY). Check the network connection and run this again."
  if [ "$GET" = curl ]; then
    url=$(cat "$TMP/latest")
  else
    url=$(tr -d '\r' <"$TMP/err" | sed -n 's/^ *[Ll][Oo][Cc][Aa][Tt][Ii][Oo][Nn]: *\([^ ]*\).*/\1/p' | tail -n 1)
  fi
  case "$url" in
    */tag/v*) VERSION=${url##*/tag/v} ;;
    *) die "couldn't find the latest version at $BASE/latest. Set SIGDEK_VERSION to the version you want, after the pipe: curl -fsSL https://sigdek.com/install.sh | SIGDEK_VERSION=0.1.0 sh" ;;
  esac
}

# checksum <file>: the file's SHA-256. The file comes in on stdin, so its name, whatever it
# holds, is never escaped in the output.
checksum() {
  if [ "$SHA" = sha256sum ]; then
    sha256sum <"$1" | awk '{ print $1 }'
  else
    shasum -a 256 <"$1" | awk '{ print $1 }'
  fi
}

# physical <dir>: the folder's path with repeated slashes collapsed and symlinks resolved, or
# nothing if it isn't a folder. A relative path is taken from the current folder, as PATH does.
physical() {
  case "$1" in
    /*) p=$1 ;;
    *) p=./$1 ;;
  esac
  (cd "$(printf '%s' "$p" | tr -s /)" 2>/dev/null && pwd -P) || true
}

# cleanup removes what's left of the install's own files, and doesn't stop if a removal fails.
cleanup() {
  if [ -n "$STAGE" ]; then rm -f "$STAGE" || true; fi
  if [ -n "$TMP" ]; then rm -rf "$TMP" || true; fi
  STAGE=
  TMP=
}

main() {
  set -eu
  TMP=
  STAGE=
  trap cleanup EXIT
  # Clean up here too: native zsh skips the EXIT trap after an exit from a signal trap.
  trap 'cleanup; exit 1' HUP INT TERM

  DOCS=https://github.com/tektonsoftware/sigdek#getting-the-sigdek-command
  SETDIR="Set SIGDEK_INSTALL_DIR to a folder you can write to, after the pipe: curl -fsSL https://sigdek.com/install.sh | SIGDEK_INSTALL_DIR=<folder> sh"
  BASE=${SIGDEK_RELEASE_BASE:-https://github.com/tektonsoftware/sigdek/releases}
  BASE=${BASE%/}
  case "$BASE" in
    https://*) PROTO='=https' HTTPS=yes ;;
    *) PROTO='=http,https' HTTPS= ;;
  esac

  GNU=
  HSTS=
  if command -v curl >/dev/null 2>&1; then
    GET=curl
  elif command -v wget >/dev/null 2>&1; then
    GET=wget
    # GNU Wget 1.x (wget2 says "GNU Wget2"), and whether it was built with HSTS, which it would
    # otherwise save in ~/.wget-hsts.
    case "$(wget --version 2>/dev/null)" in
      'GNU Wget '[0-9]*) GNU=yes ;;
    esac
    if [ -n "$GNU" ]; then
      case "$(wget --help 2>/dev/null)" in *--hsts-file*) HSTS=yes ;; esac
    fi
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

  VERSION=${SIGDEK_VERSION:-}
  VERSION=${VERSION#v}
  if [ -n "${SIGDEK_VERSION:-}" ]; then
    case "$VERSION" in
      [0-9]*[!0-9A-Za-z.+-]* | [!0-9]* | '') die "\"$VERSION\" isn't a SigDek version, like 0.1.0." ;;
    esac
  fi

  if [ -n "${SIGDEK_INSTALL_DIR:-}" ]; then
    DIR=$SIGDEK_INSTALL_DIR
    # A ~ that the shell didn't expand, as in SIGDEK_INSTALL_DIR="~/bin".
    # shellcheck disable=SC2088 # these patterns match a literal ~, on purpose
    case "$DIR" in
      '~' | '~/'*)
        [ -n "${HOME:-}" ] || die "HOME isn't set, so the ~ in SIGDEK_INSTALL_DIR can't be expanded. Give the folder's full path."
        DIR=$HOME${DIR#'~'}
        ;;
    esac
  elif [ -n "${HOME:-}" ]; then
    DIR=$HOME/.local/bin
  else
    die "HOME isn't set. $SETDIR"
  fi
  case "$DIR" in
    /*) ;;
    *) DIR=$(pwd)/$DIR ;;
  esac
  DIR=$(printf '%s' "$DIR" | tr -s /)
  DIR=${DIR%/}
  if [ -z "$DIR" ] || [ "$(physical "$DIR")" = / ]; then
    die "SIGDEK_INSTALL_DIR can't be the root folder, /. $SETDIR"
  fi
  TARGET=$DIR/sigdek
  [ ! -d "$TARGET" ] || die "$TARGET is a folder, so sigdek can't be installed there. $SETDIR"

  # A template, because macOS's `mktemp -d` alone ignores TMPDIR.
  T=${TMPDIR:-/tmp}
  TMP=$(mktemp -d "${T%/}/sigdek-install.XXXXXX") || die "couldn't make a temporary folder in $T."

  if [ -z "$VERSION" ]; then
    latest
    case "$VERSION" in
      [0-9]*[!0-9A-Za-z.+-]* | [!0-9]* | '') die "\"$VERSION\" isn't a SigDek version, like 0.1.0." ;;
    esac
  fi
  NAME=sigdek_${VERSION}_${OS}_${ARCH}
  FROM=$BASE/download/v$VERSION
  tell "Installing SigDek $VERSION ($OS/$ARCH) in $DIR."

  download "$FROM/checksums.txt" "$TMP/checksums.txt" ". Check that v$VERSION is a SigDek release: $BASE"
  WANT=$(awk -v n="$NAME" '$2 == n || $2 == "*" n { print $1; exit }' <"$TMP/checksums.txt")
  case "$WANT" in
    *[!0-9a-f]* | '') WANT= ;;
  esac
  [ "${#WANT}" -eq 64 ] || die "the checksums.txt of release v$VERSION has no SHA-256 for $NAME, so nothing was installed."

  CURRENT=
  if [ -f "$TARGET" ] && [ -x "$TARGET" ] && [ "$(checksum "$TARGET")" = "$WANT" ]; then
    CURRENT=yes
  else
    download "$FROM/$NAME" "$TMP/$NAME" ", so nothing was installed."
    GOT=$(checksum "$TMP/$NAME")
    [ "$GOT" = "$WANT" ] || die "the downloaded $NAME doesn't match its SHA-256 in checksums.txt (got $GOT, expected $WANT), so nothing was installed."
    tell "Checked $NAME against the release's checksums.txt."
    # Copy into a new file that mktemp makes in the install folder (it never reuses a name
    # that exists, so nothing planted there is followed), then rename it over any earlier copy
    # in one step, so a sigdek that's running or starting never sees half a file.
    mkdir -p "$DIR" || die "couldn't create $DIR. $SETDIR"
    [ ! -d "$TARGET" ] || die "$TARGET is a folder, so sigdek can't be installed there. $SETDIR"
    STAGE=$(mktemp "$DIR/.sigdek.XXXXXX") || die "couldn't write $TARGET. $SETDIR"
    if ! { cp "$TMP/$NAME" "$STAGE" && chmod 755 "$STAGE" && mv -f "$STAGE" "$TARGET"; }; then
      die "couldn't write $TARGET. $SETDIR"
    fi
    STAGE=
  fi

  OUT=$("$TARGET" version </dev/null 2>&1) || die "$TARGET is installed but didn't run: $OUT"
  if [ -n "$CURRENT" ]; then
    tell "sigdek at $TARGET is already $VERSION."
  else
    tell "Installed $TARGET: $OUT"
  fi

  # Whether the folder is on PATH, and which sigdek a new shell would run, comparing physical
  # paths, so a PATH entry spelled differently (a symlink, extra slashes) still counts.
  REAL=$(physical "$DIR")
  ONPATH=
  rest=${PATH:-}:
  while [ -n "$rest" ]; do
    entry=${rest%%:*}
    rest=${rest#*:}
    if [ -n "$REAL" ] && [ "$(physical "${entry:-.}")" = "$REAL" ]; then
      ONPATH=yes
      break
    fi
  done
  FOUND=$(command -v sigdek 2>/dev/null || true)
  FOUND=${FOUND%/sigdek}
  RUN=$TARGET
  if [ -n "$FOUND" ] && [ -n "$REAL" ] && [ "$(physical "$FOUND")" = "$REAL" ]; then
    RUN=sigdek
  elif [ -n "$ONPATH" ] && [ -n "$FOUND" ]; then
    tell ""
    tell "Another sigdek, in $FOUND, comes before $DIR on your PATH, so the command sigdek runs that one. Remove it, or run this one as $TARGET."
  else
    SHOWN=$DIR
    H=
    if [ -n "${HOME:-}" ]; then
      H=$(printf '%s' "$HOME" | tr -s /)
      H=${H%/}
    fi
    if [ -n "$H" ]; then
      case "$DIR" in "$H"/*) SHOWN="\$HOME/${DIR#"$H"/}" ;; esac
    fi
    LINE="export PATH=\"$SHOWN:\$PATH\""
    PROFILE=.profile
    case "${SHELL:-}" in
      */zsh) PROFILE=.zshrc ;;
      */bash)
        PROFILE=.bashrc
        if [ "$OS" = darwin ]; then
          # Terminal opens login shells, and a login bash reads only the first of these that
          # exists, so a new ~/.bash_profile would hide an existing ~/.profile.
          PROFILE=.bash_profile
          for F in .bash_profile .bash_login .profile; do
            if [ -n "$H" ] && [ -f "$H/$F" ]; then
              PROFILE=$F
              break
            fi
          done
        fi
        ;;
      */fish) PROFILE=.config/fish/config.fish LINE="fish_add_path \"$SHOWN\"" ;;
      */csh | */tcsh)
        # tcsh reads ~/.tcshrc instead of ~/.cshrc when there is one.
        PROFILE=.cshrc
        case "$SHELL" in */tcsh) if [ -n "$H" ] && [ -f "$H/.tcshrc" ]; then PROFILE=.tcshrc; fi ;; esac
        LINE="set path = ( \"$SHOWN\" \$path )"
        ;;
    esac
    tell ""
    tell "$DIR isn't on your PATH. To add it, put this line in ~/$PROFILE, then open a new terminal:"
    tell "  $LINE"
    tell "Until then, run sigdek by its full path, $TARGET."
  fi

  tell ""
  tell "Next, connect this machine to your team:"
  tell "  $RUN login"
}

# Everything above only defines functions, so a script cut short in download doesn't start the
# install. (Keep it that way, and name functions so that none of their first letters, like the
# "as" of "assert", is a command a cut could run.)
main "$@"
