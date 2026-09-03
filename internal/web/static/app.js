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
  function drawRect() {
    var ctx = canvas.getContext("2d");
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    var r = region();
    if (!(r.w > 0) || !(r.h > 0)) return;
    ctx.strokeStyle = "#7fd4a8";
    ctx.lineWidth = 2;
    ctx.setLineDash([6, 4]);
    ctx.strokeRect(r.x * canvas.width, r.y * canvas.height,
                   r.w * canvas.width, r.h * canvas.height);
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
  if (img.complete) sizeCanvas();

  document.getElementById("testbtn").addEventListener("click", function () {
    var el = document.getElementById("test-result");
    el.innerHTML = "<p class='muted'>testing…</p>";
    fetch("/watch/" + encodeURIComponent(name) + "/test", {
      method: "POST",
      body: new URLSearchParams(new FormData(form))
    }).then(function (resp) { return resp.text(); })
      .then(function (html) { el.innerHTML = html; })
      .catch(function (err) { el.innerHTML = "<p class='conf-low'>test failed: " + err + "</p>"; });
  });

  var live = document.getElementById("live");
  function poll() {
    fetch("/watch/" + encodeURIComponent(name) + "/live")
      .then(function (resp) { return resp.ok ? resp.text() : null; })
      .then(function (html) { if (html !== null) live.innerHTML = html; })
      .catch(function () {});
  }
  poll();
  setInterval(poll, 2000);

  var range = form.elements["pp_threshold"];
  var out = document.getElementById("ppt-val");
  if (range && out) {
    var sync = function () { out.textContent = range.value === "0" ? "off" : range.value; };
    range.addEventListener("input", sync);
    sync();
  }
})();
