# REHEARSAL ONLY (branch release-rehearsal, never merged).
# Installs SigDek the way a Windows user would from the rehearsal build: npm (through a local
# Verdaccio), Scoop, winget, launch.ps1, then the sh launchers under Git Bash
# (rehearsal/test-unix.sh). Nothing is published, nothing signs in, records stay in temp folders.
#   $env:ART   the rehearsal-build artifact; $env:WORK  an empty scratch folder
$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'
$ART = (Resolve-Path $env:ART).Path
$WORK = $env:WORK
$V1 = '0.0.99'; $V2 = '0.0.100'
$BASE = 'http://127.0.0.1:8765'; $BAD = 'http://127.0.0.1:8766'; $REG = 'http://127.0.0.1:4873/'
$SLOW = 'http://127.0.0.1:8767'; $SLOW_BAD = 'http://127.0.0.1:8768'; $SLOW_RATE = 250000 # bytes a second (2 Mbit/s)
New-Item -ItemType Directory -Force $WORK, "$WORK\out", "$WORK\homes" | Out-Null
$WORK = (Resolve-Path $WORK).Path
$OUT = "$WORK\out"; $RESULTS = "$OUT\results.txt"; $LOG = "$OUT\log.txt"
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }

function Log([string]$m) { Add-Content -LiteralPath $LOG -Value $m; Write-Output $m }
function Res([string]$kind, [string]$what, [string]$ev) {
  $line = "$kind | windows | $what | $ev"
  Add-Content -LiteralPath $RESULTS -Value $line; Add-Content -LiteralPath $LOG -Value $line; Write-Output $line
}
function Pass($w, $e) { Res 'PASS' $w $e }
function Fail($w, $e) { Res 'FAIL' $w $e }
function Note($w, $e) { Res 'NOTE' $w $e }
function Fwd([string]$p) { $p -replace '\\', '/' }
function FreshHome([string]$n) { $h = "$WORK\homes\$n"; Remove-Item -Recurse -Force $h -ErrorAction SilentlyContinue; New-Item -ItemType Directory -Force $h | Out-Null; $h }
function Records([string]$h) { $n = 0; Get-ChildItem "$h\log\events-*.jsonl" -ErrorAction SilentlyContinue | ForEach-Object { $n += @(Get-Content -LiteralPath $_.FullName).Count }; $n }
function CcStart([string]$cwd) { '{"session_id":"rehearsal-1","transcript_path":"/tmp/rehearsal.jsonl","cwd":"' + (Fwd $cwd) + '","hook_event_name":"SessionStart","source":"startup","model":"claude-sonnet-4-5"}' }
function CurEv([string]$ev, [string]$cwd) { $c = Fwd $cwd; '{"conversation_id":"conv-r1","generation_id":"gen-r1","model":"gpt-5","hook_event_name":"' + $ev + '","cursor_version":"2.4.0","workspace_roots":["' + $c + '"],"user_email":"dev@example.com","transcript_path":null,"tool_name":"Shell","tool_input":{"command":"npm test"},"cwd":"' + $c + '"}' }
function Join([object]$o) { (@($o) | ForEach-Object { "$_" }) -join ' ' }

# Isolation: never a real service, never the real ~/.sigdek, never a binary set by hand.
$env:SIGDEK_INGEST_URL = 'http://127.0.0.1:9'; $env:SIGDEK_APP_URL = 'http://127.0.0.1:9'
$env:SIGDEK_HOME = "$WORK\home-default"
foreach ($v in 'SIGDEK_BIN', 'SIGDEK_CACHE', 'CLAUDE_PLUGIN_DATA') { Remove-Item "Env:$v" -ErrorAction SilentlyContinue }

