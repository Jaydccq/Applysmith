import "./style.css";
import mark from "../../web/src/assets/career-cat.webp";
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

startScene(document.getElementById("scene"));
