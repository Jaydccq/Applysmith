// The scene behind the page: a few hundred sheets of paper that the scroll
// position arranges. Scattered postings become a stream, the stream is sorted,
// the chosen sheets are stacked, and the stack is sealed. Each section of the
// page names its arrangement with data-scene.
import * as THREE from "three";

const COUNT = 260;
const SHEET_W = 1;
const SHEET_H = 1.414;
const WAX = 0xe8975c;

// A fixed pseudo-random sequence, so the scene is the same on every visit.
function seeded(seed) {
  let s = seed;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 4294967296;
  };
}

// The face of a sheet: a heading and lines of text, drawn once.
function sheetTexture() {
  const c = document.createElement("canvas");
  c.width = 256;
  c.height = 362;
  const g = c.getContext("2d");
  g.fillStyle = "#ffffff";
  g.fillRect(0, 0, c.width, c.height);
  g.fillStyle = "rgba(27, 42, 34, 0.55)";
  g.fillRect(28, 34, 110, 12);
  g.fillStyle = "rgba(27, 42, 34, 0.2)";
  const rnd = seeded(11);
  for (let y = 74; y < 320; y += 15) {
    if (rnd() < 0.14) continue;
    g.fillRect(28, y, 120 + rnd() * 80, 5);
  }
  const t = new THREE.CanvasTexture(c);
  t.colorSpace = THREE.SRGBColorSpace;
  t.anisotropy = 8;
  return t;
}

// One arrangement: where each sheet sits and how it is turned.
function arrangement(fn) {
  const pos = [];
  const quat = [];
  const e = new THREE.Euler();
  for (let i = 0; i < COUNT; i++) {
    const { p, r } = fn(i);
    pos.push(new THREE.Vector3(...p));
    quat.push(new THREE.Quaternion().setFromEuler(e.set(...r)));
  }
  return { pos, quat };
}

function arrangements(chosen, rank) {
  const rnd = seeded(5);
  const scatter = arrangement(() => ({
    p: [(rnd() - 0.5) * 13, (rnd() - 0.5) * 11, (rnd() - 0.5) * 16 - 6],
    r: [rnd() * 6.28, rnd() * 6.28, rnd() * 6.28],
  }));
  // A ribbon that winds toward the viewer.
  const stream = arrangement((i) => {
    const u = i / COUNT;
    const a = u * Math.PI * 5;
    return {
      p: [(u - 0.5) * 15, Math.sin(a) * 2.2 + (rnd() - 0.5) * 0.5, Math.cos(a) * 3 - 1],
      r: [Math.cos(a) * 0.7, -0.5 + (rnd() - 0.5) * 0.3, Math.sin(a) * 0.5],
    };
  });
  // A sorted wall; the chosen sheets step forward.
  const cols = 20;
  const wall = arrangement((i) => {
    const x = (i % cols) - (cols - 1) / 2;
    const y = Math.floor(i / cols) - (COUNT / cols - 1) / 2;
    const out = chosen[i] ? 1.6 : 0;
    return { p: [x * 1.14, -y * 1.58, -3 + out], r: [0, 0, 0] };
  });
  // The chosen sheets lie in a stack; the rest fall away into the dark.
  const stack = arrangement((i) => {
    if (!chosen[i]) {
      return { p: [(rnd() - 0.5) * 60, -14 - rnd() * 10, -26 - rnd() * 20], r: [rnd() * 6, rnd() * 6, rnd() * 6] };
    }
    return {
      p: [(rnd() - 0.5) * 0.12, rank[i] * 0.045 - 0.7, (rnd() - 0.5) * 0.12],
      r: [-Math.PI / 2, 0, (rnd() - 0.5) * 0.5],
    };
  });
  return [scatter, stream, wall, stack];
}