# check_bin: `sigdek version` and one Claude Code hook through a given command.
function CheckBin([string]$ch, [string]$bin, [string]$v = $V1) {
  $out = Join (& $bin version 2>&1); $st = $LASTEXITCODE
  Log "  [$ch] $bin version -> exit=$st : $out"
  if ($st -eq 0 -and $out -eq "sigdek $v (windows/$arch)") { Pass "${ch}: sigdek version" $out } else { Fail "${ch}: sigdek version" "exit=$st : $out" }
  $h = FreshHome "$($ch -replace '[^A-Za-z0-9]','-')-hook"
  $env:SIGDEK_HOME = $h
  $out = Join ((CcStart $WORK) | & $bin hook claude-code 2>&1); $st = $LASTEXITCODE
  $env:SIGDEK_HOME = "$WORK\home-default"
  $n = Records $h
  Log "  [$ch] hook claude-code SessionStart -> exit=$st records=$n out=$out"
  if ($st -eq 0 -and $n -eq 1 -and -not $out) { Pass "${ch}: hook claude-code (SessionStart)" "exit 0, 1 record" } else { Fail "${ch}: hook claude-code (SessionStart)" "exit=$st records=$n out=$out" }
}

Log "== Release rehearsal on windows/$arch, $((Get-Date).ToUniversalTime().ToString('s'))Z"
Log "PowerShell $($PSVersionTable.PSVersion); Windows PowerShell $(powershell -NoProfile -Command '$PSVersionTable.PSVersion.ToString()'); $(git --version)"

# --- The release server, and a tampered copy ---------------------------------------------------
foreach ($v in $V1, $V2) {
  foreach ($d in "$WORK\srv\v$v", "$WORK\srv-bad\v$v") {
    New-Item -ItemType Directory -Force $d | Out-Null
    Copy-Item "$ART\dist\$v\sigdek_*", "$ART\dist\$v\checksums.txt" $d
  }
  [IO.File]::AppendAllText("$WORK\srv-bad\v$v\sigdek_${v}_windows_$arch.exe", 'x')
}
$py = (Get-Command python).Source
$servers = @()
$servers += Start-Process -FilePath $py -ArgumentList '-m', 'http.server', '8765', '--bind', '127.0.0.1', '--directory', "$WORK\srv" -RedirectStandardError "$OUT\http.log" -RedirectStandardOutput "$OUT\http-out.log" -PassThru -NoNewWindow
$servers += Start-Process -FilePath $py -ArgumentList '-m', 'http.server', '8766', '--bind', '127.0.0.1', '--directory', "$WORK\srv-bad" -RedirectStandardError "$OUT\http-bad.log" -RedirectStandardOutput "$OUT\http-bad-out.log" -PassThru -NoNewWindow
# The same two at $SLOW_RATE: a link too slow for the hook's 8-second download.
$servers += Start-Process -FilePath $py -ArgumentList 'rehearsal/throttle.py', '8767', "$WORK\srv", "$SLOW_RATE", "$OUT\http-slow.log" -RedirectStandardError "$OUT\http-slow-err.log" -RedirectStandardOutput "$OUT\http-slow-out.log" -PassThru -NoNewWindow
$servers += Start-Process -FilePath $py -ArgumentList 'rehearsal/throttle.py', '8768', "$WORK\srv-bad", "$SLOW_RATE", "$OUT\http-slow-bad.log" -RedirectStandardError "$OUT\http-slow-bad-err.log" -RedirectStandardOutput "$OUT\http-slow-bad-out.log" -PassThru -NoNewWindow
for ($i = 0; $i -lt 50; $i++) { try { Invoke-WebRequest -UseBasicParsing "$BASE/v$V1/checksums.txt" | Out-Null; break } catch { Start-Sleep -Milliseconds 200 } }

# --- The tap/bucket repository: main after the pin commit, base rewritten ------------------------
git clone -q --bare "$ART\tap.bundle" "$WORK\sigdek.git"
git -C "$WORK\sigdek.git" update-ref refs/heads/main "refs/tags/local-$V1"
Log "bucket repository $WORK\sigdek.git at $(git -C "$WORK\sigdek.git" log --oneline -1 main)"

# --- npm through Verdaccio ---------------------------------------------------------------------
Log "== npm, through a local Verdaccio with all seven packages (PowerShell)"
New-Item -ItemType Directory -Force "$WORK\verdaccio\storage" | Out-Null
@"
storage: $(Fwd "$WORK\verdaccio\storage")
auth:
  htpasswd:
    file: $(Fwd "$WORK\verdaccio\htpasswd")
    max_users: 10
uplinks:
  npmjs:
    url: https://registry.npmjs.org/
