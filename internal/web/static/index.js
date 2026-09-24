// watchglass watch-list glue: keeps the rows current, asks before a delete
// in a styled dialog, shows the add form working, and jumps to it from the
// header. No dependencies. The page works without it (plain links and
// forms, with an inline confirm() on delete).
(function () {
  var table = document.getElementById("watch-rows");
  var emptyState = document.querySelector(".empty-state[data-src]");
  var addForm = document.querySelector(".form-inline-add");
  var POLL_HEADER = "X-Watchglass-Poll"; // web.go pollHeader
  var createBtn = addForm && addForm.querySelector("button[type=submit]");
  var createLabel = createBtn ? createBtn.textContent : "";
  var demoForm = document.querySelector(".demo-offer");
  var demoBtn = demoForm && demoForm.querySelector("button[type=submit]");
  var demoLabel = demoBtn ? demoBtn.textContent : "";

  // ---- Add form, and the empty state's demo offer: busy while the POST runs ----
  function busyOnSubmit(form, btn, text) {
    form.addEventListener("submit", function () {
      btn.textContent = text;
      btn.setAttribute("aria-busy", "true");
      // A tick later, so disabling it doesn't cancel the submission.
      setTimeout(function () { btn.disabled = true; }, 0);
    });
  }
  function unbusy(btn, label) {
    btn.textContent = label;
    btn.removeAttribute("aria-busy");
    btn.disabled = false;
  }
  if (createBtn) busyOnSubmit(addForm, createBtn, "Creating…");
  if (demoBtn) busyOnSubmit(demoForm, demoBtn, "Adding…");

  // Back to a page the browser kept in its bfcache: the buttons still
  // carry the busy state they were submitted with. Restore them, then
  // refresh the rows at once rather than after the next poll period, so a
  // row deleted on the way out doesn't linger with a disabled Delete.
  window.addEventListener("pageshow", function (e) {
    if (!e.persisted) return;
    if (createBtn) unbusy(createBtn, createLabel);
    if (demoBtn) unbusy(demoBtn, demoLabel);
    if (table) {
      Array.prototype.forEach.call(table.querySelectorAll(".delete-form button"), function (b) {
        b.disabled = false;
        b.firstChild.textContent = "Delete";
      });
    }
    poll();
  });

  // "Add watch" in the header: land in the Name field, not on the heading.
  var jump = document.querySelector("[data-jump-add]");
  var nameInput = document.getElementById("new-name");
  if (jump && nameInput) {
    jump.addEventListener("click", function (e) {
      e.preventDefault();
      var reduce = window.matchMedia && matchMedia("(prefers-reduced-motion: reduce)").matches;
      nameInput.scrollIntoView({ block: "center", behavior: reduce ? "auto" : "smooth" });
      nameInput.focus({ preventScroll: true });
    });
  }

  // ---- Delete: a styled confirmation instead of the native confirm() ----
  var dialog = table && document.getElementById("confirm-delete");
  var haveDialog = !!(dialog && typeof dialog.showModal === "function");
  var pending = null; // the delete form waiting on the dialog
  // The inline confirm() is only the fallback for when this script didn't
  // load; with the dialog it would ask twice. Every row that enters the
  // table goes through here: the ones rendered with the page and the ones
  // the poller adds or swaps in.
  function adoptRow(row) {
    if (!haveDialog) return;
    var f = row.querySelector(".delete-form");
    if (f) f.removeAttribute("onsubmit");
  }
  if (haveDialog) {
    var nameSlot = dialog.querySelector("[data-confirm-name]");
    table.addEventListener("submit", function (e) {
      var f = e.target.closest(".delete-form");
      if (!f) return;
      e.preventDefault();
      var row = f.closest("tr");
      pending = f;
      nameSlot.textContent = row ? row.getAttribute("data-name") : "";
      dialog.returnValue = "";
      dialog.showModal();
    }, true);
    Array.prototype.forEach.call(table.tBodies[0].rows, adoptRow);
    dialog.addEventListener("close", function () {
      var f = pending;
      pending = null;
      if (!f) return;
      if (dialog.returnValue === "delete") {
        var b = f.querySelector("button");
        b.disabled = true;
        b.firstChild.textContent = "Deleting…";
        f.submit(); // bypasses the submit listener
      } else {
        var btn = f.querySelector("button");
        if (btn && document.contains(btn)) btn.focus();
      }
    });
    // A click on the backdrop (outside the dialog box) cancels.
    dialog.addEventListener("click", function (e) {
      if (e.target !== dialog) return;
      var r = dialog.getBoundingClientRect();
      if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) {
        dialog.close("cancel");
      }
    });
  }

  // ---- Row = link, for the parts the stretched name link can't cover ----
  // The error sentence and "No digits read" sit above the row link so
  // their tooltips still show; a plain click on them follows the row's
  // link like a click anywhere else in the row would. A modifier click
  // (new tab) or a text selection is left alone.
  if (table) {
    table.classList.add("js-rows");
    table.addEventListener("click", function (e) {
      if (e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return;
      var hit = e.target.closest(".cell-reading [title]");
      if (!hit || !table.contains(hit)) return;
      var sel = window.getSelection && getSelection();
      if (sel && !sel.isCollapsed && hit.contains(sel.anchorNode)) return;
      var row = hit.closest("tr");
      var link = row && row.querySelector(".cell-name a");
      if (link) link.click();
    });
  }

  // ---- Live rows: re-fetch this page and patch what changed ----
  // Only the cells that carry state are swapped (never the name link or
  // the delete form), so focus, a hover and an open dialog survive. No
  // live region: six readings changing every few seconds would drown a
  // screen reader; the detail page's Live panel is the place for that.
  // The empty page polls too, so a watch created elsewhere shows up.
  var src = (table || emptyState) && (table || emptyState).getAttribute("data-src");
  var PERIOD = 5000;
  var STATE_CELLS = "td:not(.cell-name):not(.col-actions)";
  var stale = document.querySelector(".index-stale");
  var chip = document.querySelector("[data-count]");
  var failures = 0, inflight = false;

  function addFormDirty() {
    if (!addForm) return false;
    if (addForm.contains(document.activeElement)) return true;
    return Array.prototype.some.call(addForm.querySelectorAll("input"), function (i) { return i.value !== ""; });
  }
  function setStale(on) {
    if (table) table.classList.toggle("is-stale", on);
    if (stale) stale.hidden = !on;
  }
  function setChip(text) {
    if (!chip) return;
    chip.hidden = text === "";
    if (chip.textContent !== text) chip.textContent = text;
  }
  function patchRow(oldRow, newRow) {
    var oc = oldRow.querySelectorAll(STATE_CELLS), nc = newRow.querySelectorAll(STATE_CELLS);
    if (oc.length !== nc.length) {
      var fresh = document.importNode(newRow, true);
      adoptRow(fresh);
      oldRow.replaceWith(fresh);
      return;
    }
    for (var i = 0; i < oc.length; i++) {
      if (oc[i].innerHTML !== nc[i].innerHTML) oc[i].innerHTML = nc[i].innerHTML;
    }
  }
  // The last watch went away in another tab while the add form is in use:
  // a reload would lose the typing, so the list is emptied in place and the
  // reload waits for the form to be clear (the next poll does it).
  function showNoneLeft() {
    var tbody = table.tBodies[0];
    if (tbody.querySelector(".row-empty")) return;
    Array.prototype.forEach.call(Array.prototype.slice.call(tbody.rows), function (r) {
      if (pending && r.contains(pending)) return;
      r.remove();
    });
    setChip("");
    if (tbody.rows.length) return; // its dialog is open; it goes on close
    var tr = document.createElement("tr");
    tr.className = "row-empty";
    tr.setAttribute("role", "row");
    var td = document.createElement("td");
    td.setAttribute("role", "cell");
    td.colSpan = 5;
    td.textContent = "No watches left. This list will refresh when the form below is clear.";
    tr.appendChild(td);
    tbody.appendChild(tr);
  }
  function apply(html) {
    var doc = new DOMParser().parseFromString(html, "text/html");
    var fresh = doc.getElementById("watch-rows");
    if (!table) {
      // The empty page: the first watch arrived from elsewhere.
      if (fresh && !addFormDirty()) location.reload();
      return;
    }
    if (!fresh) {
      if (!addFormDirty()) location.reload();
      else showNoneLeft();
      return;
    }
    var tbody = table.tBodies[0];
    var none = tbody.querySelector(".row-empty");
    if (none) none.remove();
    var byName = {};
    Array.prototype.forEach.call(tbody.rows, function (r) { byName[r.getAttribute("data-name")] = r; });
    var prev = null;
    Array.prototype.forEach.call(fresh.tBodies[0].rows, function (nr) {
      var name = nr.getAttribute("data-name");
      var row = byName[name];
      if (row) {
        delete byName[name];
        patchRow(row, nr);
        row = tbody.querySelector("tr[data-name='" + CSS.escape(name) + "']");
      } else {
        row = document.importNode(nr, true);
        adoptRow(row);
      }
      // Compare with the next element row, not the next node: the template
      // leaves whitespace text between rows, and moving a row that is
      // already in place would drop the focus it holds.
      var want = prev ? prev.nextElementSibling : tbody.rows[0];
      if (row !== want) tbody.insertBefore(row, want || null);
      prev = row;
    });
    Object.keys(byName).forEach(function (n) {
      var r = byName[n];
      if (pending && r.contains(pending)) return; // its dialog is open
      r.remove();
    });
    var freshChip = doc.querySelector("[data-count]");
    if (freshChip) setChip(freshChip.textContent);
  }
  function poll() {
    if (!src || document.hidden || inflight) return;
    inflight = true;
    var headers = { Accept: "text/html" };
    headers[POLL_HEADER] = "1";
    fetch(src, { credentials: "same-origin", cache: "no-store", headers: headers })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.text(); })
      .then(function (html) {
        failures = 0;
        setStale(false);
        apply(html);
      })
      .catch(function () {
        failures++;
        if (failures >= 3) setStale(true);
      })
      .then(function () { inflight = false; });
  }
  if (src) {
    setInterval(poll, PERIOD);
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden) poll();
    });
  }
})();
