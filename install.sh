#!/bin/sh
# Installs the SigDek recorder's `sigdek` command on macOS or Linux, without Node.js:
#
#   curl -fsSL https://sigdek.com/install.sh | sh
#   wget -qO- https://sigdek.com/install.sh | sh     (without curl; wget's certificate checking
#                                                      depends on how it was built, so prefer curl)
#
# It downloads the release binary for this machine from
# https://github.com/tektonsoftware/sigdek/releases, checks its SHA-256 against that release's
# checksums.txt, and puts it at ~/.local/bin/sigdek. It never uses sudo, writes only to that
# folder (creating it, and any missing parents, if needed) and to a temporary folder that it
# removes, and never edits your shell profile. (Where wget is wget2, as on recent Fedora, wget2
# may also update its own caches in your home folder.)
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
# Source: https://github.com/tektonsoftware/sigdek/blob/main/install.sh. Each release carries the
# copy at that release's tag, and sigdek.com/install.sh serves the latest release's:
# https://github.com/tektonsoftware/sigdek/releases/latest/download/install.sh

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
# downloads. It writes its full messages (not -nv's), so a failure's reason is in them. Other
# wgets (BusyBox, wget2) get plain flags. Whether a wget checks certificates depends on how it
# was built (some BusyBox builds don't).
fetch() {
  if [ "$GET" = curl ]; then
    curl -fsSL --proto "$PROTO" --connect-timeout 15 --speed-limit 1024 --speed-time 60 -o "$2" "$1"
  elif [ -n "$GNU" ]; then
    wget --progress=dot:giga --tries=1 -T 60 ${HTTPS:+--https-only} ${HSTS:+"--hsts-file=$TMP/wget-hsts"} -O "$2" "$1"
  else
    wget -q -T 60 -O "$2" "$1"
  fi
}

# follow <url>: one try at following url's redirects, which sets URL to the address it ends at.
# An HTTP error from url itself (4xx or 5xx) is a failed try, with its status in STATUS; one
# from the page it redirects to isn't, since latest only looks at where the redirect went.
follow() {
  STATUS=
  URL=
  if [ "$GET" = curl ]; then
    curl -sSIL --proto "$PROTO" --connect-timeout 15 --max-time 60 -o /dev/null \
      -w '%{http_code} %{num_redirects} %{url_effective}\n' "$1" >"$TMP/latest" || return
    # The last response's status, which is url's own only when nothing redirected.
    first=
    hops=
    read -r first hops URL <"$TMP/latest" || true
    [ "$hops" = 0 ] || first=
  else
    # wget prints each response's headers (-S); exit status 8 is an HTTP error from any of them.
    if [ -n "$GNU" ]; then
      wget --progress=dot:giga -S --tries=1 -T 60 ${HTTPS:+--https-only} ${HSTS:+"--hsts-file=$TMP/wget-hsts"} -O /dev/null "$1" ||
        { rc=$?; [ "$rc" -eq 8 ] || return "$rc"; }
    else
      wget -S -T 60 -O /dev/null "$1" || { rc=$?; [ "$rc" -eq 8 ] || return "$rc"; }
    fi
    first=$(tr -d '\r' <"$TMP/err" | sed -n 's/^ *HTTP\/[0-9.]* \([0-9][0-9][0-9]\).*/\1/p' | head -n 1)
    URL=$(tr -d '\r' <"$TMP/err" | sed -n 's/^ *[Ll][Oo][Cc][Aa][Tt][Ii][Oo][Nn]: *\([^ ]*\).*/\1/p' | tail -n 1)
  fi
  case "$first" in
    [45][0-9][0-9])
      STATUS=$first
      tell "HTTP status $STATUS" >&2
      return 1
      ;;
  esac
}