packages:
  'sigdek':
    access: `$all
    publish: `$authenticated
  '@tektonsoftware/*':
    access: `$all
    publish: `$authenticated
  '**':
    access: `$all
    proxy: npmjs
log: { type: stdout, format: pretty, level: warn }
"@ | Set-Content -LiteralPath "$WORK\verdaccio\config.yaml" -Encoding utf8
$env:npm_config_registry = 'https://registry.npmjs.org/'; $env:npm_config_cache = "$WORK\tool-cache"
$servers += Start-Process -FilePath (Get-Command npx.cmd).Source -ArgumentList '--yes', 'verdaccio@6.11.1', '--config', "$WORK\verdaccio\config.yaml", '--listen', '127.0.0.1:4873' -WorkingDirectory "$WORK\verdaccio" -RedirectStandardOutput "$OUT\verdaccio.log" -RedirectStandardError "$OUT\verdaccio-err.log" -PassThru -NoNewWindow
for ($i = 0; $i -lt 150; $i++) { try { Invoke-WebRequest -UseBasicParsing "${REG}-/ping" | Out-Null; break } catch { Start-Sleep -Seconds 1 } }
$tok = (Invoke-RestMethod -Method Put -Uri "${REG}-/user/org.couchdb.user:rehearsal" -ContentType 'application/json' -Body '{"name":"rehearsal","password":"rehearsal-pass-1"}').token
if (-not $tok) { Fail 'npm: setup' "Verdaccio gave no token" }
"registry=$REG`n//127.0.0.1:4873/:_authToken=$tok`n" | Set-Content -LiteralPath "$WORK\npmrc" -Encoding ascii
$env:NPM_CONFIG_USERCONFIG = "$WORK\npmrc"; $env:npm_config_cache = "$WORK\npm-cache"; $env:npm_config_prefix = "$WORK\npm-global"
$env:npm_config_registry = $REG; $env:npm_config_update_notifier = 'false'; $env:npm_config_fund = 'false'; $env:npm_config_audit = 'false'
Log "node $(node --version), npm $(npm --version)"

function Publish([string]$v) {
  $ok = $true
  $files = @(Get-ChildItem "$ART\npm-tgz\$v\tektonsoftware-sigdek-*.tgz") + @(Get-Item "$ART\npm-tgz\$v\sigdek-$v.tgz")
  foreach ($f in $files) {
    $o = Join (npm publish $f.FullName --access public 2>&1)
    if ($LASTEXITCODE -eq 0) { Log "  published $($f.Name)" } else { $ok = $false; Log "  publish FAILED $($f.Name): $o" }
  }
  if ($ok) { Pass "npm: publish $v (7 packages) to Verdaccio" (($files | ForEach-Object Name) -join ' ') } else { Fail "npm: publish $v to Verdaccio" 'see log' }
}
Publish $V1

New-Item -ItemType Directory -Force "$WORK\empty" | Out-Null
Push-Location "$WORK\empty"
$sw = [Diagnostics.Stopwatch]::StartNew()
$out = Join (npx --yes sigdek version 2>&1); $st = $LASTEXITCODE
Log "  npx --yes sigdek version -> exit=$st ($($sw.ElapsedMilliseconds) ms): $out"
if ($st -eq 0 -and $out -eq "sigdek $V1 (windows/$arch)") { Pass 'npm: npx sigdek version' $out } else { Fail 'npm: npx sigdek version' "exit=$st : $out" }
Pop-Location

