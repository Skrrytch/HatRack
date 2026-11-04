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
  if (!document.body.dataset.live || !window.EventSource) return;
  var reconnected = false, first = true;
  var es = new EventSource("/ui/stream");
  es.onerror = function () { reconnected = true; first = true; };
  es.addEventListener("state", function () {
    if (first) { first = false; if (!reconnected) return; }
    location.reload();
  });
})();
