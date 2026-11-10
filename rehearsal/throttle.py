#!/usr/bin/env python3
# REHEARSAL ONLY (branch release-rehearsal, never merged).
# Serves a folder like `python3 -m http.server`, but at a fixed rate: a slow link.
#   throttle.py <port> <folder> <bytes per second>
import functools
import http.server
import sys
import time

port, folder, rate = int(sys.argv[1]), sys.argv[2], int(sys.argv[3])


class Slow(http.server.SimpleHTTPRequestHandler):
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