function InitRepo([string]$dir, [string[]]$initArgs) {
  Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force $dir | Out-Null
  Push-Location $dir
  git init -q; git config user.email dev@example.com; git config user.name 'Dev Example'
  $out = npx --yes sigdek init @initArgs 2>&1; $st = $LASTEXITCODE
  Log "  npx --yes sigdek init $($initArgs -join ' ') -> exit=$st"
  $out | ForEach-Object { Add-Content -LiteralPath $LOG -Value "    $_" }
  $lock = Get-Content -LiteralPath "$dir\.sigdek\recorder.lock" -ErrorAction SilentlyContinue
  $lock | ForEach-Object { Add-Content -LiteralPath $LOG -Value "    lock: $_" }
  $sums = @{}; Get-Content "$ART\dist\$V1\checksums.txt" | ForEach-Object { $p = $_ -split '\s+'; $sums[$p[1]] = $p[0] }
  $ok = ($st -eq 0) -and ($lock -contains "version=$V1") -and ($lock -contains "url=$BASE/v$V1")
  foreach ($t in 'darwin_amd64', 'darwin_arm64', 'linux_amd64', 'linux_arm64', 'windows_amd64', 'windows_arm64') {
    $e = if ($t -like 'windows_*') { '.exe' } else { '' }
    if (-not ($lock -contains "$t=$($sums["sigdek_${V1}_$t$e"])")) { $ok = $false; Log "  lock is missing $t" }
  }
  git add -A; git commit -qm 'Record agent activity with SigDek'
  Pop-Location
  return $ok
}
$REPO = "$WORK\repo"
if (InitRepo $REPO @()) { Pass 'npm: npx sigdek init' "exit 0; recorder.lock pins $V1, url=$BASE/v$V1 and all six checksums" } else { Fail 'npm: npx sigdek init' 'see log' }
$REPOPS = "$WORK\repo-ps"
if (InitRepo $REPOPS @('--powershell')) { Pass 'npm: npx sigdek init --powershell' 'exit 0; lock as above' } else { Fail 'npm: npx sigdek init --powershell' 'see log' }

$out = Join (npm install -g sigdek 2>&1); $st = $LASTEXITCODE
Log "  npm install -g sigdek -> exit=$st : $out"
if ($st -eq 0) { CheckBin 'npm -g' "$WORK\npm-global\sigdek.cmd" } else { Fail 'npm: npm install -g sigdek' "exit=$st" }
Log "  Get-Command sigdek resolves to: $((Get-Command sigdek -ErrorAction SilentlyContinue).Source)"

# --- launch.ps1, the --powershell Cursor hooks ---------------------------------------------------
Log "== Repository install with --powershell: Cursor runs .sigdek/launch.ps1 through Windows PowerShell"
Push-Location $REPOPS
$cmd = (Get-Content .cursor\hooks.json -Raw | ConvertFrom-Json).hooks.preToolUse[0].command
Log "  hooks.json preToolUse command: $cmd"
$h = FreshHome 'ps1-cursor'; $env:SIGDEK_HOME = $h
$sw = [Diagnostics.Stopwatch]::StartNew()
$out = Join ((CurEv 'preToolUse' $REPOPS) | powershell -NoProfile -ExecutionPolicy Bypass -File .sigdek/launch.ps1 hook cursor preToolUse 2>&1); $st = $LASTEXITCODE
Log "  launch.ps1 preToolUse first use -> exit=$st ($($sw.ElapsedMilliseconds) ms) out=$out"
if ($st -eq 0 -and $out -eq '{"permission":"allow"}' -and (Test-Path "$h\bin\$V1\sigdek.exe")) { Pass 'repo: launch.ps1 (Cursor --powershell) first use' "exit 0, answer $out, downloaded and checked; $($sw.ElapsedMilliseconds) ms" } else { Fail 'repo: launch.ps1 (Cursor --powershell) first use' "exit=$st out=$out" }
$sw = [Diagnostics.Stopwatch]::StartNew()
$out = Join ((CurEv 'sessionStart' $REPOPS) | powershell -NoProfile -ExecutionPolicy Bypass -File .sigdek/launch.ps1 hook cursor sessionStart 2>&1); $st = $LASTEXITCODE
$n = Records $h
Log "  launch.ps1 sessionStart cached -> exit=$st ($($sw.ElapsedMilliseconds) ms) out=$out records=$n"
if ($st -eq 0 -and $out -eq '{}' -and $n -eq 1) { Pass 'repo: launch.ps1 (Cursor --powershell) sessionStart' "exit 0, answer {}, 1 record; $($sw.ElapsedMilliseconds) ms" } else { Fail 'repo: launch.ps1 sessionStart' "exit=$st out=$out records=$n" }
# Tampered: the same launcher with its lock pointing at the tampered server.
$t = "$WORK\tampered-ps\.sigdek"; New-Item -ItemType Directory -Force $t | Out-Null
Copy-Item .sigdek\* $t
(Get-Content .sigdek\recorder.lock) -replace "^url=$([regex]::Escape($BASE))", "url=$BAD" | Set-Content "$t\recorder.lock"
$h = FreshHome 'ps1-tamper'; $env:SIGDEK_HOME = $h
$out = Join ((CurEv 'preToolUse' $REPOPS) | powershell -NoProfile -ExecutionPolicy Bypass -File "$t\launch.ps1" hook cursor preToolUse 2>&1); $st = $LASTEXITCODE
$n = Records $h
if ($st -eq 0 -and $out -eq '{"permission":"allow"}' -and -not (Test-Path "$h\bin\$V1\sigdek.exe") -and $n -eq 0) { Pass 'repo: launch.ps1 refuses a tampered binary' "exit 0, safe answer $out, nothing cached, 0 records; stamp: $(Get-Content "$h\bin\$V1\.download-failed" -ErrorAction SilentlyContinue)" } else { Fail 'repo: launch.ps1 refuses a tampered binary' "exit=$st out=$out records=$n cached=$(Test-Path "$h\bin\$V1\sigdek.exe")" }
$env:SIGDEK_HOME = "$WORK\home-default"
Pop-Location