# persist <command> <url> [<argument>...]: runs the command up to 3 times, whatever the failure,
# 2 seconds apart, with its messages in $TMP/err, except that it stops at once, saying so, when
# the server's certificate couldn't be verified. On failure it returns the last try's exit
# status, with that try's reason in $WHY.
persist() {
  n=1
  while :; do
    "$@" 2>"$TMP/err" && return 0
    code=$?
    # The reason: for curl, the first line it wrote (curl: (<code>) ...). For wget, the last line
    # with ERROR, failed or unable in it, else its last line, counting only what it wrote after
    # its last successful connection (a failed try at another address, such as IPv6, comes
    # before it), never its advice to turn certificate checking off, and not a closing "Giving
    # up." on its own line (the cause is on the line before). Then wget's timestamp, speed and
    # "awaiting response..." go, and so do control characters (it can quote the server).
    WHY=$(awk -v get="$GET" '
      get == "curl" { if (NF && l == "") l = $0; next }
      / connected\.$/ { e = ""; l = ""; next }
      /no-check-certificate|insecurely|^(Giving up|Retrying)\.$/ { next }
      /ERROR|failed|unable/ { e = $0 }
      NF { l = $0 }
      END { print (e != "" ? e : l) }' <"$TMP/err" | tr -d '[:cntrl:]' |
      sed -e 's/^[0-9][0-9-]* [0-9][0-9:]* //' -e 's/^([^)]*) - //' -e 's/^HTTP request sent, awaiting response\.\.\. //')
    [ -n "$WHY" ] || WHY="$GET: exit $code"
    # curl's and wget's exit status when the certificate couldn't be verified. Trying again won't
    # help, and checking must stay on.
    case "$GET:$code" in
      curl:60 | curl:35) die "the certificate of the server for $2 couldn't be verified ($WHY), so nothing was installed. Certificate checking must stay on: fix the system's certificates, then run this again." ;;
      wget:5) die "the certificate of the server for $2 couldn't be verified ($WHY), so nothing was installed. Certificate checking must stay on: use curl, or fix the system's certificates, then run this again." ;;
    esac
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
  if ! persist follow "$BASE/latest"; then
    [ -z "$STATUS" ] || die "couldn't find the latest version: $BASE/latest answered with HTTP status $STATUS, 3 times. Run this again in a few minutes."
    die "couldn't reach $BASE/latest to find the latest version ($WHY). Check the network connection and run this again."
  fi
  case "$URL" in
    */tag/v*) VERSION=${URL##*/tag/v} ;;
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

# canonical <absolute path>: the path as pwd -P will give it once the folder exists: its longest
# part that is a folder now, with symlinks resolved, then the rest with . and .. resolved, as
# mkdir -p makes it.
canonical() {
  known=$1
  more=
  until [ -d "$known" ] && real=$(cd "$known" 2>/dev/null && pwd -P); do
    case "$known" in
      */*)
        more=${known##*/}/$more
        known=${known%/*}
        ;;
      *)
        more=$known/$more
        known=
        ;;
    esac
    [ -n "$known" ] || known=/
  done
  while [ -n "$more" ]; do
    part=${more%%/*}
    more=${more#*/}
    case "$part" in
      '' | .) ;;
      ..) real=${real%/*} ;;
      *) real=${real%/}/$part ;;
    esac
    [ -n "$real" ] || real=/
  done
  printf '%s\n' "$real"
}

# verbatim <text>: text in single quotes, as one shell word, with each ' in it written as '\''.
verbatim() {
  printf "'%s'" "$(printf '%s\n' "$1" | sed "s/'/'\\\\''/g")"
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
    # Any other ~, as in ~user/bin, would make a folder named ~user.
    case "$DIR" in
      '~'*) die "SIGDEK_INSTALL_DIR starts with a ~ that this script can't expand (only ~ and ~/ are). Write the full path, or use \$HOME." ;;
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
  # The folder as it will be: symlinks, ., .. and repeated slashes resolved, so that what this
  # prints is where sigdek is.
  DIR=$(canonical "$(printf '%s' "$DIR" | tr -s /)")
  [ "$DIR" != / ] || die "SIGDEK_INSTALL_DIR can't be the root folder, /. $SETDIR"
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
    tell "Installing SigDek $VERSION ($OS/$ARCH) in $DIR."
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
    # HOME as DIR spells it, so that a folder under it can be shown as $HOME/<rest>.
    H=
    case "${HOME:-}" in
      /*) H=$(canonical "$(printf '%s' "$HOME" | tr -s /)") ;;
    esac
    LEAD=
    REST=$DIR
    if [ -n "$H" ]; then
      # shellcheck disable=SC2016 # a literal $HOME, for the profile line
      case "$DIR" in
        "$H") LEAD='$HOME' REST= ;;
        "$H"/*) LEAD='$HOME' REST=/${DIR#"$H"/} ;;
      esac
    fi
    # The folder as the profile line writes it: in double quotes when it holds only letters,
    # digits, spaces and _./@%+=:,- (as nearly every folder does), and otherwise in single
    # quotes, so that no shell reads anything in its name as code. Inside single quotes, fish
    # also takes \ as an escape, and csh takes !, so their lines escape those too.
    case "$REST" in
      *[![:alnum:]\ _./@%+=:,-]*)
        WORD=$(verbatim "$REST")
        FISHWORD=$(verbatim "$(printf '%s\n' "$REST" | sed 's/\\/\\\\/g')")
        CSHWORD=$(verbatim "$(printf '%s\n' "$REST" | sed 's/!/\\!/g')")
        if [ -n "$LEAD" ]; then
          WORD="\"$LEAD\"$WORD"
          FISHWORD="\"$LEAD\"$FISHWORD"
          CSHWORD="\"$LEAD\"$CSHWORD"
        fi
        LINE="export PATH=$WORD:\"\$PATH\""
        ;;
      *)
        WORD="\"$LEAD$REST\""
        FISHWORD=$WORD
        CSHWORD=$WORD
        LINE="export PATH=\"$LEAD$REST:\$PATH\""
        ;;
    esac
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
      */fish) PROFILE=.config/fish/config.fish LINE="fish_add_path $FISHWORD" ;;
      */csh | */tcsh)
        # tcsh reads ~/.tcshrc instead of ~/.cshrc when there is one.
        PROFILE=.cshrc
        case "$SHELL" in */tcsh) if [ -n "$H" ] && [ -f "$H/.tcshrc" ]; then PROFILE=.tcshrc; fi ;; esac
        LINE="set path = ( $CSHWORD \$path )"
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
