# SigDek launcher for Windows PowerShell. Same contract as launch.sh: runs the pinned
# recorder, downloading and checking its SHA-256 on first use; whatever goes wrong it exits 0,
# printing only the "carry on" answer Cursor needs (nothing for Claude Code).
# Source: https://github.com/tektonsoftware/sigdek
$started = [DateTime]::UtcNow
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
    # launch.sh (Git Bash) may be downloading the same recorder in the background; leave it be.
    $mark = Join-Path (Split-Path $bin) '.downloading'
    if ((Test-Path -LiteralPath $mark) -and ((Get-Date) - (Get-Item -LiteralPath $mark).LastWriteTime).TotalMinutes -lt 6) { Exit-Safe }
    # Windows PowerShell 5.1 may offer only TLS 1.0 and 1.1, which GitHub refuses; add 1.2.
    try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch {}

    # The agent waits at most 10 seconds for a hook, and Invoke-WebRequest's -TimeoutSec doesn't
    # limit the download itself. So the download stops 7 seconds after the hook started and keeps
    # what arrived in a partial file; the next event asks for the rest (an HTTP Range request),
    # and the event that completes it checks the SHA-256 and runs the recorder. One event appends
    # at a time. A partial that hasn't grown for an hour, or is over 64 MB, is dropped. An event
    # that adds to it doesn't count as a failed download; one that adds nothing does.
    $folder = Split-Path $bin
    Get-ChildItem -LiteralPath $folder -Filter '*.partial' -ErrorAction SilentlyContinue | Where-Object { ((Get-Date) - $_.LastWriteTime).TotalMinutes -ge 60 -or $_.Length -gt 64MB } | Remove-Item -Force -ErrorAction SilentlyContinue
    $part = Join-Path $folder "sigdek.exe.$($want.Substring(0, 16)).partial"
    try { $file = [IO.File]::Open($part, 'OpenOrCreate', 'ReadWrite', 'None') } catch { Exit-Safe } # another event is appending
    $until = $started.AddSeconds(7)
    $code = 0; $added = 0; $done = $false; $restart = $false; $bad = $false; $ok = $false
    $request = $null; $response = $null
    try {
      $have = $file.Length
      $request = [Net.WebRequest]::Create("$($lock['url'])/sigdek_${version}_windows_$arch.exe")
      $request.UserAgent = 'SigDek launcher'
      if ($have -gt 0) { $request.AddRange([long]$have) }
      $answer = $request.GetResponseAsync()
      try { $null = $answer.Wait([int][Math]::Max(0, ($until - [DateTime]::UtcNow).TotalMilliseconds)) } catch {}
      if ($answer.Status -eq 'RanToCompletion') {
        $response = $answer.Result
        $code = [int]$response.StatusCode
        $total = -1
        if ($code -eq 206 -and $response.Headers['Content-Range'] -match '^bytes (\d+)-\d+/(\d+)$' -and [long]$Matches[1] -eq $have) {
          $total = [long]$Matches[2]
        } elseif ($code -eq 200) {
          $file.SetLength(0) # the whole file: start over
          $total = $response.ContentLength
        } else {
          $restart = $true
        }
        if (-not $restart) {
          $null = $file.Seek(0, 'End')
          $body = $response.GetResponseStream()
          $chunk = New-Object byte[] 65536
          while ($true) {
            $ms = [int]($until - [DateTime]::UtcNow).TotalMilliseconds
            if ($ms -le 0) { break }
            $next = $body.ReadAsync($chunk, 0, $chunk.Length)
            try { $null = $next.Wait($ms) } catch {}
            if ($next.Status -ne 'RanToCompletion') { break }
            if ($next.Result -le 0) { $done = $total -lt 0 -or $file.Length -eq $total; break }
            $file.Write($chunk, 0, $next.Result)
            $added += $next.Result
            if ($file.Length -gt 64MB) { $bad = $true; break }
            if ($total -ge 0 -and $file.Length -ge $total) { $done = $file.Length -eq $total; $restart = -not $done; break }
          }
          $file.Flush()
        }
      } elseif ($answer.IsFaulted) {
        $e = $answer.Exception.InnerException
        if ($e -is [Net.WebException] -and $e.Response) { $code = [int]$e.Response.StatusCode }
        # 416: nothing left to send, so the partial may be complete (another event's last chunk).
        if ($code -eq 416) { $done = $true }
      }
      if ($done -and -not $bad) {
        $file.Position = 0
        $sha = [Security.Cryptography.SHA256]::Create()
        $sum = -join ($sha.ComputeHash($file) | ForEach-Object { $_.ToString('x2') })
        if ($sum -eq $want) { $ok = $true } elseif ($code -eq 416) { $restart = $true } else { $bad = $true }
      }
      if ($restart) { $file.SetLength(0) }
    } catch {
    } finally {
      # Abort first: closing a response with body left to read can wait for the rest.
      if ($request -and -not $done) { try { $request.Abort() } catch {} }
      if ($response) { try { $response.Close() } catch {} }
      $file.Close()
    }
    if ($ok) {
      try { Move-Item -Force -LiteralPath $part -Destination $bin } catch { Exit-Safe }
      Remove-Item -Force -LiteralPath $stamp -ErrorAction SilentlyContinue
    } elseif ($bad -or ($added -eq 0 -and -not $restart)) {
      if ($bad) { Remove-Item -Force -LiteralPath $part -ErrorAction SilentlyContinue }
      try { Set-Content -LiteralPath $stamp -Value "$($fails + 1) $want" } catch {}
      Exit-Safe
    } else {
      Remove-Item -Force -LiteralPath $stamp -ErrorAction SilentlyContinue # it's coming, slowly
      Exit-Safe
    }
  }
  $stdin | & $bin @hookArgs
  exit 0
} catch {
  Exit-Safe
}
