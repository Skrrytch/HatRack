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
  // picking a wallpaper from the gallery fills in its path
  document.addEventListener("change", function (e) {
    var t = e.target;
    if (!t.dataset || !t.dataset.target) return;
    var input = document.getElementById(t.dataset.target);
    if (input) input.value = t.value;
    var g = t.closest(".gallery");
    if (g) g.querySelectorAll(".thumb").forEach(function (l) { l.classList.toggle("on", l.contains(t)); });
  });
  setInterval(tick, 20000);
  if (!document.body.dataset.live || !window.EventSource) return;
  var reconnected = false, first = true;
  var es = new EventSource("/ui/stream");
  es.onerror = function () { reconnected = true; first = true; };
  es.addEventListener("state", function () {
    if (first) { first = false; if (!reconnected) return; }
    // do not throw away an edit in progress
    if (document.querySelector("details.iv[open], details.add[open]")) return;
    location.reload();
  });
})();