# --- launch.ps1 on a slow link -------------------------------------------------------------------
$name = "sigdek_${V1}_windows_$arch.exe"
$size = (Get-Item "$WORK\srv\v$V1\$name").Length
Log "== launch.ps1 on a slow link: $name ($size bytes) at $SLOW_RATE bytes a second, about $([int]($size / $SLOW_RATE)) s; the hook allows 8 s"
function SlowCopy([string]$to, [string]$url) {
  New-Item -ItemType Directory -Force $to | Out-Null
  Copy-Item "$REPOPS\.sigdek\*" $to
  (Get-Content "$REPOPS\.sigdek\recorder.lock") -replace "^url=$([regex]::Escape($BASE))", "url=$url" | Set-Content "$to\recorder.lock"
}
# Counted with Git Bash's grep: PowerShell read 0 lines from these logs while the server held them open.
function Gets([string]$file) {
  $gc = @(Get-Content -LiteralPath "$OUT\$file" -ErrorAction SilentlyContinue | Where-Object { $_ -like "*GET /v$V1/$name HTTP*" }).Count
  $gr = [int](& 'C:\Program Files\Git\usr\bin\grep.exe' -c "GET /v$V1/$name HTTP" (Fwd "$OUT\$file"))
  Add-Content -LiteralPath $LOG -Value "    requests in ${file}: grep $gr, Get-Content $gc"
  $gr
}
function Hook([string]$launcher, [string]$ev) {
  $sw = [Diagnostics.Stopwatch]::StartNew()
  $o = Join ((CurEv $ev $REPOPS) | powershell -NoProfile -ExecutionPolicy Bypass -File $launcher hook cursor $ev 2>&1)
  @{ out = $o; st = $LASTEXITCODE; ms = $sw.ElapsedMilliseconds }
}
SlowCopy "$WORK\slow-ps\.sigdek" $SLOW
SlowCopy "$WORK\slow-ps-bad\.sigdek" $SLOW_BAD
if ((Get-Content "$WORK\slow-ps\.sigdek\launch.ps1" -Raw) -match "'download'") { Log '  the launch.ps1 sigdek init wrote has the background download' } else { Fail 'slow: setup' 'launch.ps1 has no background download' }
for ($i = 0; $i -lt 50; $i++) { try { Invoke-WebRequest -UseBasicParsing "$SLOW/v$V1/checksums.txt" | Out-Null; break } catch { Start-Sleep -Milliseconds 200 } }
$h = FreshHome 'ps1-slow'; $env:SIGDEK_HOME = $h; $c = "$h\bin\$V1"
$r = Hook "$WORK\slow-ps\.sigdek\launch.ps1" 'preToolUse'; $landFrom = [Diagnostics.Stopwatch]::StartNew()
Log "  slow launch.ps1 preToolUse first use -> exit=$($r.st) ($($r.ms) ms) out=$($r.out) cache=$(Join (Get-ChildItem -Force -Name $c -ErrorAction SilentlyContinue))"
if ($r.st -eq 0 -and $r.out -eq '{"permission":"allow"}' -and $r.ms -lt 10000 -and -not (Test-Path "$c\sigdek.exe") -and (Test-Path "$c\.downloading") -and (Records $h) -eq 0) { Pass 'slow: launch.ps1 first use' "exit 0 in $($r.ms) ms, answer $($r.out), 0 records; the hidden copy carries on" } else { Fail 'slow: launch.ps1 first use' "exit=$($r.st) ms=$($r.ms) out=$($r.out)" }
$r = Hook "$WORK\slow-ps\.sigdek\launch.ps1" 'sessionStart'
Log "  slow launch.ps1 sessionStart during the download -> exit=$($r.st) ($($r.ms) ms) out=$($r.out)"
if ($r.st -eq 0 -and $r.out -eq '{}' -and $r.ms -lt 3000 -and (Records $h) -eq 0 -and -not (Test-Path "$c\sigdek.exe")) { Pass 'slow: launch.ps1, an event during the download' "exit 0 in $($r.ms) ms, answer {}, 0 records" } else { Fail 'slow: launch.ps1, an event during the download' "exit=$($r.st) ms=$($r.ms) out=$($r.out)" }
$hb = FreshHome 'ps1-slow-bad'; $env:SIGDEK_HOME = $hb; $cb = "$hb\bin\$V1"
$rb = Hook "$WORK\slow-ps-bad\.sigdek\launch.ps1" 'preToolUse'
Log "  slow tampered launch.ps1 preToolUse -> exit=$($rb.st) ($($rb.ms) ms) out=$($rb.out)"
$land = $null
for ($i = 0; $i -lt 600; $i++) {
  if (-not $land -and (Test-Path "$c\sigdek.exe") -and -not (Test-Path "$c\.downloading")) { $land = $landFrom.ElapsedMilliseconds }
  if ($land -and -not (Test-Path "$cb\.downloading")) { break }
  Start-Sleep -Milliseconds 500
}
Get-ChildItem -Force $c, $cb -ErrorAction SilentlyContinue | ForEach-Object { Add-Content -LiteralPath $LOG -Value "    $($_.FullName) $($_.Length)" }
$n = Gets 'http-slow.log'
if ($land -and -not (Test-Path "$c\.download-failed") -and $n -eq 1) { Pass 'slow: launch.ps1 background download lands' "$land ms after the first hook returned; $n request (the hidden copy's)" } else { Fail 'slow: launch.ps1 background download lands' "land=$land requests=$n" }
$env:SIGDEK_HOME = $h
$r = Hook "$WORK\slow-ps\.sigdek\launch.ps1" 'sessionStart'
if ($r.st -eq 0 -and $r.out -eq '{}' -and (Records $h) -eq 1) { Pass 'slow: launch.ps1, the next event records' "exit 0, answer {}, 1 record; $($r.ms) ms" } else { Fail 'slow: launch.ps1, the next event records' "exit=$($r.st) out=$($r.out) records=$(Records $h)" }
$want = ((Get-Content "$ART\dist\$V1\checksums.txt" | Where-Object { $_ -match " $([regex]::Escape($name))$" }) -split '\s+')[0]
$stamp = (Get-Content -LiteralPath "$cb\.download-failed" -Raw -ErrorAction SilentlyContinue)
$nb = Gets 'http-slow-bad.log'
$temps = @(Get-ChildItem "$cb\*.download" -ErrorAction SilentlyContinue).Count
if ($rb.st -eq 0 -and $rb.out -eq '{"permission":"allow"}' -and $rb.ms -lt 10000 -and -not (Test-Path "$cb\sigdek.exe") -and "$stamp".Trim() -eq "1 $want" -and $temps -eq 0 -and -not (Test-Path "$cb\.downloading") -and (Records $hb) -eq 0) {
  Pass 'slow: launch.ps1 refuses a tampered background download' "hook exit 0 in $($rb.ms) ms, answer $($rb.out); $nb request; nothing cached, no temporary file, 0 records; .download-failed: 1 $($want.Substring(0, 12))..."
} else { Fail 'slow: launch.ps1 refuses a tampered background download' "exit=$($rb.st) ms=$($rb.ms) out=$($rb.out) stamp=$stamp temps=$temps requests=$nb" }
$env:SIGDEK_HOME = $hb
$r = Hook "$WORK\slow-ps-bad\.sigdek\launch.ps1" 'preToolUse'
if ($r.st -eq 0 -and $r.out -eq '{"permission":"allow"}' -and (Gets 'http-slow-bad.log') -eq $nb) { Pass 'slow: launch.ps1 backs off after the refused download' "exit 0 in $($r.ms) ms, no request" } else { Fail 'slow: launch.ps1 back-off after the refused download' "exit=$($r.st) out=$($r.out) requests=$(Gets 'http-slow-bad.log')" }
$env:SIGDEK_HOME = "$WORK\home-default"

