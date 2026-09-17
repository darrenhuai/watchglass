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
    canvas.hidden = false;
    // A same-size reload (the periodic snapshot refresh) only needs the
    // rectangle repainted. Resizing goes through drawRect, which also
    // rewrites the manual region fields — and would clobber a value the
    // user is mid-keystroke in.
    if (canvas.width === img.clientWidth && canvas.height === img.clientHeight) {
      paint();
      return;
    }
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
  // repaints — never calls syncManualFields. A field that is empty (or
  // holds a partial entry the browser reports as "") leaves its hidden
  // counterpart alone rather than zeroing it: one keystroke in X must not
  // silently rewrite Y/W/H.
  function applyManualFields() {
    if (mx.value !== "") fx.value = mx.value;
    if (my.value !== "") fy.value = my.value;
    if (mw.value !== "") fw.value = mw.value;
    if (mh.value !== "") fh.value = mh.value;
    paint();
  }
  if (mx) {
    [mx, my, mw, mh].forEach(function (el) {
      el.addEventListener("input", applyManualFields);
    });
    // Populate from the configured region right away, not only from the
    // image-load path (drawRect): when the camera is down the image never
    // loads, and manual entry is then the ONLY way to edit the region — it
    // must start from the real values, not from blanks.
    syncManualFields();
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
  // A touch drag the browser turns into a scroll ends in pointercancel, not
  // pointerup; without this the stale drag would pause the snapshot refresh.
  canvas.addEventListener("pointercancel", function () { drag = null; });
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
  function showSnapError() {
    img.hidden = true;
    snapCause = null;
    // The never-sized canvas (300x150 by default) would otherwise sit on top
    // of the placeholder, catching a text-select drag across the error
    // message as a region drag. sizeCanvas unhides it once an image loads.
    canvas.hidden = true;
    if (!snapError) return;
    snapError.hidden = false;
    snapError.textContent = "";
    snapError.appendChild(el("strong", null, "No image from the camera"));
    snapError.appendChild(el("p", "snap-cause", "Checking why…"));
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
          return "The camera answered a retry but failed again. It may be overloaded; retrying automatically.";
        }
        var ct = resp.headers.get("Content-Type") || "";
        if (ct.indexOf("text/") !== 0) {
          discard();
          return "watchglass answered HTTP " + resp.status;
        }
        return resp.text();
      })
      .then(function (text) {
        if (text === null) return;
        snapError.textContent = "";
        snapError.appendChild(el("strong", null, "No image from the camera"));
        snapCause = splitError(text);
        snapCauseHeader = null;
        syncSnapCause();
      })
      .catch(function () {
        snapError.textContent = "";
        snapError.appendChild(el("strong", null, "No image from the camera"));
        snapError.appendChild(el("p", "snap-cause", "watchglass didn't answer. Is it still running?"));
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
  document.getElementById("testbtn").addEventListener("click", function () {
    var box = document.getElementById("test-result");
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
      if (r.ok && r.html) { box.innerHTML = r.text; return; }
      renderTestError(box, r.text.trim() || "watchglass answered HTTP " + r.status + ".");
    }).catch(function () {
      renderTestError(box, "watchglass didn't answer. Is it still running?");
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
      if (sum && sum.textContent !== (summary || "")) sum.textContent = summary || "";
      if (raw && raw.textContent !== (message || "")) raw.textContent = message || "";
      statusDetail.hidden = state !== "error";
      syncSnapCause();
    }
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
        var statusHTML = status ? status.innerHTML : html;
        var stripHTML = strip ? strip.outerHTML : "";
        if (statusHTML !== lastStatus) {
          liveStatus.innerHTML = statusHTML;
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

  // should_fix 1: the trigger fieldset used to show every field for every
  // Type at once, with no indication of which fields a given Type actually
  // reads (config.Validate and runner.Tick's own switches are the source of
  // truth this mirrors) and no explanation of what each Type does.
  var ttypeSel = form.elements["ttype"];
  var engineSel = form.elements["engine"];
  var rowPattern = document.getElementById("row-pattern");
  var rowOp = document.getElementById("row-op");
  var rowEngine = document.getElementById("row-engine");
  var fsPreprocess = document.getElementById("fs-preprocess");
  var ttypeHelp = document.getElementById("ttype-help");
  var TRIGGER_HELP = {
    pixel_change: "Fires when at least Threshold% of the region's pixels change between readings.",
    ocr_match: "Fires when the region's recognized text matches the Pattern regex.",
    ocr_changed: "Fires whenever the region's recognized text changes to a new stable value.",
    numeric: "Extracts a number from the region's text (Pattern, optional) and fires when it crosses Threshold using Op."
  };
  // Without tesseract (or rapidocr) the server locks the OCR types
  // (data-needs-tesseract / data-needs-rapidocr) for a watch on that
  // engine; the built-in seven-segment decoder runs them fine, so
  // switching Engine unlocks them here without a round trip. The Engine
  // row also stays visible for a pixel_change watch on a box without
  // tesseract, and whenever the selected engine is missing (a rapidocr
  // watch on a box without rapidocr) — with every OCR type locked it's the
  // only way to reach them at all. The saved type is never locked (see the
  // template).
  var tesseractPresent = !engineSel || engineSel.dataset.tesseract !== "0";
  var rapidPresent = !engineSel || engineSel.dataset.rapidocr !== "0";
  var savedType = ttypeSel ? ttypeSel.value : "";
  // engineMissing names the external engine the selected Engine needs but
  // the box lacks, or "" when the selection can run.
  function engineMissing() {
    var e = engineSel ? engineSel.value : "";
    if (e === "sevenseg") return "";
    if (e === "rapidocr") return rapidPresent ? "" : "rapidocr";
    return tesseractPresent ? "" : "tesseract";
  }
  function syncTypeOptions() {
    if (!ttypeSel) return;
    var missing = engineMissing();
    Array.prototype.forEach.call(ttypeSel.options, function (o) {
      if (o.dataset.needsTesseract !== "1" && o.dataset.needsRapidocr !== "1") return;
      // The selected option is never disabled: a disabled selected option is
      // left out of the form, and the save would carry no ttype at all.
      o.disabled = missing !== "" && o.value !== ttypeSel.value;
      o.textContent = o.value + (missing ? " (needs " + missing + ")" : "");
    });
  }
  function updateTriggerFields() {
    if (!ttypeSel) return;
    var t = ttypeSel.value;
    if (rowPattern) rowPattern.hidden = !(t === "ocr_match" || t === "numeric");
    if (rowOp) rowOp.hidden = t !== "numeric";
    if (rowEngine) rowEngine.hidden = t === "pixel_change" && tesseractPresent && engineMissing() === "";
    if (fsPreprocess) fsPreprocess.hidden = t === "pixel_change";
    if (ttypeHelp) ttypeHelp.textContent = TRIGGER_HELP[t] || "";
  }
  if (ttypeSel) {
    ttypeSel.addEventListener("change", updateTriggerFields);
    updateTriggerFields();
  }
  if (engineSel) {
    engineSel.addEventListener("change", syncTypeOptions);
    syncTypeOptions();
  }

  // Save & restart: show that it's working while the POST and the restart
  // run, and don't take a second click. Disabled a tick later so the
  // submission itself isn't cancelled. A page restored from the
  // back/forward cache (Back from an error page) gets the button back.
  var saveBtn = form.querySelector("button[type=submit]");
  if (saveBtn) {
    var saveLabel = saveBtn.textContent;
    form.addEventListener("submit", function () {
      saveBtn.textContent = "Saving…";
      saveBtn.setAttribute("aria-busy", "true");
      setTimeout(function () { saveBtn.disabled = true; }, 0);
    });
    window.addEventListener("pageshow", function (e) {
      if (!e.persisted) return;
      saveBtn.textContent = saveLabel;
      saveBtn.removeAttribute("aria-busy");
      saveBtn.disabled = false;
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

  var range = form.elements["pp_threshold"];
  var out = document.getElementById("ppt-val");
  if (range && out) {
    var sync = function () { out.textContent = range.value === "0" ? "off" : range.value; };
    range.addEventListener("input", sync);
    sync();
  }
})();
