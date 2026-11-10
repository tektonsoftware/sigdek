#!/usr/bin/env python3
# REHEARSAL ONLY (branch release-rehearsal, never merged).
# Serves a folder like `python3 -m http.server`, but at a fixed rate: a slow link.
#   throttle.py <port> <folder> <bytes per second> [log file]
# The log file gets each line flushed, so scripts can count requests while it runs (on Windows,
# a log redirected through Start-Process reaches the file only in buffered chunks).
import functools
import http.server
import sys
import time

port, folder, rate = int(sys.argv[1]), sys.argv[2], int(sys.argv[3])
log = open(sys.argv[4], "a", buffering=1) if len(sys.argv) > 4 else sys.stderr


class Slow(http.server.SimpleHTTPRequestHandler):
    def log_message(self, format, *args):
        log.write("%s - - [%s] %s\n" % (self.address_string(), self.log_date_time_string(), format % args))
        log.flush()

    def copyfile(self, src, dst):
        chunk = max(1024, rate // 20)
        start, sent = time.monotonic(), 0
        try:
            while True:
                b = src.read(chunk)
                if not b:
                    break
                dst.write(b)
                dst.flush()
                sent += len(b)
                ahead = sent / rate - (time.monotonic() - start)
                if ahead > 0:
                    time.sleep(ahead)
            self.log_message("sent all %d bytes in %.1f s", sent, time.monotonic() - start)
        except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
            self.log_message("client went away after %d bytes, %.1f s", sent, time.monotonic() - start)


http.server.ThreadingHTTPServer(("127.0.0.1", port), functools.partial(Slow, directory=folder)).serve_forever()