# --- Scoop -------------------------------------------------------------------------------------
Log "== Scoop"
try {
  Invoke-RestMethod get.scoop.sh -OutFile "$WORK\install-scoop.ps1"
  & "$WORK\install-scoop.ps1" -RunAsAdmin -ScoopDir "$WORK\scoop" *>> $LOG
  $env:SCOOP = "$WORK\scoop"; $env:PATH = "$WORK\scoop\shims;$env:PATH"
  Log "  scoop: $(Join (scoop --version 2>&1))"
  # Scoop takes a URL, not a Windows path, for a bucket; file:/// stands in for https://github.com/...
  $bucketUrl = 'file:///' + (Fwd "$WORK\sigdek.git")
  $out = Join (scoop bucket add sigdek $bucketUrl *>&1)
  Log "  scoop bucket add sigdek $bucketUrl -> $out"
  $list = scoop bucket list *>&1 | Out-String
  Log "  scoop bucket list: $list"
  if ($list -match 'sigdek') { Pass 'scoop: bucket add from a repository with bucket/' $out } else { Fail 'scoop: bucket add' $out }
  $out = scoop install sigdek *>&1; $st = $LASTEXITCODE
  $out | ForEach-Object { Add-Content -LiteralPath $LOG -Value "    $_" }
  if (Test-Path "$WORK\scoop\shims\sigdek.exe") {
    Pass 'scoop: scoop install sigdek' (Join ($out | Select-Object -Last 2))
    CheckBin 'scoop' "$WORK\scoop\shims\sigdek.exe"
  } else { Fail 'scoop: scoop install sigdek' "exit=$st : $(Join ($out | Select-Object -Last 4))" }
  # Upgrade: the release workflow's next pin commit lands on main.
  git -C "$WORK\sigdek.git" update-ref refs/heads/main "refs/tags/local-$V2"
  $o = scoop update *>&1; $o | ForEach-Object { Add-Content -LiteralPath $LOG -Value "    scoop update: $_" }
  $o = scoop status *>&1; Log "  scoop status: $(Join $o)"
  $o = scoop update sigdek *>&1; $o | ForEach-Object { Add-Content -LiteralPath $LOG -Value "    scoop update sigdek: $_" }
  $out = Join (& "$WORK\scoop\shims\sigdek.exe" version 2>&1)
  if ($out -eq "sigdek $V2 (windows/$arch)") { Pass 'upgrade: scoop update; scoop update sigdek' $out } else { Fail 'upgrade: scoop update sigdek' $out }
} catch { Fail 'scoop' "$_" }

