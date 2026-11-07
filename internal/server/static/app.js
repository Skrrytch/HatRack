// Small helpers, no framework: tell the server the browser's time zone and
// reload the page when the state changes elsewhere.
(function () {
  try {
    var tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    var m = document.cookie.match(/(?:^|; )hatrack_tz=([^;]*)/);
    if (tz && (!m || decodeURIComponent(m[1]) !== tz)) {
      document.cookie = "hatrack_tz=" + encodeURIComponent(tz) + ";path=/;max-age=31536000;samesite=strict";
      if (!m && document.cookie.indexOf("hatrack_tz=") >= 0) { location.reload(); return; }
    }
  } catch (e) {}
  // durations that keep running: data-since (ms) plus data-base (ms)
  function fmt(ms) {
    var m = Math.max(0, Math.round(ms / 60000));
    if (m < 60) return m + "m";
    var r = m % 60;
    return Math.floor(m / 60) + "h " + (r < 10 ? "0" : "") + r + "m";
  }
  function tick() {
    var els = document.querySelectorAll("[data-since]");
    for (var i = 0; i < els.length; i++) {
      var s = +els[i].dataset.since;
      if (s) els[i].textContent = fmt(Date.now() - s + (+els[i].dataset.base || 0));
    }
  }
  tick();
  // opening "+ Detail" puts the cursor into its field
  document.addEventListener("toggle", function (e) {
    var d = e.target;
    if (d.classList && d.classList.contains("detailedit") && d.open) {
      var input = d.querySelector("input[name=detail]");
      if (input) { input.focus(); input.select(); }
    }
  }, true);
  // "add a program": append the choice as a new line of the list field
  document.addEventListener("change", function (e) {
    var t = e.target;
    if (!t.dataset || !t.dataset.append || !t.value) return;
    var area = document.getElementById(t.dataset.append);
    if (area) area.value = (area.value.replace(/\s+$/, "") + "\n" + t.value).replace(/^\n/, "");
    t.value = "";
  });
  // "recently seen" apps: a chip appends its app to the ignore list
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("[data-add]");
    if (!b) return;
    var area = document.getElementById("obs-ignore");
    if (!area) return;
    var app = b.dataset.add;
    var lines = area.value.split("\n").map(function (l) { return l.trim().toLowerCase(); });
    if (lines.indexOf(app.toLowerCase()) < 0) area.value = (area.value.replace(/\s+$/, "") + "\n" + app).replace(/^\n/, "");
    b.remove();
  });
  // the "Observed" band: its info shows on hover (mouse, keyboard) or tap
  // (touch); a click, or "Add entry" in the info on touch, prefills the add
  // form with the block's time and the hat booked then
  var tip = null, touch = false, booked = null;
  function hideTip() { if (tip) { tip.remove(); tip = null; } }
  function showTip(b, withButton) {
    hideTip();
    var sec = b.closest(".obs");
    tip = document.createElement("div");
    tip.className = "obstip";
    (b.dataset.tip || "").split("\n").forEach(function (l, i) {
      var d = document.createElement("div");
      d.textContent = l;
      if (i === 0) d.className = "t";
      tip.appendChild(d);
    });
    if (b.dataset.start) {
      var x;
      if (withButton) {
        x = document.createElement("button");
        x.type = "button";
        x.className = "primary";
        x.textContent = sec.dataset.addlabel;
        x.addEventListener("click", function () { hideTip(); prefill(b); });
      } else {
        x = document.createElement("div");
        x.className = "dim";
        x.textContent = sec.dataset.hint;
      }
      tip.appendChild(x);
    }
    sec.appendChild(tip);
    var r = b.getBoundingClientRect(), sr = sec.getBoundingClientRect();
    tip.style.left = Math.max(0, Math.min(r.left - sr.left, sr.width - tip.offsetWidth)) + "px";
    tip.style.top = (r.bottom - sr.top + 6) + "px";
  }
  function mins(v) {
    var m = /^(\d\d):(\d\d)/.exec(v || "");
    return m ? +m[1] * 60 + +m[2] : null;
  }
  function hm(m) { return ("0" + Math.floor(m / 60)).slice(-2) + ":" + ("0" + m % 60).slice(-2); }
  // the note under the add form: which booked entries the new one replaces
  function updateNote(form) {
    var d = form.closest("details.card.add"), note = d && d.querySelector(".overnote");
    if (!note) return;
    if (booked === null) {
      try { booked = JSON.parse(document.getElementById("booked").textContent) || []; } catch (e) { booked = []; }
    }
    var s = mins(form.elements.start.value), e = form.elements.end.value ? mins(form.elements.end.value) : 1440, out = [];
    if (s !== null && e !== null && e > s) {
      booked.forEach(function (b) {
        var lo = Math.max(s, b.s), hi = Math.min(e, b.e);
        if (lo < hi) out.push(b.t + " " + hm(lo) + "–" + hm(hi));
      });
    }
    note.hidden = !out.length;
    note.textContent = out.length ? note.dataset.prefix.replace("%s", out.join(", ")) : "";
  }
  function prefill(b) {
    var d = document.querySelector("details.card.add");
    if (!d) return;
    d.open = true;
    var f = d.querySelector("form");
    f.elements.start.value = b.dataset.start;
    f.elements.end.value = b.dataset.end;
    var what = f.elements.what;
    if (what && b.dataset.what) {
      for (var i = 0; i < what.options.length; i++) if (what.options[i].value === b.dataset.what) what.value = b.dataset.what;
    }
    updateNote(f);
    d.scrollIntoView({ block: "center", behavior: "smooth" });
    if (what) what.focus({ preventScroll: true });
  }
  function blockOf(e) { return e.target.closest && e.target.closest(".obsband [data-tip]"); }
  document.addEventListener("pointerdown", function (e) { touch = e.pointerType === "touch" || e.pointerType === "pen"; }, true);
  document.addEventListener("mouseover", function (e) { var b = blockOf(e); if (b && !touch) showTip(b, false); });
  document.addEventListener("mouseout", function (e) {
    var b = blockOf(e);
    if (b && !touch && !(e.relatedTarget && b.contains(e.relatedTarget))) hideTip();
  });
  document.addEventListener("focusin", function (e) { var b = blockOf(e); if (b) showTip(b, false); });
  document.addEventListener("focusout", function (e) { if (blockOf(e)) hideTip(); });
  document.addEventListener("click", function (e) {
    if (tip && tip.contains(e.target)) return;
    var b = blockOf(e);
    if (!b) { hideTip(); return; }
    if (touch) { showTip(b, true); return; }
    if (b.dataset.start) { hideTip(); prefill(b); }
  });
  document.addEventListener("input", function (e) {
    var t = e.target;
    if (t.form && (t.name === "start" || t.name === "end")) updateNote(t.form);
  });
  // picking a wallpaper from the gallery fills in its path
  document.addEventListener("change", function (e) {
    var t = e.target;
    if (!t.dataset || !t.dataset.target) return;
    var input = document.getElementById(t.dataset.target);
    if (input) input.value = t.value;
    var g = t.closest(".gallery");
    if (g) g.querySelectorAll(".thumb").forEach(function (l) { l.classList.toggle("on", l.contains(t)); });
  });
  // colour swatches: pick a palette colour and bring its wallpapers forward
  function suggestWallpapers(name, fill) {
    var re = new RegExp("^hatrack:wp\\d+-" + name + "$");
    document.querySelectorAll(".gallery").forEach(function (g) {
      var thumbs = Array.prototype.slice.call(g.querySelectorAll(".thumb"));
      var hits = thumbs.filter(function (l) { return name && re.test(l.dataset.value || ""); });
      thumbs.forEach(function (l) { l.classList.remove("suggest"); });
      for (var i = hits.length - 1; i >= 0; i--) { hits[i].classList.add("suggest"); g.insertBefore(hits[i], g.firstChild); }
      if (!fill || !hits.length) return;
      var radio = hits[0].querySelector("input");
      var input = radio && document.getElementById(radio.dataset.target);
      if (input && !input.value) {
        radio.checked = true;
        radio.dispatchEvent(new Event("change", { bubbles: true }));
        var d = g.closest("details");
        if (d) d.open = true;
      }
    });
  }
  function markSwatches(form, hex) {
    var name = "";
    form.querySelectorAll(".swatch").forEach(function (b) {
      var on = b.dataset.color.toLowerCase() === hex.toLowerCase();
      b.classList.toggle("on", on);
      if (on) name = b.dataset.name;
    });
    var page = form.closest(".hatpage");
    if (page) page.style.setProperty("--c", hex);
    return name;
  }
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest(".swatch");
    if (!b) return;
    var form = b.closest("form");
    var input = form && form.querySelector("input[name=color]");
    if (input) input.value = b.dataset.color;
    suggestWallpapers(markSwatches(form, b.dataset.color), true);
  });
  document.addEventListener("input", function (e) {
    var t = e.target;
    if (t.name !== "color" || t.type !== "color") return;
    var form = t.closest("form");
    if (form) suggestWallpapers(markSwatches(form, t.value), false);
  });
  var cur = document.querySelector(".hatpage .swatch.on");
  if (cur) suggestWallpapers(cur.dataset.name, false);
  setInterval(tick, 20000);
  if (!document.body.dataset.live || !window.EventSource) return;
  var reconnected = false, first = true;
  var es = new EventSource("/ui/stream");
  es.onerror = function () { reconnected = true; first = true; };
  es.addEventListener("state", function () {
    if (first) { first = false; if (!reconnected) return; }
    // do not throw away an edit in progress
    if (document.querySelector("details.iv[open], details.add[open], details.detailedit[open]")) return;
    location.reload();
  });
})();