export function startScene(canvas) {
  let renderer;
  try {
    renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true, powerPreference: "high-performance" });
  } catch {
    canvas.remove();
    return;
  }
  const still = matchMedia("(prefers-reduced-motion: reduce)").matches;
  renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
  renderer.outputColorSpace = THREE.SRGBColorSpace;
  renderer.toneMapping = THREE.ACESFilmicToneMapping;
  renderer.toneMappingExposure = 1.05;
  renderer.shadowMap.enabled = true;
  renderer.shadowMap.type = THREE.PCFShadowMap;

  const scene = new THREE.Scene();
  scene.fog = new THREE.Fog(0x0f2a1f, 16, 46);
  const camera = new THREE.PerspectiveCamera(34, 1, 0.1, 120);

  scene.add(new THREE.HemisphereLight(0xfff3dc, 0x12301f, 1.1));
  const key = new THREE.DirectionalLight(0xffe2bd, 3.2);
  key.position.set(-6, 11, 9);
  key.castShadow = true;
  key.shadow.mapSize.set(2048, 2048);
  key.shadow.camera.left = key.shadow.camera.bottom = -14;
  key.shadow.camera.right = key.shadow.camera.top = 14;
  key.shadow.camera.far = 50;
  key.shadow.bias = -0.0006;
  key.shadow.normalBias = 0.02;
  scene.add(key);
  const rim = new THREE.DirectionalLight(0x9fd0b4, 1.4);
  rim.position.set(8, -3, -8);
  scene.add(rim);

  const rnd = seeded(3);
  const chosen = [];
  const rank = [];
  let n = 0;
  for (let i = 0; i < COUNT; i++) {
    chosen.push(rnd() < 0.16);
    rank.push(chosen[i] ? n++ : 0);
  }
  const layouts = arrangements(chosen, rank);

  const face = new THREE.MeshStandardMaterial({ map: sheetTexture(), roughness: 0.82, metalness: 0 });
  const edge = new THREE.MeshStandardMaterial({ roughness: 0.9 });
  const sheets = new THREE.InstancedMesh(
    new THREE.BoxGeometry(SHEET_W, SHEET_H, 0.014),
    [edge, edge, edge, edge, face, face],
    COUNT,
  );
  sheets.castShadow = true;
  sheets.receiveShadow = true;
  sheets.frustumCulled = false;
  const tint = new THREE.Color();
  const delay = [];
  const spin = [];
  for (let i = 0; i < COUNT; i++) {
    // Chosen sheets are warm ivory; the rest sit back in a greyer green.
    if (chosen[i]) tint.setHSL(0.11, 0.6, 0.93 + rnd() * 0.04);
    else tint.setHSL(0.27 + rnd() * 0.05, 0.16, 0.6 + rnd() * 0.16);
    sheets.setColorAt(i, tint);
    delay.push(rnd());
    spin.push(new THREE.Vector3(rnd() - 0.5, rnd() - 0.5, rnd() - 0.5).normalize());
  }
  const group = new THREE.Group();
  group.add(sheets);
  scene.add(group);

  // The wax seal that closes the stack.
  const seal = new THREE.Mesh(
    new THREE.CylinderGeometry(0.15, 0.165, 0.045, 48),
    new THREE.MeshStandardMaterial({ color: WAX, roughness: 0.35, metalness: 0.05 }),
  );
  seal.castShadow = true;
  seal.position.set(0.18, n * 0.045 - 0.66, 0.42);
  group.add(seal);

  // Where the group sits and how the camera looks at each scene:
  // x offset as a share of the visible width, group tilt and turn, camera
  // distance.
  const stages = [
    { arr: 0, x: 0.24, tilt: 0, yaw: 0, dist: 17, seal: 0 },
    { arr: 1, x: 0.24, tilt: 0, yaw: 0, dist: 17, seal: 0 },
    { arr: 2, x: 0.27, tilt: -0.45, yaw: -0.4, dist: 30, seal: 0 },
    { arr: 3, x: 0.2, tilt: 0.5, yaw: 0, dist: 6.5, seal: 0 },
    { arr: 3, x: 0.2, tilt: 0.62, yaw: 0, dist: 5.6, seal: 1 },
    { arr: 3, x: 0.3, tilt: 0.62, yaw: 0, dist: 8, seal: 1 },
    { arr: 3, x: 0.22, tilt: 0.7, yaw: 0, dist: 5.8, seal: 1 },
  ];
  const marks = [...document.querySelectorAll("[data-scene]")];

  // The scene position is the stage of the section at the middle of the
  // viewport, blended with its neighbour.
  function stageAt() {
    const mid = innerHeight / 2;
    let prev = null;
    for (const el of marks) {
      const r = el.getBoundingClientRect();
      const c = el.classList.contains("paper") ? r.top + innerHeight / 2 : r.top + r.height / 2;
      const at = { c, k: Number(el.dataset.scene) };
      if (c >= mid) {
        if (!prev) return at.k;
        return prev.k + (at.k - prev.k) * ((mid - prev.c) / (at.c - prev.c));
      }
      prev = at;
    }
    return prev.k;
  }

  const smooth = (t) => t * t * (3 - 2 * t);
  const lerp = (a, b, t) => a + (b - a) * t;
  const m = new THREE.Matrix4();
  const p = new THREE.Vector3();
  const q = new THREE.Quaternion();
  const drift = new THREE.Quaternion();
  const one = new THREE.Vector3(1, 1, 1);
  const pointer = { x: 0, y: 0 };
  let narrow = false;

  function resize() {
    const w = canvas.clientWidth;
    const h = canvas.clientHeight;
    renderer.setSize(w, h, false);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
    narrow = w < 760;
  }

  let shown = stageAt();
  function frame(ms) {
    const time = still ? 0 : ms / 1000;
    const target = stageAt();
    shown = still ? target : shown + (target - shown) * 0.08;
    const k = Math.min(stages.length - 1.0001, Math.max(0, shown));
    const a = stages[Math.floor(k)];
    const b = stages[Math.floor(k) + 1];
    const t = k - Math.floor(k);
    const A = layouts[a.arr];
    const B = layouts[b.arr];

    for (let i = 0; i < COUNT; i++) {
      // Sheets leave one arrangement a little after each other.
      const local = smooth(Math.min(1, Math.max(0, t * 1.5 - delay[i] * 0.5)));
      p.lerpVectors(A.pos[i], B.pos[i], local);
      q.slerpQuaternions(A.quat[i], B.quat[i], local);
      // Loose sheets tumble and bob; arranged ones hold still.
      const loose = (a.arr === 0 ? 1 - local : 0) + (b.arr === 0 ? local : 0) + (a.arr === 1 ? 1 - local : 0) * 0.25 + (b.arr === 1 ? local : 0) * 0.25;
      if (loose > 0) {
        drift.setFromAxisAngle(spin[i], time * (0.12 + delay[i] * 0.2) * loose);
        q.multiply(drift);
        p.y += Math.sin(time * 0.4 + delay[i] * 20) * 0.25 * loose;
      }
      m.compose(p, q, one);
      sheets.setMatrixAt(i, m);
    }
    sheets.instanceMatrix.needsUpdate = true;

    const dist = lerp(a.dist, b.dist, smooth(t)) * (narrow ? 1.5 : 1);
    const width = 2 * dist * Math.tan((camera.fov * Math.PI) / 360) * camera.aspect;
    group.position.x = narrow ? 0 : lerp(a.x, b.x, smooth(t)) * width;
    group.rotation.x = lerp(a.tilt, b.tilt, smooth(t));
    group.rotation.y = lerp(a.yaw, b.yaw, smooth(t)) + (still ? 0.3 : Math.sin(time * 0.15) * 0.25 + time * 0.02 * (a.arr === 3 ? 1 : 0)) + pointer.x * 0.12;
    const s = smooth(Math.min(1, Math.max(0, lerp(a.seal, b.seal, t) * 1.6 - 0.6)));
    seal.scale.setScalar(Math.max(0.0001, s));

    camera.position.set(pointer.x * 0.5, pointer.y * 0.35, dist);
    camera.lookAt(group.position.x * 0.2, 0, 0);
    renderer.render(scene, camera);
    if (!still) requestAnimationFrame(frame);
  }

  addEventListener("resize", () => { resize(); if (still) frame(0); });
  addEventListener("pointermove", (e) => {
    pointer.x = e.clientX / innerWidth - 0.5;
    pointer.y = 0.5 - e.clientY / innerHeight;
  });
  if (still) addEventListener("scroll", () => frame(0), { passive: true });
  resize();
  requestAnimationFrame(frame);
}
