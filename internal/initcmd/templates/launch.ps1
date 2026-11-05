# SigDek launcher for Windows PowerShell. Same contract as launch.sh: runs the pinned
# recorder, downloading and checking its SHA-256 on first use; whatever goes wrong it exits 0,
# printing only the "carry on" answer Cursor needs (nothing for Claude Code).
# Source: https://github.com/tektonsoftware/sigdek
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # the progress bar makes Invoke-WebRequest far slower
$hookArgs = $args
$safe = $null
if ($hookArgs.Count -ge 3 -and $hookArgs[0] -ceq 'hook' -and $hookArgs[1] -ceq 'cursor') {
  if (@('preToolUse', 'subagentStart', 'beforeShellExecution', 'beforeMCPExecution', 'beforeReadFile') -ccontains $hookArgs[2]) {
    $safe = '{"permission":"allow"}'
  } elseif ($hookArgs[2] -ceq 'beforeSubmitPrompt') {
    $safe = '{"continue":true}'
  } else {
    $safe = '{}'
  }
}
function Exit-Safe {
  if ($safe) { try { [Console]::Out.Write($safe + "`n") } catch {} }
  exit 0
}
try {
  # Hooks often run without a console, where these setters throw; none of them is essential.
  $utf8 = New-Object System.Text.UTF8Encoding $false
  $OutputEncoding = $utf8
  try { [Console]::InputEncoding = $utf8 } catch {}
  try { [Console]::OutputEncoding = $utf8 } catch {}

  # Read the hook's input as bytes, at most 8 MB and for at most 3 seconds, so a tool that
  # never closes stdin can't hang the agent.
  $in = [Console]::OpenStandardInput()
  $buf = New-Object byte[] 65536
  $got = New-Object System.IO.MemoryStream
  $deadline = [DateTime]::UtcNow.AddSeconds(3)
  while ($got.Length -lt 8MB) {
    $read = $in.ReadAsync($buf, 0, $buf.Length)
    $left = [int][Math]::Max(0, ($deadline - [DateTime]::UtcNow).TotalMilliseconds)
    if (-not $read.Wait($left)) { break }
    if ($read.Result -le 0) { break }
    $got.Write($buf, 0, $read.Result)
  }
  $stdin = $utf8.GetString($got.ToArray())

  # A binary you installed or built yourself. Anyone who can set this, or edit the hook files,
  # can switch the recorder off; the team's Coverage report then shows this machine as silent.
  if ($env:SIGDEK_BIN -and (Test-Path -LiteralPath $env:SIGDEK_BIN)) {
    $stdin | & $env:SIGDEK_BIN @hookArgs
    exit 0
  }
  $dir = Split-Path -Parent $MyInvocation.MyCommand.Path
  $lock = @{}
  Get-Content -LiteralPath (Join-Path $dir 'recorder.lock') | ForEach-Object {
    if ($_ -match '^([a-z0-9_]+)=(.*)$') { $lock[$Matches[1]] = $Matches[2].Trim() }
  }
  $version = $lock['version']
  if (-not $version) { Exit-Safe }
  $arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
  $cache = Join-Path $HOME '.sigdek\bin'
  if ($env:SIGDEK_HOME) { $cache = Join-Path $env:SIGDEK_HOME 'bin' }
  if ($env:SIGDEK_CACHE) { $cache = $env:SIGDEK_CACHE }
  if ($env:CLAUDE_PLUGIN_DATA) { $cache = $env:CLAUDE_PLUGIN_DATA }
  $bin = Join-Path (Join-Path $cache $version) 'sigdek.exe'
  if (-not (Test-Path -LiteralPath $bin)) {
    $want = $lock["windows_$arch"]
    New-Item -ItemType Directory -Force -Path (Split-Path $bin) | Out-Null
    if (-not $want) {
      # `sigdek init --offline` pins only the platform it ran on. Say so in the error log, once
      # a day, so `sigdek status` on this machine shows why nothing is recorded.
      try {
        $noted = Join-Path (Split-Path $bin) '.no-build'
        if (-not (Test-Path -LiteralPath $noted) -or ((Get-Date) - (Get-Item -LiteralPath $noted).LastWriteTime).TotalHours -ge 24) {
          Set-Content -LiteralPath $noted -Value ''
          $logDir = if ($env:SIGDEK_HOME) { $env:SIGDEK_HOME } else { Join-Path $HOME '.sigdek' }
          New-Item -ItemType Directory -Force -Path $logDir | Out-Null
          $v = $version -replace '[^A-Za-z0-9._+-]', ''
          $at = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
          Add-Content -LiteralPath (Join-Path $logDir 'errors.log') -Value ('{"at":"' + $at + '","error":"launcher: .sigdek/recorder.lock pins SigDek ' + $v + ' with no build for windows/' + $arch + ', so nothing is recorded on this machine; run sigdek init with network access and commit .sigdek/recorder.lock"}')
        }
      } catch {}
      Exit-Safe
    }
    if (-not $lock['url']) { Exit-Safe }

    # After a failed download, wait 1, 2, 4 ... up to 60 minutes before trying again; a new
    # checksum in recorder.lock retries at once.
    $stamp = Join-Path (Split-Path $bin) '.download-failed'
    $fails = 0
    if (Test-Path -LiteralPath $stamp) {
      $last = Get-Content -LiteralPath $stamp -Raw -ErrorAction SilentlyContinue
      if ($last -match '^\s*(\d+)\s+(\S+)' -and $Matches[2] -eq $want) {
        $fails = [int]$Matches[1]
        $wait = [Math]::Min(60, [Math]::Pow(2, [Math]::Max(0, $fails - 1)))
        if (((Get-Date) - (Get-Item -LiteralPath $stamp).LastWriteTime).TotalMinutes -lt $wait) { Exit-Safe }
      }
    }
    $tmp = "$bin.$PID.download"
    # Windows PowerShell 5.1 may offer only TLS 1.0 and 1.1, which GitHub refuses; add 1.2.
    try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch {}
    try {
      # The agent waits at most 10 seconds for a hook; the download must finish inside that.
      Invoke-WebRequest -UseBasicParsing -TimeoutSec 8 -Uri "$($lock['url'])/sigdek_${version}_windows_$arch.exe" -OutFile $tmp
      $sum = (Get-FileHash -Algorithm SHA256 -LiteralPath $tmp).Hash.ToLower()
      if ($sum -ne $want) { throw "checksum mismatch" }
      Move-Item -Force -LiteralPath $tmp -Destination $bin
      Remove-Item -Force -LiteralPath $stamp -ErrorAction SilentlyContinue
    } catch {
      Remove-Item -Force -LiteralPath $tmp -ErrorAction SilentlyContinue
      try { Set-Content -LiteralPath $stamp -Value "$($fails + 1) $want" } catch {}
      Exit-Safe
    }
  }
  $stdin | & $bin @hookArgs
  exit 0
} catch {
  Exit-Safe
}
