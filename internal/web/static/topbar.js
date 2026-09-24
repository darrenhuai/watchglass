// watchglass topbar: keeps the Home Assistant (MQTT) line current, so the
// broker going away or coming back shows without a reload. Loaded only
// when config.yaml has an mqtt: block. No dependencies; without it the
// line is still right as of the page load.
(function () {
  var line = document.getElementById("ha-status");
  if (!line || !window.fetch || !window.DOMParser) return;
  var src = line.getAttribute("data-src");
  var PERIOD = 5000;
  var inflight = false;

  // The element stays (it is a role=status live region, so screen readers
  // hear a real change); only its class, tooltip and words are swapped,
  // and only when they differ.
  function apply(html) {
    var fresh = new DOMParser().parseFromString(html, "text/html").getElementById("ha-status");
    if (!fresh) return;
    if (line.className !== fresh.className) line.className = fresh.className;
    if (line.title !== fresh.title) line.title = fresh.title;
    if (line.innerHTML !== fresh.innerHTML) line.innerHTML = fresh.innerHTML;
  }
  var timer = null;
  // While the first attempt is still out ("connecting…") the answer is a
  // second or two away, so it is asked for sooner.
  function schedule() {
    clearTimeout(timer);
    timer = setTimeout(poll, line.classList.contains("is-connecting") ? 1500 : PERIOD);
  }
  function poll() {
    if (inflight) return;
    if (document.hidden) { schedule(); return; }
    inflight = true;
    fetch(src, { credentials: "same-origin", cache: "no-store", headers: { Accept: "text/html" } })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.text(); })
      .then(apply, function () { /* watchglass itself is away; the page says so elsewhere */ })
      .then(function () { inflight = false; schedule(); });
  }
  schedule();
  document.addEventListener("visibilitychange", function () {
    if (!document.hidden) poll();
  });
})();
