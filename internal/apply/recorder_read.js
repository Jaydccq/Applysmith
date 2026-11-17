// Reads the recorder's buffer. A function expression that is called with the
// epoch of the last read and the sequence number already received, so that
// reading changes nothing in the page. A buffer whose epoch differs (the page
// moved to another origin and started a new one) is read from the start. The
// page can change what this returns; the system keeps only sealed entries.
(function (epoch, from) {
  var out = { armed: false, epoch: "", next: 0, entries: [] };
  try {
    var raw = sessionStorage.getItem("__ajhr");
    if (!raw) return out;
    var s = JSON.parse(raw);
    out.armed = s.at === performance.timeOrigin;
    out.epoch = s.epoch; out.next = s.n;
    if (s.epoch !== epoch) from = 0;
    out.entries = s.e.filter(function (x) { return x.n > from; });
  } catch (x) {}
  return out;
})