# --- winget ------------------------------------------------------------------------------------
Log "== winget"
$m1 = "$ART\dist\$V1\winget\manifests\t\TektonSoftware\SigDek\$V1"
$m2 = "$ART\dist\$V2\winget\manifests\t\TektonSoftware\SigDek\$V2"
Get-ChildItem $m1 | ForEach-Object { Add-Content -LiteralPath $LOG -Value "  --- $($_.Name)"; Get-Content $_.FullName | ForEach-Object { Add-Content -LiteralPath $LOG -Value "    $_" } }
$wg = Get-Command winget -ErrorAction SilentlyContinue
if (-not $wg) {
  Log "  winget isn't on PATH on this image; installing it with Microsoft.WinGet.Client's Repair-WinGetPackageManager"
  try {
    Install-PackageProvider -Name NuGet -Force -Scope CurrentUser *>> $LOG
    Install-Module Microsoft.WinGet.Client -Force -Scope CurrentUser -Repository PSGallery *>> $LOG
    Import-Module Microsoft.WinGet.Client
    Repair-WinGetPackageManager -AllUsers -Latest *>> $LOG
  } catch { Log "  installing winget failed: $_" }
  $wg = Get-Command winget -ErrorAction SilentlyContinue
  if (-not $wg -and (Test-Path "$env:LOCALAPPDATA\Microsoft\WindowsApps\winget.exe")) { $wg = Get-Item "$env:LOCALAPPDATA\Microsoft\WindowsApps\winget.exe" }
}
if ($wg) {
  $winget = if ($wg.Source) { $wg.Source } else { $wg.FullName }
  Log "  winget $(Join (& $winget --version 2>&1)) at $winget"
  & $winget settings --enable LocalManifestFiles *>> $LOG
  $out = & $winget validate --manifest $m1 --disable-interactivity 2>&1; $st = $LASTEXITCODE
  Log "  winget validate --manifest $m1 -> exit=$st : $(Join $out)"
  if ($st -eq 0) { Pass 'winget: winget validate' (Join ($out | Select-Object -Last 1)) } else { Fail 'winget: winget validate' "exit=$st : $(Join $out)" }
  $out = & $winget install --manifest $m1 --accept-source-agreements --accept-package-agreements --disable-interactivity 2>&1; $st = $LASTEXITCODE
  $out | ForEach-Object { Add-Content -LiteralPath $LOG -Value "    winget install: $_" }
  $link = @("$env:LOCALAPPDATA\Microsoft\WinGet\Links\sigdek.exe", "$env:ProgramFiles\WinGet\Links\sigdek.exe") | Where-Object { Test-Path $_ } | Select-Object -First 1
  if ($st -eq 0 -and $link) {
    Pass 'winget: winget install --manifest' "exit 0; command at $link"
    CheckBin 'winget' $link
    $out = & $winget upgrade --manifest $m2 --accept-source-agreements --accept-package-agreements --disable-interactivity 2>&1; $st = $LASTEXITCODE
    $out | ForEach-Object { Add-Content -LiteralPath $LOG -Value "    winget upgrade: $_" }
    $v = Join (& $link version 2>&1)
    if ($v -eq "sigdek $V2 (windows/$arch)") { Pass 'upgrade: winget upgrade --manifest' $v } else { Note 'upgrade: winget upgrade --manifest' "exit=$st, now: $v" }
  } else {
    Fail 'winget: winget install --manifest' "exit=$st link=$link : $(Join ($out | Select-Object -Last 4))"
  }
} else {
  Fail 'winget: available on the runner' 'winget could not run here; see the log'
}

