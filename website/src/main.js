import "./style.css";
import mark from "../../web/src/assets/career-cat.webp";
import inbox from "../../docs/user-guide/images/tutorial-04-inbox.webp";
import report from "../../docs/user-guide/images/tutorial-05-report.webp";
import form from "../../docs/user-guide/images/tutorial-06-sample-form-filled.webp";
import runs from "../../docs/user-guide/images/tutorial-07-runs-board.webp";
import { startScene } from "./scene.js";

document.getElementById("brand-mark").src = mark;

// The navigation switches to dark ink while the paper section is under it.
const nav = document.querySelector(".nav");
const paper = document.querySelector(".paper");
const onScroll = () => {
  const r = paper.getBoundingClientRect();
  nav.classList.toggle("on-paper", r.top < 40 && r.bottom > 40);
};
addEventListener("scroll", onScroll, { passive: true });
onScroll();

const copy = document.getElementById("copy");
copy.addEventListener("click", async () => {
  await navigator.clipboard.writeText(document.getElementById("demo-command").textContent);
  copy.textContent = "Copied";
  setTimeout(() => { copy.textContent = "Copy"; }, 1600);
});

// The screens: one is shown at a time, chosen by its tab or the arrow keys.
const shots = { inbox, report, form, runs };
for (const img of document.querySelectorAll("[data-shot]")) img.src = shots[img.dataset.shot];
const tabs = [...document.querySelectorAll(".tour [role=tab]")];
const show = (tab) => {
  for (const t of tabs) {
    const on = t === tab;
    t.setAttribute("aria-selected", on);
    t.tabIndex = on ? 0 : -1;
    document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
  }
};
tabs.forEach((tab, i) => {
  tab.addEventListener("click", () => show(tab));
  tab.addEventListener("keydown", (e) => {
    const step = { ArrowDown: 1, ArrowRight: 1, ArrowUp: -1, ArrowLeft: -1 }[e.key];
    if (!step) return;
    e.preventDefault();
    const next = tabs[(i + step + tabs.length) % tabs.length];
    show(next);
    next.focus();
  });
});
show(tabs[0]);

// The site film is not tracked in git (video/README.md): the page shows it
// only when the build found the file in video/dist.
const film = import.meta.glob("../../video/dist/site-film.{mp4,jpg}", { eager: true, query: "?url", import: "default" });
if (film["../../video/dist/site-film.mp4"]) {
  const video = document.getElementById("film-video");
  video.poster = film["../../video/dist/site-film.jpg"] ?? "";
  video.src = film["../../video/dist/site-film.mp4"];
  document.getElementById("film").hidden = false;
}

startScene(document.getElementById("scene"));
