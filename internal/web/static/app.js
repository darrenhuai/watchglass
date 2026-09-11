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

  function sizeCanvas() {
    canvas.width = img.clientWidth;
    canvas.height = img.clientHeight;
    drawRect();
  }
  function region() {
    return {
      x: parseFloat(fx.value) || 0, y: parseFloat(fy.value) || 0,
      w: parseFloat(fw.value) || 0, h: parseFloat(fh.value) || 0
    };
  }
  // should_fix 7: keyboard/switch/screen-reader users can't drive the
  // pointerdown/pointermove drag below at all, so "Set region manually"
  // (four number inputs revealed by a <details>) is the only other input
  // path to fx/fy/fw/fh. Both paths funnel through region()/paint(), so
  // whichever one last touched the hidden fields stays authoritative.
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
  function syncManualFields() {
    if (!mx) return;
    var r = region();
    mx.value = r.x.toFixed(4);
    my.value = r.y.toFixed(4);
    mw.value = r.w.toFixed(4);
    mh.value = r.h.toFixed(4);
  }
  function paint() {
    var ctx = canvas.getContext("2d");
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    var r = region();
    if (!(r.w > 0) || !(r.h > 0)) return;
    var rx = r.x * canvas.width, ry = r.y * canvas.height,
        rw = r.w * canvas.width, rh = r.h * canvas.height;
    ctx.fillStyle = "rgba(127, 212, 168, 0.12)";
    ctx.fillRect(rx, ry, rw, rh);
    ctx.strokeStyle = "#7fd4a8";
    ctx.lineWidth = 2;
    ctx.setLineDash([]);
    ctx.strokeRect(rx, ry, rw, rh);
    var hs = 6;
    ctx.fillStyle = "#7fd4a8";
    [[rx, ry], [rx + rw, ry], [rx, ry + rh], [rx + rw, ry + rh]].forEach(function (c) {
      ctx.fillRect(c[0] - hs / 2, c[1] - hs / 2, hs, hs);
    });
  }
  // drawRect is the drag/load path: it syncs the manual fields to match
  // (they aren't the source of the change) and repaints.
  function drawRect() {
    syncManualFields();
    paint();
  }
  // applyManualFields is the manual-entry path: the manual fields ARE the
  // source of the change and must be left exactly as typed, so this only
  // repaints — never calls syncManualFields.
  function applyManualFields() {
    fx.value = mx.value || "0";
    fy.value = my.value || "0";
    fw.value = mw.value || "0";
    fh.value = mh.value || "0";
    paint();
  }
  if (mx) {
    [mx, my, mw, mh].forEach(function (el) {
      el.addEventListener("input", applyManualFields);
    });
  }
  var drag = null;
  canvas.addEventListener("pointerdown", function (e) {
    var b = canvas.getBoundingClientRect();
    var cx = Math.min(Math.max((e.clientX - b.left) / b.width, 0), 1);
    var cy = Math.min(Math.max((e.clientY - b.top) / b.height, 0), 1);
    drag = { x0: cx, y0: cy };
    canvas.setPointerCapture(e.pointerId);
  });
  canvas.addEventListener("pointermove", function (e) {
    if (!drag) return;
    var b = canvas.getBoundingClientRect();
    var x1 = Math.min(Math.max((e.clientX - b.left) / b.width, 0), 1);
    var y1 = Math.min(Math.max((e.clientY - b.top) / b.height, 0), 1);
    fx.value = Math.min(drag.x0, x1).toFixed(4);
    fy.value = Math.min(drag.y0, y1).toFixed(4);
    fw.value = Math.abs(x1 - drag.x0).toFixed(4);
    fh.value = Math.abs(y1 - drag.y0).toFixed(4);
    drawRect();
  });
  canvas.addEventListener("pointerup", function () { drag = null; });
  img.addEventListener("load", sizeCanvas);
  window.addEventListener("resize", sizeCanvas);

  // must_fix 2: the <img> has no way to show the /snapshot endpoint's own
  // 502 body — the browser just renders its generic broken-image glyph, no
  // branding, no explanation. On error, re-fetch the same URL (the <img>
  // request itself never exposes a failed response's body) purely to read
  // that text, then swap in an in-app placeholder showing it.
  var snapError = document.getElementById("snap-error");
  function showSnapError() {
    img.hidden = true;
    if (!snapError) return;
    snapError.hidden = false;
    snapError.textContent = "camera unreachable — checking why…";
    fetch(base + "/watch/" + encodeURIComponent(name) + "/snapshot")
      .then(function (resp) { return resp.text(); })
      .then(function (text) {
        snapError.textContent = "";
        var strong = document.createElement("strong");
        strong.textContent = "camera unreachable";
        var detail = document.createElement("div");
        detail.className = "mono";
        detail.textContent = text || "no further detail available";
        snapError.appendChild(strong);
        snapError.appendChild(detail);
      })
      .catch(function () { snapError.textContent = "camera unreachable"; });
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

  document.getElementById("testbtn").addEventListener("click", function () {
    var el = document.getElementById("test-result");
    el.innerHTML = "<p class='muted'>testing…</p>";
    fetch(base + "/watch/" + encodeURIComponent(name) + "/test", {
      method: "POST",
      body: new URLSearchParams(new FormData(form))
    }).then(function (resp) { return resp.text(); })
      .then(function (html) { el.innerHTML = html; })
      .catch(function (err) { el.innerHTML = "<p class='conf-low'>test failed: " + err + "</p>"; });
  });

  var live = document.getElementById("live");
  function poll() {
    fetch(base + "/watch/" + encodeURIComponent(name) + "/live")
      .then(function (resp) { return resp.ok ? resp.text() : null; })
      .then(function (html) { if (html !== null) live.innerHTML = html; })
      .catch(function () {});
  }
  poll();
  setInterval(poll, 2000);

  // should_fix 1: the trigger fieldset used to show every field for every
  // Type at once, with no indication of which fields a given Type actually
  // reads (config.Validate and runner.Tick's own switches are the source of
  // truth this mirrors) and no explanation of what each Type does.
  var ttypeSel = form.elements["ttype"];
  var rowPattern = document.getElementById("row-pattern");
  var rowOp = document.getElementById("row-op");
  var fsPreprocess = document.getElementById("fs-preprocess");
  var ttypeHelp = document.getElementById("ttype-help");
  var TRIGGER_HELP = {
    pixel_change: "Fires when at least Threshold% of the region's pixels change between readings.",
    ocr_match: "Fires when the region's recognized text matches the Pattern regex.",
    ocr_changed: "Fires whenever the region's recognized text changes to a new stable value.",
    numeric: "Extracts a number from the region's text (Pattern, optional) and fires when it crosses Threshold using Op."
  };
  function updateTriggerFields() {
    if (!ttypeSel) return;
    var t = ttypeSel.value;
    if (rowPattern) rowPattern.hidden = !(t === "ocr_match" || t === "numeric");
    if (rowOp) rowOp.hidden = t !== "numeric";
    if (fsPreprocess) fsPreprocess.hidden = t === "pixel_change";
    if (ttypeHelp) ttypeHelp.textContent = TRIGGER_HELP[t] || "";
  }
  if (ttypeSel) {
    ttypeSel.addEventListener("change", updateTriggerFields);
    updateTriggerFields();
  }

  var range = form.elements["pp_threshold"];
  var out = document.getElementById("ppt-val");
  if (range && out) {
    var sync = function () { out.textContent = range.value === "0" ? "off" : range.value; };
    range.addEventListener("input", sync);
    sync();
  }
})();
