#!/usr/bin/env python3
"""fakecam.py — a fake HTTP snapshot camera for the watchglass demo rig.

Serves GET /snapshot.jpg, returning one of the six PNG frames in
internal/demo/frames/printer/frame_0.png .. frame_5.png (the same frames
the watchglass binary embeds for `watchglass -demo`). watchglass's HTTP source decodes the
body by sniffing its magic bytes (image/Decode), not by URL extension or
Content-Type, so serving PNG bytes at a ".jpg" path — the shape a lot of
real IP cameras use — is intentional and works fine; we still send
"Content-Type: image/png" because it costs nothing and is honest.

Frame selection:
  * Normal mode: frame = int(time.time() / period) % 6 — a free-running
    loop keyed off the wall clock, so the "printer" is always mid-job no
    matter when you start watchglass.
  * --once-complete: frame advances 0, 1, 2, 3, 4, 5 once, one frame per
    `period` seconds counted from this process's start, then holds frame 5
    (PRINT COMPLETE) forever. This is the shot for the launch GIF: start
    the server, start watchglass, and the printer finishes exactly once.
  * --hold N: serve frame N forever. For scripted recordings: hold a
    mid-print frame while you frame the shot, then restart the server
    with --once-complete for the finish — the free-running loop can hit
    PRINT COMPLETE at an unscripted moment and fire the trigger early.
  * --bind ADDR: interface to listen on (default 127.0.0.1). Use 0.0.0.0
    to feed a watchglass running in a container, which reaches the host
    at host.docker.internal:<port> rather than 127.0.0.1.
  * --frames DIR|printer|sevenseg: serve frame_0.png .. frame_5.png from
    DIR, or name one of the built-in sets: "sevenseg" is the seven-segment
    display rig. The old examples/demo/frames and frames-sevenseg paths
    still work; they map to the built-in sets.

Only GET /snapshot.jpg is served; anything else is a 404. stdlib only.
"""
import argparse
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

# The frames live with the Go code that embeds them (internal/demo), so the
# demo camera built into the binary and this script show the same pictures.
BUILTIN_FRAMES = Path(__file__).resolve().parent.parent.parent / "internal" / "demo" / "frames"
FRAMES_DIR = BUILTIN_FRAMES / "printer"
FRAME_COUNT = 6
# Where the frames used to be, relative to this script, for commands and
# scripts written before they moved.
LEGACY_DIRS = {"frames": "printer", "frames-sevenseg": "sevenseg"}


def resolve_frames(arg):
    """A --frames value: a directory, a built-in set's name, or an old path."""
    path = Path(arg)
    if path.is_dir():
        return path
    if str(arg) in ("printer", "sevenseg"):
        return BUILTIN_FRAMES / str(arg)
    if path.name in LEGACY_DIRS:
        return BUILTIN_FRAMES / LEGACY_DIRS[path.name]
    return path


def load_frames(frames_dir=FRAMES_DIR):
    frames = []
    for i in range(FRAME_COUNT):
        path = Path(frames_dir) / f"frame_{i}.png"
        frames.append(path.read_bytes())
    return frames


def frame_index(start, period, once_complete, hold):
    if hold is not None:
        return hold
    if once_complete:
        elapsed = time.time() - start
        idx = int(elapsed / period)
        return min(idx, FRAME_COUNT - 1)
    return int(time.time() / period) % FRAME_COUNT


def make_handler(frames, start, period, once_complete, hold):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))

        def do_GET(self):
            if self.path != "/snapshot.jpg":
                self.send_error(404, "not found")
                return
            idx = frame_index(start, period, once_complete, hold)
            body = frames[idx]
            self.send_response(200)
            self.send_header("Content-Type", "image/png")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(body)
            self.log_message('GET /snapshot.jpg -> frame_%d.png (%d bytes)', idx, len(body))

    return Handler


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--period", type=float, default=5.0,
                         help="seconds each frame is shown (default 5.0)")
    parser.add_argument("--port", type=int, default=8100,
                         help="port to listen on (default 8100)")
    parser.add_argument("--once-complete", action="store_true",
                         help="play frames 0..5 once, then hold PRINT COMPLETE forever")
    parser.add_argument("--hold", type=int, choices=range(FRAME_COUNT), default=None,
                         help="serve this frame forever (for scripted recordings)")
    parser.add_argument("--bind", default="127.0.0.1",
                         help="interface to listen on (default 127.0.0.1; 0.0.0.0 for containers)")
    parser.add_argument("--frames", default=FRAMES_DIR,
                         help="directory holding frame_0.png .. frame_5.png, or printer / sevenseg "
                              "for a built-in set (default printer)")
    args = parser.parse_args()

    args.frames = resolve_frames(args.frames)
    frames = load_frames(args.frames)
    start = time.time()
    handler = make_handler(frames, start, args.period, args.once_complete, args.hold)
    server = ThreadingHTTPServer((args.bind, args.port), handler)

    mode = (f"hold frame_{args.hold}" if args.hold is not None
            else "once-complete" if args.once_complete else "looping")
    print(f"fakecam: serving http://{args.bind}:{args.port}/snapshot.jpg "
          f"(period={args.period}s, mode={mode}, frames={args.frames})", file=sys.stderr)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
