// Builds the scenes of one film and exposes seek(t): every frame is a pure
// function of t, so a renderer can ask for frames in any order.
(() => {
  const film = FILMS[new URLSearchParams(location.search).get("film")] || { scenes: [] };
  const stage = document.getElementById("stage");
  const bar = document.querySelector("#progress i");
  const FADE_IN = 0.45, FADE_OUT = 0.35, RISE = 0.6, TYPE_RATE = 30;
  const CAT = "../web/src/assets/career-cat.webp";
  const ICON = "../app/icons/icon.png";

  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };
  // enter marks an element to rise in `at` seconds after its scene starts.
  const enter = (e, at) => { e.dataset.at = at; return e; };

  function head(s, from = 0.15) {
    const h = el("div", "head");
    const top = el("div");
    if (s.kicker) top.append(enter(el("div", "kicker", s.kicker), from));
    top.append(enter(el("h2", "", s.title), from + 0.12));
    h.append(top);
    if (s.text) h.append(enter(el("p", "lede", s.text), from + 0.3));
    return h;
  }

  function brand() {
    const b = el("div", "brand");
    const img = el("img");
    img.src = CAT;
    b.append(img, "Applysmith");
    return b;
  }

  const build = {
    title(s, root) {
      root.classList.add("title");
      if (s.icon) {
        const img = enter(el("img", "icon"), 0.1);
        img.src = ICON;
        root.append(img);
      }
      if (s.kicker) root.append(enter(el("div", "kicker", s.kicker), 0.25));
      root.append(enter(el("h1", s.small ? "small" : "", s.title), 0.35));
      if (s.text) root.append(enter(el("p", "lede", s.text), 0.7));
      if (s.cmd) root.append(enter(el("div", "chip", s.cmd), 1.1));
    },
    cards(s, root) {
      root.append(head(s), brand());
      const grid = el("div", "cards");
      grid.style.gridTemplateColumns = `repeat(${s.cols}, 1fr)`;
      s.cards.forEach((c, i) => {
        const card = enter(el("div", s.cards.length > 4 ? "card tight" : "card"), 0.8 + i * (s.stagger || 0.35));
        if (c.n) card.append(el("div", "n", c.n));
        card.append(el("h3", "", c.h), el("p", "", c.p));
        grid.append(card);
      });
      root.append(grid);
    },
    shot(s, root) {
      root.classList.add("shot");
      root.append(head(s));
      const win = enter(el("div", "window"), 0.35);
      const top = el("div", "bar");
      top.append(el("b"), el("b"), el("b"), el("span", "", "127.0.0.1:47319" + s.path));
      const view = el("div", "view");
      const img = el("img");
      img.src = `shots/${s.img}.webp`;
      view.append(img);
      win.append(top, view);
      root.append(win);
      // The capture is 1440 CSS pixels wide and shown 1500 wide.
      const k = 1500 / 1440;
      const [y0, y1] = s.scroll;
      return (local, dur) => {
        const p = Math.min(1, Math.max(0, (local - 1) / (dur - 2)));
        const smooth = p * p * (3 - 2 * p);
        img.style.transform = `translateY(${-(y0 + (y1 - y0) * smooth) * k}px)`;
      };
    },
    terminal(s, root) {
      root.append(head(s), brand());
      const term = enter(el("div", "term"), 0.5);
      const top = el("div", "bar");
      top.append(el("b"), el("b"), el("b"));
      const pre = el("pre");
      term.append(top, pre);
      root.append(term);
      // Each command is typed, then its output appears line by line.
      const lines = [];
      let at = 1.3;
      for (const step of s.steps) {
        lines.push({ cls: "c", text: step.cmd, at, typed: true });
        at += step.cmd.length / TYPE_RATE + 0.4;
        for (const out of step.out || []) {
          lines.push({ cls: "o", text: out, at });
          at += 0.1;
        }
        at += 0.7;
      }
      return (local) => {
        pre.replaceChildren();
        for (const line of lines) {
          if (local < line.at) break;
          const row = el("div", line.cls);
          const shown = line.typed ? Math.floor((local - line.at) * TYPE_RATE) : line.text.length;
          row.textContent = line.text.slice(0, shown) || "";
          if (line.typed && shown < line.text.length) row.append(el("span", "caret"));
          pre.append(row);
        }
      };
    },
    steps(s, root) {
      root.append(head(s), brand());
      const grid = el("div", "steps");
      s.steps.forEach((name, i) => {
        const optional = name.endsWith("?");
        const row = enter(el("div", optional ? "step optional" : "step"), 0.9 + i * 0.22);
        row.append(el("span", "n", String(i)), optional ? name.slice(0, -1) : name);
        if (optional) row.append(el("em", "", "optional"));
        grid.append(row);
      });
      root.append(grid);
    },
  };

  const ease = (p) => 1 - Math.pow(1 - p, 3);

  let start = 0;
  const scenes = film.scenes.map((s) => {
    const root = el("section", "scene");
    stage.append(root);
    const update = build[s.kind](s, root);
    const scene = { root, start, dur: s.dur, update, rising: [...root.querySelectorAll("[data-at]")] };
    start += s.dur;
    return scene;
  });
  const total = start;

  window.seek = (t) => {
    for (const sc of scenes) {
      const local = t - sc.start;
      const on = local >= 0 && local < sc.dur;
      sc.root.style.visibility = on ? "visible" : "hidden";
      if (!on) continue;
      sc.root.style.opacity = Math.min(1, local / FADE_IN, (sc.dur - local) / FADE_OUT);
      for (const e of sc.rising) {
        const p = ease(Math.min(1, Math.max(0, (local - Number(e.dataset.at)) / RISE)));
        e.style.opacity = p;
        e.style.transform = `translateY(${(1 - p) * 26}px)`;
      }
      if (sc.update) sc.update(local, sc.dur);
    }
    bar.style.width = `${(100 * t) / total}%`;
    return true;
  };

  // Resolves to the length of the film once fonts and images are loaded.
  window.ready = Promise.all([
    document.fonts.ready,
    ...[...document.images].map((img) => img.decode()),
  ]).then(() => { seek(0); return total; });
})();
