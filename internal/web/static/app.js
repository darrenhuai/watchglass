// watchglass detail-page glue: canvas rectangle editor, test-region fetch,
// live-readout polling. No dependencies.
(function () {
  var stage = document.getElementById("stage");
  if (!stage) return;
  var img = document.getElementById("snap");
  var canvas = document.getElementById("overlay");
  var form = document.getElementById("watchform");
  var fx = form.elements["x"], fy = form.elements["y"],
      fw = form.elements["w"], fh = form.elements["h"];
  var name = stage.dataset.name;
  var base = stage.dataset.base || "";
  // Called after the drag or the manual fields change the region inputs
  // (in code, so the form sees no event); bound to the dirty check at the
  // end of this script.
  var regionEdited = function () {};
  var ACCENT = getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() || "#7fd4a8";

  // The editor works in CSS px throughout (pointer positions, hit-testing,
  // painting); only the backing store is scaled by devicePixelRatio, so
  // the 2px stroke and the handles stay crisp on phones and retina
  // screens instead of being upscaled bitmaps. cssW/cssH are the canvas's
  // on-screen size, 0 until an image has loaded.
  var cssW = 0, cssH = 0;
  function sizeCanvas() {
    canvas.hidden = false;
    var w = img.clientWidth, h = img.clientHeight, d = window.devicePixelRatio || 1;
    var bw = Math.round(w * d), bh = Math.round(h * d);
    // A same-size reload (the periodic snapshot refresh) only needs the
    // rectangle repainted. Resizing goes through drawRect, which also
    // rewrites the manual region fields — and would clobber a value the
    // user is mid-keystroke in. The backing size is compared too, so a
    // DPR change (browser zoom, a move to another monitor) resizes.
    if (w === cssW && h === cssH && canvas.width === bw && canvas.height === bh) {
      paint();
      return;
    }
    cssW = w;
    cssH = h;
    canvas.style.width = w + "px";
    canvas.style.height = h + "px";
    canvas.width = bw;
    canvas.height = bh;
    drawRect();
  }
  function region() {
    return {
      x: parseFloat(fx.value) || 0, y: parseFloat(fy.value) || 0,
      w: parseFloat(fw.value) || 0, h: parseFloat(fh.value) || 0
    };
  }
  // The region inputs hold four decimals, so every edge lives on a 1e-4
  // grid. Both edges are snapped to that grid as integers (0..GRID) and
  // the width is their difference: x + w as decimal strings can then never
  // come out above 1.0000, which rounding each of x and w on its own did
  // (0.0063 + 0.9938) and the server rejected.
  var GRID = 1e4;
  function toGrid(v) { return Math.round(Math.min(Math.max(v, 0), 1) * GRID); }
  function fmt(g) { return (g / GRID).toFixed(4); }
  function setRegion(x, y, w, h) {
    fx.value = fmt(x);
    fy.value = fmt(y);
    fw.value = fmt(w);
    fh.value = fmt(h);
  }
  // A new watch starts on the whole frame; that region is drawn as a
  // dashed edge with nothing dimmed, and a press inside it draws a new
  // one (there is no "outside" to press on).
  function isFull(r) { return r.x <= 0.001 && r.y <= 0.001 && r.w >= 0.999 && r.h >= 0.999; }
  // A region that nearly fills the frame (what a drag into a corner saves,
  // e.g. 0.0056/0.0066/0.9944/0.9934) has no room to move on either axis
  // and no exterior wide enough to start a new drag on, so a press inside
  // it would be a move clamped to nothing. It behaves like the full frame:
  // off its handles, a press draws a new rectangle. Uses DRAG_START_PX
  // (below) at call time.
  function roomless(r) {
    var room = 2 * DRAG_START_PX;
    return (1 - r.w) * cssW < room && (1 - r.h) * cssH < room;
  }
  // should_fix 7: keyboard/switch/screen-reader users can't drive the
  // pointerdown/pointermove drag below at all, so "Set region manually"
  // (four number inputs revealed by a <details>) is the other input path
  // to fx/fy/fw/fh, beside the arrow keys on the focused canvas. All paths
  // funnel through region()/paint(), so whichever one last touched the
  // hidden fields stays authoritative.
  //
  // syncManualFields and paint are kept deliberately separate from each
  // other (see drawRect vs. applyManualFields below): syncManualFields
  // reformats mx/my/mw/mh from the hidden fields' current value, which is
  // exactly what must NOT happen while those same fields are the ones the
  // user is actively mid-keystroke in — each keystroke's "input" event
  // would otherwise round-trip through region()'s parseFloat and stomp the
  // field back to a reformatted value, fighting the user's own typing (e.g.
  // typing "0.5" gets clobbered back to "0.0000" after the first "0").
  var mx = document.getElementById("m-x"), my = document.getElementById("m-y"),
      mw = document.getElementById("m-w"), mh = document.getElementById("m-h");
  var MANUAL = mx ? [mx, my, mw, mh] : [];
  var MANUAL_NAMES = ["Left", "Top", "Width", "Height"];
  var manual = document.getElementById("region-manual");
  var manualErr = document.getElementById("region-manual-error");
  function setManualValidity(el, msg) {
    el.setCustomValidity(msg);
    el.setAttribute("aria-invalid", msg ? "true" : "false");
  }
  // The first problem, named, under the fields; the fields carry
  // aria-invalid (red) themselves. Hidden when there is none.
  function showManualError() {
    if (!manualErr) return;
    var text = "";
    MANUAL.some(function (el, i) {
      if (!el.validationMessage) return false;
      text = MANUAL_NAMES[i] + ": " + el.validationMessage + ".";
      return true;
    });
    // A polite live region that is always rendered (empty collapses to
    // nothing in CSS), written only when the message changes, so a screen
    // reader hears the reason once, not on every keystroke.
    if (manualErr.textContent !== text) manualErr.textContent = text;
  }
  function syncManualFields() {
    if (!mx) return;
    var r = region();
    mx.value = r.x.toFixed(4);
    my.value = r.y.toFixed(4);
    mw.value = r.w.toFixed(4);
    mh.value = r.h.toFixed(4);
    MANUAL.forEach(function (el) { setManualValidity(el, ""); });
    showManualError();
  }
  // The stroke is inset 1px so a region on the frame's edge keeps its edge
  // inside #stage's clip; the handle centres are kept inside the canvas the
  // same way. hitTest reads the same numbers paint draws, so a grab lands
  // where the handle is.
  var HANDLE = 8, INSET = 1;
  function geom(r) {
    var x0 = Math.round(r.x * cssW), y0 = Math.round(r.y * cssH);
    var x1 = Math.round((r.x + r.w) * cssW), y1 = Math.round((r.y + r.h) * cssH);
    var sx = Math.max(x0, INSET), sy = Math.max(y0, INSET);
    var ex = Math.min(x1, cssW - INSET), ey = Math.min(y1, cssH - INSET);
    var m = HANDLE / 2 + 1;
    function c(v, max) { return Math.min(Math.max(v, m), max - m); }
    return {
      x0: x0, y0: y0, x1: x1, y1: y1, sx: sx, sy: sy, ex: ex, ey: ey,
      corners: {
        nw: [c(sx, cssW), c(sy, cssH)], ne: [c(ex, cssW), c(sy, cssH)],
        sw: [c(sx, cssW), c(ey, cssH)], se: [c(ex, cssW), c(ey, cssH)]
      }
    };
  }
  function paint() {
    syncHint();
    if (!cssW || !cssH) return;
    var ctx = canvas.getContext("2d");
    ctx.setTransform(canvas.width / cssW, 0, 0, canvas.height / cssH, 0, 0);
    ctx.clearRect(0, 0, cssW, cssH);
    var r = region();
    if (!(r.w > 0) || !(r.h > 0)) return;
    var g = geom(r), full = isFull(r);
    // What is outside the region is dimmed rather than the inside tinted:
    // a 12% mint tint vanished on the bright frames the region is often
    // meant to watch. The stroke sits on a dark halo for the same reason.
    if (!full) {
      ctx.fillStyle = "rgba(0, 0, 0, 0.4)";
      ctx.fillRect(0, 0, cssW, cssH);
      ctx.clearRect(g.x0, g.y0, g.x1 - g.x0, g.y1 - g.y0);
    }
    ctx.setLineDash(full ? [6, 4] : []);
    ctx.lineWidth = 4;
    ctx.strokeStyle = "rgba(0, 0, 0, 0.7)";
    ctx.strokeRect(g.sx, g.sy, g.ex - g.sx, g.ey - g.sy);
    ctx.lineWidth = 2;
    ctx.strokeStyle = ACCENT;
    ctx.strokeRect(g.sx, g.sy, g.ex - g.sx, g.ey - g.sy);
    ctx.setLineDash([]);
    ctx.lineWidth = 1;
    ctx.strokeStyle = "rgba(0, 0, 0, 0.75)";
    ctx.fillStyle = ACCENT;
    Object.keys(g.corners).forEach(function (k) {
      var c = g.corners[k];
      ctx.fillRect(c[0] - HANDLE / 2, c[1] - HANDLE / 2, HANDLE, HANDLE);
      ctx.strokeRect(c[0] - HANDLE / 2 + 0.5, c[1] - HANDLE / 2 + 0.5, HANDLE - 1, HANDLE - 1);
    });
  }
  // drawRect is the drag/load path: it syncs the manual fields to match
  // (they aren't the source of the change) and repaints.
  function drawRect() {
    syncManualFields();
    paint();
  }
  // Each field's problem, in field order, "" where there is none: blank,
  // not a number, outside 0..1, a zero size, or the two edges together
  // running past the frame. The cross-field check allows a rounding hair
  // so a four-decimal region on the edge is never flagged.
  function manualProblems() {
    var v = MANUAL.map(function (el) { return el.value === "" ? NaN : parseFloat(el.value); });
    var msgs = MANUAL.map(function (el, i) {
      if (el.value === "") return "Required";
      if (isNaN(v[i])) return "Must be a number";
      if (v[i] < 0 || v[i] > 1) return "Must be between 0 and 1";
      if (i >= 2 && v[i] <= 0) return "Must be greater than 0";
      return "";
    });
    if (!msgs[0] && !msgs[2] && v[0] + v[2] > 1 + 5e-5) msgs[2] = "Left + Width can't exceed 1";
    if (!msgs[1] && !msgs[3] && v[1] + v[3] > 1 + 5e-5) msgs[3] = "Top + Height can't exceed 1";
    return msgs;
  }
  // applyManualFields is the manual-entry path: the manual fields ARE the
  // source of the change and must be left exactly as typed, so this never
  // calls syncManualFields. The hidden fields are written only once all
  // four values make a region; until then the painted rectangle is the
  // last valid one and the red field says which value is the problem.
  function applyManualFields() {
    var msgs = manualProblems();
    MANUAL.forEach(function (el, i) { setManualValidity(el, msgs[i]); });
    showManualError();
    if (msgs.join("")) return;
    var v = MANUAL.map(function (el) { return parseFloat(el.value); });
    var x = toGrid(v[0]), y = toGrid(v[1]);
    setRegion(x, y, Math.min(toGrid(v[2]), GRID - x), Math.min(toGrid(v[3]), GRID - y));
    paint();
    markTestStale();
    regionEdited();
  }
  // Arrow keys in a manual field step by 0.01 (Shift 0.1, Alt 0.001); the
  // native step would be 1 with step=any, and the 0.0001 of old took ten
  // thousand presses to cross the frame.
  function manualStep(e) {
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
    e.preventDefault();
    var step = e.altKey ? 10 : e.shiftKey ? 1000 : 100;
    var cur = parseFloat(e.target.value);
    var g = toGrid(isNaN(cur) ? 0 : cur) + (e.key === "ArrowUp" ? step : -step);
    e.target.value = fmt(Math.min(Math.max(g, 0), GRID));
    applyManualFields();
  }
  // Test and Save read the region inputs, which a bad manual value never
  // reached: rather than run on the last valid region, open the fields and
  // point at the problem. The inputs sit outside #watchform (they carry no
  // name), so the form's own validation can't see them.
  function manualGate(e) {
    var bad = null;
    MANUAL.some(function (el) { if (!el.checkValidity()) { bad = el; return true; } return false; });
    if (!bad) return false;
    if (e) e.preventDefault();
    if (manual) manual.open = true;
    bad.reportValidity();
    return true;
  }
  // The hint under the stage, from state: nothing to draw on, touch editing
  // on or off, the whole frame selected. One function, so the three
  // never overwrite each other.
  var hint = document.getElementById("region-hint");
  var coarse = !!(window.matchMedia && window.matchMedia("(pointer: coarse)").matches);
  function editing() { return stage.classList.contains("is-editing"); }
  function hintText() {
    if (img.hidden) return "No frame to draw on. Set the region manually below.";
    var full = isFull(region());
    var lead = full ? "Whole frame selected. " : "";
    var aim = full ? "narrow it" : "select the region to watch";
    if (editing()) return lead + "Drag on the image to " + aim + ". Tap Done when you're finished.";
    if (coarse) return lead + "Tap Edit region, then drag on the image to " + aim + ".";
    return lead + "Drag on the image to " + aim + ", or set it manually below.";
  }
  function syncHint() {
    if (!hint) return;
    var t = hintText();
    if (hint.textContent !== t) hint.textContent = t;
  }
  // Touch editing mode (see #stage canvas in style.css): off, a finger on
  // the snapshot scrolls the page; on, it draws. Save turns it off.
  var editBtn = document.getElementById("region-edit");
  function setEditing(on) {
    stage.classList.toggle("is-editing", on);
    if (editBtn) {
      editBtn.setAttribute("aria-pressed", on ? "true" : "false");
      editBtn.textContent = on ? "Done" : "Edit region";
    }
    syncHint();
  }
  if (editBtn) editBtn.addEventListener("click", function () { setEditing(!editing()); });
  form.addEventListener("submit", function (e) {
    if (manualGate(e)) return;
    setEditing(false);
  });
  // A test result describes the region and settings it was run with. Once
  // either changes it stays on screen (it's still useful to compare) but
  // says it is out of date. The drag and the manual fields write the hidden
  // region inputs directly, which fires no events, so they call this
  // themselves; the fields a test reads are watched below.
  function markTestStale() {
    var panel = document.querySelector("#test-result .test-panel");
    if (panel && !panel.classList.contains("is-stale")) panel.classList.add("is-stale");
  }
  if (mx) {
    MANUAL.forEach(function (el) {
      el.addEventListener("input", applyManualFields);
      el.addEventListener("keydown", manualStep);
    });
    // Leaving a field reformats it to four decimals (0.5 becomes 0.5000)
    // once every value is good; a field left blank or out of range keeps
    // what was typed, marked, so the problem stays visible.
    manual.addEventListener("change", function () {
      if (!manualProblems().join("")) syncManualFields();
    });
    // Populate from the configured region right away, not only from the
    // image-load path (drawRect): when the camera is down the image never
    // loads, and manual entry is then the ONLY way to edit the region — it
    // must start from the real values, not from blanks.
    syncManualFields();
  }

  // The drag. A press picks a mode from what is under the pointer: a
  // corner handle resizes from the opposite corner, the inside moves the
  // rectangle whole, anywhere else draws a new one. Nothing is written
  // until the pointer has travelled DRAG_START_PX, and a press that ends
  // before that, is cancelled, or leaves a region under MIN_REGION_PX in
  // either direction puts the region the press started from back: a
  // click, a jitter or a resting thumb used to replace the saved region
  // with a speck, silently. Only the primary pointer's main button
  // draws; a second finger or the right button changes nothing.
  var DRAG_START_PX = 6, MIN_REGION_PX = 4;
  var CURSORS = { nw: "nwse-resize", se: "nwse-resize", ne: "nesw-resize", sw: "nesw-resize", move: "move", "new": "crosshair" };
  function pointerPos(e) {
    var b = canvas.getBoundingClientRect();
    return [e.clientX - b.left, e.clientY - b.top];
  }
  // What a press at (px, py) CSS px would do; tol is the handle's reach
  // (wider for a finger). Corners are tested before the inside, so a tiny
  // region whose handles overlap its inside can still be resized.
  function hitTest(px, py, tol) {
    var r = region();
    if (!cssW || !(r.w > 0) || !(r.h > 0)) return "new";
    var g = geom(r), hit = null;
    Object.keys(g.corners).some(function (k) {
      var c = g.corners[k];
      if (Math.abs(px - c[0]) <= tol && Math.abs(py - c[1]) <= tol) { hit = k; return true; }
      return false;
    });
    if (hit) return hit;
    if (isFull(r) || roomless(r)) return "new";
    return px >= g.x0 && px <= g.x1 && py >= g.y0 && py <= g.y1 ? "move" : "new";
  }
  var drag = null;
  canvas.addEventListener("pointerdown", function (e) {
    if (drag || !e.isPrimary || e.button !== 0) return;
    // A finger draws only in editing mode; otherwise the browser has the
    // gesture (touch-action pan-y) and the page scrolls.
    if (e.pointerType === "touch" && !editing()) return;
    var p = pointerPos(e), r = region();
    var mode = hitTest(p[0], p[1], e.pointerType === "touch" ? 14 : 10);
    drag = { id: e.pointerId, mode: mode, px: e.clientX, py: e.clientY, live: false,
      saved: [fx.value, fy.value, fw.value, fh.value] };
    if (mode === "move") {
      drag.w = toGrid(r.w);
      drag.h = toGrid(r.h);
      drag.dx = p[0] / cssW - r.x;
      drag.dy = p[1] / cssH - r.y;
    } else if (mode === "new") {
      drag.ax = p[0] / cssW;
      drag.ay = p[1] / cssH;
    } else {
      // Resize: the anchor is the corner opposite the one grabbed.
      drag.ax = mode === "nw" || mode === "sw" ? r.x + r.w : r.x;
      drag.ay = mode === "nw" || mode === "ne" ? r.y + r.h : r.y;
    }
    canvas.setPointerCapture(e.pointerId);
    canvas.style.cursor = CURSORS[mode];
  });
  canvas.addEventListener("pointermove", function (e) {
    if (!drag) {
      if (e.pointerType !== "touch") {
        var q = pointerPos(e);
        canvas.style.cursor = CURSORS[hitTest(q[0], q[1], 10)];
      }
      return;
    }
    if (e.pointerId !== drag.id) return;
    if (!drag.live) {
      if (Math.abs(e.clientX - drag.px) < DRAG_START_PX && Math.abs(e.clientY - drag.py) < DRAG_START_PX) return;
      drag.live = true;
    }
    var p = pointerPos(e);
    var x = Math.min(Math.max(p[0] / cssW, 0), 1), y = Math.min(Math.max(p[1] / cssH, 0), 1);
    if (drag.mode === "move") {
      setRegion(Math.min(toGrid(x - drag.dx), GRID - drag.w), Math.min(toGrid(y - drag.dy), GRID - drag.h), drag.w, drag.h);
    } else {
      var xa = toGrid(Math.min(drag.ax, x)), xb = toGrid(Math.max(drag.ax, x));
      var ya = toGrid(Math.min(drag.ay, y)), yb = toGrid(Math.max(drag.ay, y));
      setRegion(xa, ya, xb - xa, yb - ya);
    }
    drawRect();
    markTestStale();
    regionEdited();
  });
  function endDrag(e, cancelled) {
    if (!drag || e.pointerId !== drag.id) return;
    var r = region();
    if (cancelled || !drag.live || r.w * cssW < MIN_REGION_PX || r.h * cssH < MIN_REGION_PX) {
      fx.value = drag.saved[0];
      fy.value = drag.saved[1];
      fw.value = drag.saved[2];
      fh.value = drag.saved[3];
      drawRect();
      regionEdited();
    } else {
      announceRegion();
    }
    drag = null;
    var p = pointerPos(e);
    canvas.style.cursor = e.pointerType === "touch" ? "" : CURSORS[hitTest(p[0], p[1], 10)];
  }
  canvas.addEventListener("pointerup", function (e) { endDrag(e, false); });
  // A touch drag the browser takes for a scroll ends in pointercancel, not
  // pointerup; the region goes back and the stale drag no longer pauses
  // the snapshot refresh.
  canvas.addEventListener("pointercancel", function (e) { endDrag(e, true); });
  // Keyboard: arrows move the region by 1% of the frame, Shift+arrows
  // resize it (right/down grow), Alt makes either 0.1%. Nothing under 1%
  // wide or tall, nothing past the frame. The readout announces the result
  // once per key, on keyup, not on every repeat.
  var readout = document.getElementById("region-readout");
  var KEY_DX = { ArrowLeft: -1, ArrowRight: 1 }, KEY_DY = { ArrowUp: -1, ArrowDown: 1 };
  function announceRegion() {
    if (!readout) return;
    var r = region();
    function pct(v) { return Math.round(v * 100) + "%"; }
    readout.textContent = "Left " + pct(r.x) + ", top " + pct(r.y) + ", width " + pct(r.w) + ", height " + pct(r.h);
  }
  canvas.addEventListener("keydown", function (e) {
    var dx = KEY_DX[e.key] || 0, dy = KEY_DY[e.key] || 0;
    if ((!dx && !dy) || e.ctrlKey || e.metaKey) return;
    e.preventDefault();
    var step = e.altKey ? 10 : 100, r = region();
    var x = toGrid(r.x), y = toGrid(r.y), w = Math.max(toGrid(r.w), 100), h = Math.max(toGrid(r.h), 100);
    if (e.shiftKey) {
      w = Math.min(Math.max(w + dx * step, 100), GRID - x);
      h = Math.min(Math.max(h + dy * step, 100), GRID - y);
    } else {
      x = Math.min(Math.max(x + dx * step, 0), GRID - w);
      y = Math.min(Math.max(y + dy * step, 0), GRID - h);
    }
    setRegion(x, y, w, h);
    drawRect();
    markTestStale();
    regionEdited();
  });
  canvas.addEventListener("keyup", function (e) { if (KEY_DX[e.key] || KEY_DY[e.key]) announceRegion(); });
  canvas.addEventListener("focus", announceRegion);
  img.addEventListener("load", sizeCanvas);
  window.addEventListener("resize", sizeCanvas);

  // must_fix 2: the <img> has no way to show the /snapshot endpoint's own
  // 502 body — the browser just renders its generic broken-image glyph, no
  // branding, no explanation. On error, re-fetch the same URL (the <img>
  // request itself never exposes a failed response's body) purely to read
  // that text, then swap in an in-app placeholder showing it.
  var snapError = document.getElementById("snap-error");
  var snapURL = base + "/watch/" + encodeURIComponent(name) + "/snapshot";
  // One image retry per page load, at most: an intermittently failing
  // camera could otherwise bounce forever between a failed <img> load and
  // a successful diagnostic fetch, hammering it once per round trip.
  var snapRetried = false;
  // Error bodies from /snapshot and /test are text/plain: a one-line summary,
  // a newline, then the full error chain (web.go grabError). Everything
  // below builds DOM nodes and sets textContent: the chain echoes the
  // source URL, which is user input, so it must never reach innerHTML.
  function splitError(text) {
    text = (text || "").replace(/\s+$/, "");
    var i = text.indexOf("\n");
    if (i < 0) return { summary: text, raw: "" };
    var raw = text.slice(i + 1).trim();
    var summary = text.slice(0, i).trim();
    return { summary: summary, raw: raw === summary ? "" : raw };
  }
  function el(tag, className, text) {
    var n = document.createElement(tag);
    if (className) n.className = className;
    if (text != null) n.textContent = text;
    return n;
  }
  // The server writes times in its own zone (often UTC in a container) with
  // the instant in datetime; show them in the viewer's zone instead, with
  // the date when it isn't today and the full date and time on hover.
  function localizeTimes(root) {
    var times = root.querySelectorAll("time[datetime]");
    var today = new Date().toDateString();
    for (var i = 0; i < times.length; i++) {
      var d = new Date(times[i].getAttribute("datetime"));
      if (isNaN(d.getTime())) continue;
      var text = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
      if (d.toDateString() !== today) {
        text = d.toLocaleDateString([], { month: "short", day: "numeric" }) + ", " + text;
      }
      times[i].textContent = text;
      times[i].title = d.toLocaleString([], { dateStyle: "full", timeStyle: "long" });
    }
  }
  // How long ago a stale badge's time was, when that's at least a minute:
  // "3 min ago", "5 h ago", "2 days ago". A camera dead for days must not
  // read like one that failed a moment ago.
  function ageText(ms) {
    var min = Math.floor(ms / 60000);
    if (min < 1) return "";
    if (min < 60) return min + " min ago";
    var h = Math.floor(min / 60);
    if (h < 48) return h + " h ago";
    return Math.floor(h / 24) + " days ago";
  }
  function updateAges(root) {
    var ages = root.querySelectorAll(".stale-age");
    for (var i = 0; i < ages.length; i++) {
      var t = ages[i].previousElementSibling;
      var d = t && t.getAttribute("datetime") ? new Date(t.getAttribute("datetime")) : null;
      var a = d && !isNaN(d.getTime()) ? ageText(Date.now() - d.getTime()) : "";
      var text = a ? " · " + a : "";
      if (ages[i].textContent !== text) ages[i].textContent = text;
    }
  }
  localizeTimes(document);
  function techDetail(raw) {
    var d = el("details", "tech-detail");
    d.appendChild(el("summary", null, "Technical detail"));
    d.appendChild(el("p", "tech-raw mono", raw));
    return d;
  }
  // The placeholder's cause (summary + raw chain) for the last failed
  // snapshot. While the watch is in the error state the header above the
  // stage already says the same thing, so the placeholder then shows only
  // its heading; syncSnapCause re-renders when the header shows or hides.
  var snapCause = null, snapCauseHeader = null;
  function headerShowsError() {
    var header = document.getElementById("status-detail");
    return !!header && !header.hidden;
  }
  function syncSnapCause() {
    if (!snapError || !snapCause) return;
    var shown = headerShowsError();
    if (shown === snapCauseHeader) return;
    snapCauseHeader = shown;
    var old = snapError.querySelectorAll(".snap-cause, .tech-detail");
    for (var i = 0; i < old.length; i++) old[i].remove();
    if (shown) return;
    snapError.appendChild(el("p", "snap-cause", snapCause.summary || "No further detail available."));
    if (snapCause.raw) snapError.appendChild(techDetail(snapCause.raw));
  }
  // The slate: an LED, a heading, and (syncSnapCause) the cause under it.
  // The class says how bad it is (style.css): is-checking while the
  // diagnosis runs, is-slow when the watch itself is fine and only the
  // preview is missing, is-down when the source is unreachable.
  function renderSnap(cls, led, heading) {
    snapError.className = "snap-error " + cls;
    snapError.textContent = "";
    var h = el("strong");
    var dot = el("span", "led " + led);
    dot.setAttribute("aria-hidden", "true");
    h.appendChild(dot);
    h.appendChild(el("span", null, heading));
    snapError.appendChild(h);
  }
  // With no image there is nothing to drag on: the hint says so and the
  // manual fields open (and close again on the next image, but only if it
  // was this that opened them; a user who opened them keeps them).
  var manualAutoOpened = false;
  function noImage() {
    img.hidden = true;
    // The never-sized canvas (300x150 by default) would otherwise sit on top
    // of the placeholder, catching a text-select drag across the error
    // message as a region drag. sizeCanvas unhides it once an image loads.
    canvas.hidden = true;
    syncHint();
    if (manual && !manual.open) {
      manual.open = true;
      manualAutoOpened = true;
    }
  }
  img.addEventListener("load", function () {
    if (manualAutoOpened && manual) {
      manual.open = false;
      manualAutoOpened = false;
    }
  });
  // The last failed diagnosis: its HTTP status (0: no answer at all) and
  // the split error text. The heading depends on the status pill too (a
  // running watch with no preview is a slow camera, not a dead one), so
  // it is re-rendered when the pill changes, but only then: a rebuild on
  // every poll would close an opened technical detail.
  var snapLast = null;
  function pillIsError() {
    var p = document.getElementById("status-pill");
    return !!p && p.classList.contains("status-error");
  }
  function renderSnapResult() {
    if (!snapLast || !snapError || snapError.hidden) return;
    var st = snapLast.status, heading, down;
    if (st === 200) { heading = "Frame failed to load"; down = false; }
    else if (st === 400) { heading = "Source misconfigured"; down = true; }
    else if (st === 0) { heading = "watchglass didn't answer"; down = true; }
    else if (pillIsError()) { heading = "Camera unreachable"; down = true; }
    else { heading = "No frame from camera"; down = false; }
    var key = heading + (down ? "!" : "");
    if (key === snapLast.key) return;
    snapLast.key = key;
    renderSnap(down ? "is-down" : "is-slow", down ? "led-error" : "led-amber", heading);
    snapCause = { summary: snapLast.summary, raw: snapLast.raw };
    snapCauseHeader = null;
    syncSnapCause();
  }
  function showSnapError() {
    noImage();
    snapCause = null;
    snapLast = null;
    if (!snapError) return;
    snapError.hidden = false;
    renderSnap("is-checking", "led-amber", "No frame yet");
    snapError.appendChild(el("p", "snap-cause", "Checking the camera…"));
    fetch(snapURL)
      .then(function (resp) {
        // Whenever the body isn't read as text below, cancel it: an
        // unread response body keeps the request in flight (and holds a
        // frame's worth of PNG) until it is garbage-collected.
        var discard = function () { if (resp.body) resp.body.cancel(); };
        if (resp.ok) {
          discard();
          // The camera answered this time (a transient failure, not a dead
          // source). Retry the image once instead of printing the PNG body
          // as the "reason".
          if (!snapRetried) {
            snapRetried = true;
            snapError.hidden = true;
            img.hidden = false;
            img.src = snapURL + "?r=" + Date.now();
            return null;
          }
          return { status: 200, text: "The camera answered but the image didn't arrive. Retrying automatically." };
        }
        var ct = resp.headers.get("Content-Type") || "";
        if (ct.indexOf("text/") !== 0) {
          discard();
          return { status: resp.status, text: "watchglass answered HTTP " + resp.status };
        }
        return resp.text().then(function (t) { return { status: resp.status, text: t }; });
      })
      .then(function (r) {
        if (r === null) return;
        var e = splitError(r.text);
        snapLast = { status: r.status, summary: e.summary, raw: e.raw, key: "" };
        renderSnapResult();
      })
      .catch(function () {
        snapLast = { status: 0, summary: "The snapshot request failed. Is watchglass still running?", raw: "", key: "" };
        renderSnapResult();
      });
  }
  img.addEventListener("error", showSnapError);
  // A same-host /snapshot request against a dead source can 502 fast enough
  // that the browser dispatches "error" before this deferred script even
  // runs — img.complete is already true by then, so the "load" case above
  // never fires either. naturalWidth stays 0 only on a failed decode (a
  // real image is never 0x0), which is how a load-vs-error outcome that
  // already happened is told apart here.
  if (img.complete) {
    if (img.naturalWidth === 0) {
      showSnapError();
    } else {
      sizeCanvas();
    }
  }

  // The frame above was fetched once at page load while the Live strip
  // under it polls every 2s, so the two visibly drifted apart and the page
  // read as frozen. Refresh it on the watch's own interval, floored at 3s
  // (each tick is a second camera grab on top of the runner's own) and
  // capped at an hour (a monthly-poll watch still gets a fresh frame, and
  // the delay stays inside setInterval's 32-bit range), by
  // preloading into a detached Image and swapping src only once that has
  // decoded: the visible frame is never blanked, and a failed preload never
  // reaches the <img> error path — a lone miss just waits for the next
  // tick. Two misses in a row is a dead camera rather than a blip, so hand
  // off to showSnapError then, which puts up the placeholder and its
  // diagnosis without a reload. Ticks carry on underneath the placeholder,
  // and the first preload that succeeds clears it again.
  var DURATION_UNIT = { ms: 1, s: 1000, m: 60000, h: 3600000 };
  // Go duration syntax as the form shows it ("2s", "500ms", "1m30s");
  // anything else falls back to 5s.
  function parseDuration(s) {
    if (!/^(\d+(\.\d+)?(ms|s|m|h))+$/.test(s)) return 5000;
    var ms = 0;
    s.replace(/(\d+(?:\.\d+)?)(ms|s|m|h)/g, function (_, n, u) { ms += n * DURATION_UNIT[u]; });
    return ms;
  }
  var intervalField = form.elements["interval"];
  var refreshMs = Math.min(Math.max(parseDuration(intervalField ? intervalField.value : ""), 3000), 3600000);
  var preload = null, misses = 0;
  function refreshSnap() {
    // A swap mid-drag would resize the canvas under the pointer; a hidden
    // tab has nobody looking; a preload still in flight means the camera
    // is slow, and stacking a second request on it only makes that worse.
    if (preload || drag || document.visibilityState !== "visible") return;
    var next = new Image();
    next.onload = function () {
      preload = null;
      misses = 0;
      if (snapError) snapError.hidden = true;
      img.hidden = false;
      img.src = next.src;
    };
    next.onerror = function () {
      preload = null;
      if (++misses >= 2 && !img.hidden) showSnapError();
    };
    next.src = snapURL + "?t=" + Date.now();
    preload = next;
  }
  setInterval(refreshSnap, refreshMs);

  // Only a successful text/html answer (testresult.html, escaped by
  // html/template) is inserted as markup. An error body is plain text that
  // can echo user input, and is always rendered as text.
  function renderTestError(box, text) {
    var e = splitError(text);
    box.textContent = "";
    var wrap = el("div", "test-error");
    var head = el("p", "test-error-title");
    var led = el("span", "led led-error");
    led.setAttribute("aria-hidden", "true");
    head.appendChild(led);
    head.appendChild(el("span", null, "Test failed"));
    wrap.appendChild(head);
    wrap.appendChild(el("p", "test-error-summary", e.summary));
    if (e.raw) wrap.appendChild(techDetail(e.raw));
    box.appendChild(wrap);
  }
  var testBox = document.getElementById("test-result");
  var testBtn = document.getElementById("testbtn");
  // Side by side (>= 920px) style.css pins the stage column --stage-gap
  // under the viewport top while the form scrolls. A column taller than
  // the viewport (a test result, the manual fields open on a short laptop)
  // pinned like that would keep its bottom — the result, the Live strip —
  // out of reach, so from then on it behaves like a sidebar: scrolling
  // down lets it travel with the page until its bottom sits --stage-gap
  // above the viewport bottom and it pins there; scrolling up lets it
  // travel until its top is back at --stage-gap. Between the two pins it
  // is position:relative at a fixed offset from its place in the grid, so
  // a change of its own height (a result landing, the fields opening, the
  // result dismissed) grows or shrinks it downwards from where it is and
  // never moves what is on screen; only the page's own scrolling does.
  // data-pin says which of the three it is in.
  var colStage = document.querySelector(".col-stage");
  var stageGrid = colStage && colStage.parentNode;
  // mode, the page offset the last scroll event was handled at, and the
  // column's viewport top as last seen: positions are worked out from
  // that, never from the column's current height, so a scroll and a
  // height change landing in the same frame (click × right after a
  // wheel tick) still put the column where it was, moved with the page.
  var stagePin = { mode: "top", lastY: window.scrollY, top: 0 };
  function setPin(mode, top) {
    stagePin.mode = mode;
    colStage.style.position = mode === "free" ? "relative" : "";
    colStage.style.top = mode === "top" ? "" : top + "px";
    colStage.dataset.pin = mode;
  }
  function noteStageTop() { stagePin.top = colStage.getBoundingClientRect().top; }
  // The stacked layout has no --stage-gap: any inline offset left over
  // from the side-by-side one is dropped there.
  function stagePinned() {
    var gap = parseFloat(getComputedStyle(colStage).getPropertyValue("--stage-gap"));
    if (gap !== gap && stagePin.mode !== "top") { setPin("top"); stageGrid.style.minHeight = ""; }
    return gap === gap ? gap : NaN;
  }
  function stageMetrics(gap) {
    var grid = stageGrid.getBoundingClientRect();
    var col = colStage.getBoundingClientRect();
    return { gap: gap, top: col.top, h: col.height, natural: grid.top,
      tall: col.height > window.innerHeight - 2 * gap,
      bottomPin: window.innerHeight - gap - col.height };
  }
  // A sticky column cannot leave its grid: pinned deep in the form and
  // then outgrowing the grid's end (the form is barely taller than the
  // stage plus a result), it would be shoved up to fit. So the grid is
  // kept at least as tall as a tall column needs at the offset it wants
  // — and only a tall one: a short pinned column that is being scrolled
  // past and updated by the live strip must never ratchet the page longer.
  // Returns the offset the column can actually have (the grid's end bounds
  // a short column; a tall one has just been given the room).
  function fitStageGrid(m, offset) {
    offset = Math.max(0, offset);
    stageGrid.style.minHeight = m.tall ? offset + m.h + "px" : "";
    return Math.min(offset, Math.max(0, stageGrid.getBoundingClientRect().height - m.h));
  }
  // Free the column at viewport top `top`: never above its place in the
  // grid, never past the grid's end.
  function freeStage(m, top) {
    setPin("free", fitStageGrid(m, top - m.natural));
  }
  function onStageScroll() {
    var y = window.scrollY, dy = y - stagePin.lastY;
    stagePin.lastY = y;
    var gap = stagePinned();
    if (gap !== gap || dy === 0) return;
    var m = stageMetrics(gap);
    if (stagePin.mode === "top") {
      // A tall column leaves the top pin as the page starts moving down,
      // from where it sat before this frame's scroll (so it travels from
      // the first pixel like any other block).
      if (m.tall && dy > 0) freeStage(m, stagePin.top - dy);
    } else if (stagePin.mode === "bottom") {
      if (dy < 0) freeStage(m, stagePin.top - dy);
    } else if (dy < 0 && m.top >= m.gap) {
      setPin("top");
    } else if (dy > 0 && m.tall && m.top + m.h <= window.innerHeight - m.gap) {
      fitStageGrid(m, m.bottomPin - m.natural);
      setPin("bottom", m.bottomPin);
    }
    noteStageTop();
  }
  // The column or the window changed size. Top-pinned, it grows downwards
  // from its pin (the grid is grown under it if it must); bottom-pinned,
  // it is freed where its top edge was (its bottom is what moved, and the
  // next scroll down re-pins it); free, it keeps its offset.
  function fitStage() {
    if (!colStage) return;
    var gap = stagePinned();
    if (gap !== gap) return;
    var m = stageMetrics(gap);
    if (stagePin.mode === "top") fitStageGrid(m, gap - m.natural);
    else if (stagePin.mode === "bottom") freeStage(m, stagePin.top - (window.scrollY - stagePin.lastY));
    else freeStage(m, m.natural + (parseFloat(colStage.style.top) || 0));
    noteStageTop();
  }
  if (colStage) {
    if (window.ResizeObserver) new ResizeObserver(fitStage).observe(colStage);
    window.addEventListener("resize", fitStage);
    window.addEventListener("scroll", onStageScroll, { passive: true });
    fitStage();
    noteStageTop();
  }
  // A result lands under the Test button, which on a phone or with the
  // manual fields open can be below the fold: bring it into view (a
  // pinned column that has just outgrown the viewport travels with that
  // scroll, see onStageScroll). Focus stays on the button (the box is a
  // live region, so the result is read out anyway) so a second Test is
  // one keypress away.
  function revealTestResult() {
    fitStage();
    var r = testBox.getBoundingClientRect();
    if (r.top >= 0 && r.bottom <= window.innerHeight) return;
    var reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    testBox.scrollIntoView({ block: "nearest", behavior: reduce ? "auto" : "smooth" });
  }
  // The × in the result's header clears it; focus goes back to the button
  // that makes a new one rather than being dropped on <body>.
  testBox.addEventListener("click", function (e) {
    if (!e.target.closest("[data-dismiss-test]")) return;
    testBox.textContent = "";
    testBtn.focus();
  });
  // Only the fields a test result depends on: the engine, the trigger it
  // checks and the preprocessing. Interval, notify and the like don't
  // change what a test shows.
  var TEST_FIELDS = { ttype: 1, engine: 1, pattern: 1, op: 1, tthreshold: 1, confirm: 1,
    pp_grayscale: 1, pp_invert: 1, pp_threshold: 1, pp_upscale: 1 };
  function testFieldChanged(e) {
    if (e.target && TEST_FIELDS[e.target.name]) markTestStale();
  }
  form.addEventListener("input", testFieldChanged);
  form.addEventListener("change", testFieldChanged);
  testBtn.addEventListener("click", function () {
    if (manualGate()) return;
    var box = testBox;
    box.textContent = "";
    box.appendChild(el("p", "muted", "Testing…"));
    fetch(base + "/watch/" + encodeURIComponent(name) + "/test", {
      method: "POST",
      body: new URLSearchParams(new FormData(form))
    }).then(function (resp) {
      return resp.text().then(function (t) {
        var html = (resp.headers.get("Content-Type") || "").indexOf("text/html") === 0;
        return { ok: resp.ok, html: html, text: t, status: resp.status };
      });
    }).then(function (r) {
      if (r.ok && r.html) {
        // Times are localised before the fragment lands, so the live
        // region announces the result once, already in the viewer's zone.
        var tpl = document.createElement("template");
        tpl.innerHTML = r.text;
        localizeTimes(tpl.content);
        box.textContent = "";
        box.appendChild(tpl.content);
        revealTestResult();
        return;
      }
      renderTestError(box, r.text.trim() || "watchglass answered HTTP " + r.status + ".");
      revealTestResult();
    }).catch(function () {
      renderTestError(box, "watchglass didn't answer. Is it still running?");
      revealTestResult();
    });
  });

  // The /live fragment is two blocks (see live.html): .live-status (the
  // stale/stopped badge + latest readout) and .strip (the filmstrip). Each
  // lands in its own container and is only swapped in when its markup
  // actually changed. #live-status is the screen-reader live region: with
  // aria-atomic it re-announces its whole content on ANY DOM change, so
  // rewriting the entire panel every 2s — filmstrip included, even when
  // byte-identical — kept the polite queue full forever. The filmstrip
  // stays outside the region entirely, and an unchanged status is left
  // untouched. The fragment's data-state also drives the header's status
  // pill, so a watch that stops or errors underneath an open page is
  // reflected there too, not only on the next full reload.
  var liveStatus = document.getElementById("live-status");
  var liveStrip = document.getElementById("live-strip");
  var pill = document.getElementById("status-pill");
  var statusDetail = document.getElementById("status-detail");
  var PILL_LED = { running: "led-green", error: "led-error", stopped: "led-stopped" };
  // The tab title leads with a failing or stopped state (pageTitle in
  // web.go), so it follows the pill.
  var baseTitle = document.title.replace(/^\[(error|stopped)\] /, "");
  function updateStatus(state, summary, message) {
    if (!pill || !PILL_LED[state]) return;
    pill.className = "status-pill status-" + state;
    var led = pill.querySelector(".led");
    if (led) led.className = "led " + PILL_LED[state];
    var text = pill.querySelector(".status-text");
    if (text) text.textContent = state;
    var title = (state === "error" || state === "stopped" ? "[" + state + "] " : "") + baseTitle;
    if (document.title !== title) document.title = title;
    if (statusDetail) {
      // Only the text nodes change, so an opened "Technical detail" stays
      // open across polls.
      var sum = statusDetail.querySelector(".status-summary");
      var raw = statusDetail.querySelector(".tech-raw");
      if (sum && sum.textContent !== (summary || "")) {
        sum.textContent = summary || "";
        sum.title = summary || ""; // the sentence is clamped to two lines; the title has it whole
      }
      if (raw && raw.textContent !== (message || "")) raw.textContent = message || "";
      statusDetail.hidden = state !== "error";
      syncSnapCause();
    }
    // A placeholder that was amber ("no frame from camera") turns red once
    // the watch itself reports the source down, and back.
    renderSnapResult();
  }
  var lastStatus = null, lastStrip = null;
  function poll() {
    fetch(base + "/watch/" + encodeURIComponent(name) + "/live")
      .then(function (resp) { return resp.ok ? resp.text() : null; })
      .then(function (html) {
        if (html === null) return;
        var tpl = document.createElement("template");
        tpl.innerHTML = html;
        var status = tpl.content.querySelector(".live-status");
        var strip = tpl.content.querySelector(".strip");
        // Compared as the server sent it; localised just before it lands,
        // so a real change is announced once and never re-announced by a
        // rewrite after insertion.
        var statusHTML = status ? status.innerHTML : html;
        var stripHTML = strip ? strip.outerHTML : "";
        if (statusHTML !== lastStatus) {
          if (status) {
            localizeTimes(status);
            updateAges(status);
            liveStatus.innerHTML = status.innerHTML;
          } else {
            liveStatus.innerHTML = statusHTML;
          }
          lastStatus = statusHTML;
        }
        if (stripHTML !== lastStrip) {
          liveStrip.innerHTML = stripHTML;
          lastStrip = stripHTML;
        }
        if (status) updateStatus(status.dataset.state, status.dataset.summary, status.dataset.message);
      })
      .catch(function () {});
  }
  poll();
  setInterval(poll, 2000);
  // An unchanged status is never re-swapped, so a stale badge's age is
  // brought up to date here. It is aria-hidden and only written when the
  // wording changes (at most once a minute).
  setInterval(function () { updateAges(liveStatus); }, 30000);

  // should_fix 1: the trigger fieldset used to show every field for every
  // Type at once, with no indication of which fields a given Type actually
  // reads (config.Validate and runner.Tick's own switches are the source of
  // truth this mirrors) and no explanation of what each Type does.
  var ttypeSel = form.elements["ttype"];
  var engineSel = form.elements["engine"];
  var rowPattern = document.getElementById("row-pattern");
  var rowOp = document.getElementById("row-op");
  var rowEngine = document.getElementById("row-engine");
  var rowThreshold = document.getElementById("row-threshold");
  var fsPreprocess = document.getElementById("fs-preprocess");
  var ttypeHelp = document.getElementById("ttype-help");
  var thresholdHelp = document.getElementById("threshold-help");
  var engineNote = document.getElementById("engine-note");
  // What each type does, in the words of the labels next to it. Facts
  // from trigger.go: pixel_change fires at pct >= threshold and again on
  // every reading still over it (cooldown permitting); numeric fires on
  // the crossing only and takes the number from Pattern's first group.
  var opDefaulted = false; // Compare was set to "gt" by updateTriggerFields, not the user
  var TRIGGER_HELP = {
    pixel_change: "Fires when at least Threshold percent of the region's pixels change between frames, and again each Cooldown while it stays changed.",
    ocr_match: "Fires when the text read from the region starts matching Pattern (a regular expression).",
    ocr_changed: "Fires each time the text read from the region settles on a new value.",
    numeric: "Reads a number from the region and fires when it goes above or below Threshold (set under Compare). Pattern is optional; its first capture group picks the number."
  };
  // config.go rejects a pixel_change threshold of 0, hence "above 0".
  var THRESHOLD_HELP = {
    pixel_change: "Percent of the region that must change, above 0 and up to 100.",
    numeric: "The value to compare against. Can be negative."
  };
  // Without tesseract (or rapidocr) the server locks the OCR types
  // (data-needs-tesseract / data-needs-rapidocr) for a watch on that
  // engine; the built-in seven-segment decoder runs them fine, so
  // switching Engine unlocks them here without a round trip. The Engine
  // row also stays visible for a pixel_change watch on a box without
  // tesseract, and whenever the selected engine is missing (a rapidocr
  // watch on a box without rapidocr) — with every OCR type locked it's the
  // only way to reach them at all. The selected type is never locked (see
  // the template): a text type on an engine that can't run it is instead
  // shown as blocked (.needs-engine on both selects, the engine note in
  // its warn state), and the server refuses to save that change.
  var tesseractPresent = !engineSel || engineSel.dataset.tesseract !== "0";
  var rapidPresent = !engineSel || engineSel.dataset.rapidocr !== "0";
  // engineMissing names the external engine the selected Engine needs but
  // the box lacks, or "" when the selection can run.
  function engineMissing() {
    var e = engineSel ? engineSel.value : "";
    if (e === "sevenseg") return "";
    if (e === "rapidocr") return rapidPresent ? "" : "rapidocr";
    return tesseractPresent ? "" : "tesseract";
  }
  // The engine note's copy, the same sentences as engineNoteFor in
  // formview.go (which renders the saved state); "" hides the note.
  var SEVENSEG_ALT = "sevenseg (the seven-segment decoder)";
  function engineNoteFor(t, missing) {
    var pixel = t === "pixel_change";
    if (missing === "") {
      return pixel ? "Not used by pixel_change. It only matters if Type becomes a text trigger." : "";
    }
    if (missing === "rapidocr") {
      return pixel
        ? "Not used by pixel_change. rapidocr isn't available on this box, so the text triggers are locked on this engine: pip install rapidocr onnxruntime, or switch Engine."
        : "rapidocr isn't available on this box (Python with the rapidocr package). Install it with pip install rapidocr onnxruntime, or switch Engine.";
    }
    if (pixel) {
      return "Not used by pixel_change. Tesseract isn't on PATH, so the text triggers are locked while Engine is tesseract: switch to " +
        (rapidPresent ? "rapidocr or " : "") + SEVENSEG_ALT + " first, or install tesseract.";
    }
    return "Tesseract isn't on PATH, so this trigger can't run. Switch Engine to " +
      (rapidPresent ? "rapidocr for printed text or " : "") + SEVENSEG_ALT + " for digit displays, or install tesseract.";
  }
  // Everything the Engine and Type selection decide together: which Type
  // options are locked and how they read, whether the selection is
  // blocked, whether the Engine row shows, and what its note says.
  function syncEngineUI() {
    if (!ttypeSel) return;
    var missing = engineMissing();
    var t = ttypeSel.value;
    var pixel = t === "pixel_change";
    Array.prototype.forEach.call(ttypeSel.options, function (o) {
      if (o.dataset.needsTesseract !== "1" && o.dataset.needsRapidocr !== "1") return;
      // The selected option is never disabled: a disabled selected option is
      // left out of the form, and the save would carry no ttype at all. The
      // lock suffix goes on the others, so the closed select never shows it.
      var lock = missing !== "" && o.value !== t;
      o.disabled = lock;
      o.textContent = (o.dataset.label || o.value) + (lock ? " (needs " + missing + ")" : "");
    });
    var blocked = missing !== "" && !pixel;
    ttypeSel.classList.toggle("needs-engine", blocked);
    if (engineSel) engineSel.classList.toggle("needs-engine", blocked);
    if (rowEngine) rowEngine.hidden = pixel && tesseractPresent && missing === "";
    if (engineNote) {
      var text = engineNoteFor(t, missing);
      engineNote.textContent = text;
      // A refused save says the same thing under Type (err-ttype) until
      // the selection moves; the warn note doesn't repeat it meanwhile.
      engineNote.hidden = text === "" || !!(rowEngine && rowEngine.hidden) || (blocked && !!document.getElementById("err-ttype"));
      engineNote.classList.toggle("is-warn", blocked);
    }
  }
  // The server's objection to a submitted Type/Engine pair (err-ttype)
  // stands while that pair does. Once either select moves it is retired
  // and the live engine note takes over, so the form never explains a
  // selection the user has already left, in red, under a different row.
  function retireTypeError() {
    var err = document.getElementById("err-ttype");
    if (!err) return;
    var row = err.parentNode;
    row.removeChild(err);
    row.classList.remove("field-invalid");
    ttypeSel.removeAttribute("aria-invalid");
    ttypeSel.setAttribute("aria-describedby", (ttypeSel.getAttribute("aria-describedby") || "").replace(/\berr-ttype\b\s*/, "").trim());
  }
  function updateTriggerFields() {
    if (!ttypeSel) return;
    var t = ttypeSel.value;
    if (rowPattern) rowPattern.hidden = !(t === "ocr_match" || t === "numeric");
    if (rowOp) rowOp.hidden = t !== "numeric";
    if (rowThreshold) rowThreshold.hidden = !(t === "pixel_change" || t === "numeric");
    if (fsPreprocess) fsPreprocess.hidden = t === "pixel_change";
    if (ttypeHelp) ttypeHelp.textContent = TRIGGER_HELP[t] || "";
    if (thresholdHelp) thresholdHelp.textContent = THRESHOLD_HELP[t] || "";
    // Compare: the validator only takes gt or lt for numeric, so a type
    // switched to numeric starts on "above" rather than on a choice that
    // always fails; required only while the row shows (a required control
    // in a hidden row would block the submit with nothing to focus). A
    // default the user never touched goes back to blank when the type
    // leaves numeric, so a look at numeric and back leaves the form clean.
    var opSel = form.elements["op"];
    if (opSel) {
      if (t === "numeric" && opSel.value === "") { opSel.value = "gt"; opDefaulted = true; }
      else if (t !== "numeric" && opDefaulted && opSel.value === "gt") { opSel.value = ""; opDefaulted = false; }
      opSel.required = t === "numeric";
    }
  }
  if (ttypeSel) {
    ttypeSel.addEventListener("change", function () { retireTypeError(); updateTriggerFields(); syncEngineUI(); });
  }
  if (engineSel) {
    engineSel.addEventListener("change", function () { retireTypeError(); syncEngineUI(); });
  }
  if (form.elements["op"]) {
    form.elements["op"].addEventListener("change", function () { opDefaulted = false; });
  }

  // Save & restart: show that it's working while the POST and the restart
  // run, and don't take a second click. Disabled a tick later so the
  // submission itself isn't cancelled. resyncFromForm (below) gives the
  // button back on every pageshow, so a page restored from the
  // back/forward cache never keeps it stuck.
  var saveBtn = form.querySelector("button[type=submit]");
  var saveLabel = saveBtn ? saveBtn.textContent : "";
  function restoreSaveBtn() {
    if (!saveBtn) return;
    saveBtn.textContent = saveLabel;
    saveBtn.removeAttribute("aria-busy");
    saveBtn.disabled = false;
  }
  if (saveBtn) {
    form.addEventListener("submit", function (e) {
      if (e.defaultPrevented) return; // a bad manual region field stopped it (manualGate)
      saveBtn.textContent = "Saving…";
      saveBtn.setAttribute("aria-busy", "true");
      setTimeout(function () { saveBtn.disabled = true; }, 0);
    });
  }
  // A rejected save lands with the error summary focused (autofocus), so
  // it is read out; jump links in it move focus to the field they name.
  var formErrors = document.getElementById("form-errors");
  if (formErrors) {
    if (document.activeElement !== formErrors) formErrors.focus();
    formErrors.addEventListener("click", function (e) {
      var a = e.target.closest("a[href^='#']");
      var target = a && document.getElementById(a.getAttribute("href").slice(1));
      if (!target) return;
      e.preventDefault();
      target.scrollIntoView({ block: "center" });
      if (target.focus) target.focus({ preventScroll: true });
    });
  }

  // Binarize: 0 is "off", and an off slider's thumb is unlit (.is-off)
  // rather than the same accent as Save and the running LED.
  var range = form.elements["pp_threshold"];
  var out = document.getElementById("ppt-val");
  function syncRange() {
    if (!range || !out) return;
    var off = range.value === "0";
    out.textContent = off ? "off" : range.value;
    range.classList.toggle("is-off", off);
    out.classList.toggle("is-off", off);
  }
  if (range && out) range.addEventListener("input", syncRange);
  // The folded Preprocess section's readout ("off", "grayscale · binarize
  // 128 · 2×"): the server renders it (preprocessSummary in web.go) and
  // this keeps it in step with the controls, so a closed section still
  // says what it holds. Same wording as the Go side.
  var ppSummaryEl = document.getElementById("pp-summary");
  function ppSummary() {
    if (!ppSummaryEl) return;
    var p = [];
    var gray = form.elements["pp_grayscale"], inv = form.elements["pp_invert"], up = form.elements["pp_upscale"];
    if (gray && gray.checked) p.push("grayscale");
    if (inv && inv.checked) p.push("invert");
    if (range && range.value !== "0") p.push("binarize " + range.value);
    if (up && up.value !== "0") p.push(up.value + "×");
    var text = p.length ? p.join(" · ") : "off";
    if (ppSummaryEl.textContent !== text) ppSummaryEl.textContent = text;
  }
  if (ppSummaryEl) {
    form.addEventListener("input", function (e) { if (e.target && /^pp_/.test(e.target.name || "")) ppSummary(); });
    form.addEventListener("change", function (e) { if (e.target && /^pp_/.test(e.target.name || "")) ppSummary(); });
  }

  // Everything derived from the form's controls, in one place, run now
  // and again on every pageshow: a page that comes back through Back
  // (bfcache or a fresh load) has its controls restored by the browser
  // AFTER this script ran, without change or input events, so whatever
  // was derived at script time (locks, rows, notes, the manual region
  // fields, the slider readout, the busy Save button) is stale until
  // this runs again. The region inputs are restorable text inputs (see
  // the template), so the manual fields are re-read from them, not the
  // other way round.
  function resyncFromForm() {
    updateTriggerFields();
    syncEngineUI();
    syncRange();
    syncManualFields();
    paint();
    ppSummary();
    restoreSaveBtn();
  }
  resyncFromForm();

  // Unsaved changes: the form as loaded (after the syncs above, so a
  // default Compare or a reformatted value never counts) against the form
  // now. While they differ the Save bar says so in place of the file note,
  // and leaving the page asks first. The drag and the manual fields write
  // the region inputs in code, which fires no form event, so they call
  // checkDirty themselves; a submit clears the guard. A rejected or failed
  // save comes back as a new page showing the submitted values, which
  // differ from the file by definition: there is no clean state to
  // snapshot, so that page is dirty from the start (the server renders the
  // bar that way too) until a save goes through.
  var serialize = function () { return new URLSearchParams(new FormData(form)).toString(); };
  var clean = formErrors ? null : serialize();
  var dirtyNote = document.getElementById("dirty-note");
  var saveNote = form.querySelector(".save-note");
  var submitting = false;
  function checkDirty() {
    var d = !submitting && (clean === null || serialize() !== clean);
    if (dirtyNote) dirtyNote.hidden = !d;
    if (saveNote) saveNote.hidden = d;
    return d;
  }
  form.addEventListener("input", checkDirty);
  form.addEventListener("change", checkDirty);
  form.addEventListener("submit", function (e) { if (e.defaultPrevented) return; submitting = true; checkDirty(); });
  window.addEventListener("beforeunload", function (e) {
    if (!checkDirty()) return;
    e.preventDefault();
    e.returnValue = "";
  });
  regionEdited = checkDirty;
  window.addEventListener("pageshow", function () {
    submitting = false;
    resyncFromForm();
    checkDirty();
  });
})();