# --- release.yml's winget job, without submitting: the folder it picks, and wingetcreate's arguments
try {
  $dir = Get-ChildItem -Directory -Recurse "$ART\dist\$V1\winget\manifests" | Where-Object { $_.Name -match '^\d' } | Select-Object -First 1
  Log "  release.yml's folder pick: $($dir.FullName)"
  Invoke-WebRequest https://aka.ms/wingetcreate/latest -OutFile "$WORK\wingetcreate.exe"
  $help = & "$WORK\wingetcreate.exe" submit --help *>&1 | Out-String
  Add-Content -LiteralPath $LOG -Value $help
  if ($dir.Name -eq $V1 -and $help -match '--prtitle' -and $help -match '--token') { Pass 'winget: release.yml picks the manifest folder; wingetcreate submit takes --token and --prtitle' "$($dir.Name); wingetcreate $((Get-Item "$WORK\wingetcreate.exe").VersionInfo.ProductVersion)" } else { Fail 'winget: release.yml winget job inputs' "dir=$($dir.Name)" }
} catch { Fail 'winget: wingetcreate' "$_" }

# --- The sh launchers under Git Bash -----------------------------------------------------------
Log "== Git Bash: repository and plugin launchers (rehearsal/test-unix.sh)"
$env:SIGDEK_HOME = "$WORK\home-default"
$env:ART = Fwd $ART; $env:WORK = Fwd $WORK; $env:REPO = Fwd $REPO
$env:SECTIONS = 'repo plugin upgrade slow'; $env:SKIP_SERVERS = '1'
foreach ($v in 'NPM_CONFIG_USERCONFIG', 'npm_config_prefix') { Remove-Item "Env:$v" -ErrorAction SilentlyContinue }
& 'C:\Program Files\Git\bin\bash.exe' rehearsal/test-unix.sh

foreach ($s in $servers) { Stop-Process -Id $s.Id -Force -ErrorAction SilentlyContinue }
Log "== Done on windows"
$fails = @(Get-Content $RESULTS | Where-Object { $_ -like 'FAIL | windows*' })
Log "FAIL lines: $($fails.Count)"
if ($fails.Count) { exit 1 }
exit 0
