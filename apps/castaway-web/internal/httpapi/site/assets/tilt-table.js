{ // own block: page scripts share one global scope
// Tilt Table, top-down. Tilt the phone to roll three balls, one at a time, into three holes. Grab a ball from
// the rack and set it on the mat; a ball that rolls off the table drops into the bamboo gutter and has to be
// tilted back down to the return at the bottom, then grabbed and set on the mat again (a rollback).
// Physics and drawing follow Inside Track (inside-track.js): 240 Hz fixed step, one sun, flat matte shapes.
const TABLE = { x0: -0.55, x1: 0.55, y0: 0, y1: 1.5 };
const GW = 0.15, OUT = { x0: TABLE.x0 - GW, x1: TABLE.x1 + GW, y0: 0, y1: TABLE.y1 + GW }; // gutter round three sides
const BOARD = { x0: OUT.x0 - 0.04, x1: OUT.x1 + 0.04, y0: -0.27, y1: OUT.y1 + 0.04 };
const R = 0.05, HOLE_R = 0.074, RAIL = 0.018;
const PAD = { x0: -0.18, x1: 0.18, y0: 0.03, y1: 0.29 };
const RETURN_Y = 0.36;                   // gutter below this is the return: a ball there can be grabbed
const RACK = [-0.13, 0, 0.13].map((x) => [x, -0.135]);
const COLORS = ["#c8372d", "#e8b923", "#2f5fa8"];
const HOLES = [[-0.33, 0.64], [0.33, 1.0], [0, 1.33]];
const RAILS = [[-0.02, 0.46, 0.4, 0.46], [-0.4, 0.82, 0.06, 0.82], [0.1, 1.16, 0.42, 1.16]];
const DROP = 0.07, G = 9.8, ROLL_G = 7;  // (5/7)·g: gravity along a slope for a rolling ball
const MAX_TILT = 22, DEAD = 1.2, GAIN = 0.75; // phone degrees → table tilt
const ROLL = (v) => 0.12 + 0.05 * v;     // rolling resistance on the table
const GUTTER_ROLL = (v) => 0.2 + 0.08 * v;
const WALL_E = 0.35, MAX_V = 3, SKIM = 0.9, CAPTURE_V = 1.25, GRAB = 0.12;
const PRACTICE_S = 180, DEG = Math.PI / 180;
const DEBUG = location.search.includes("debug");
// Scored runs: the server puts data-action on <body>, and data-resume (holes, rollbacks, aces, elapsed ms, and
// whether the ball in play already fell) when the player already started. The clock is the server's.
const DATA = document.body.dataset, ACTION = DATA.action || "", RESUME = DATA.resume === "1";

const $ = (id) => document.getElementById(id);
const canvas = $("c"), ctx = canvas.getContext("2d");
let scale = 1, cx = 0, cy = 0, W = 0, H = 0, bg = null;
function resize() {
  const d = devicePixelRatio || 1, top = 46, bottom = 70;
  W = innerWidth; H = innerHeight; canvas.width = W * d; canvas.height = H * d;
  scale = Math.min(W / (BOARD.x1 - BOARD.x0), (H - top - bottom) / (BOARD.y1 - BOARD.y0)) * 0.97;
  cx = W / 2 - ((BOARD.x0 + BOARD.x1) / 2) * scale;
  cy = top + (H - top - bottom) / 2 + ((BOARD.y0 + BOARD.y1) / 2) * scale;
  ctx.setTransform(d, 0, 0, d, 0, 0); bg = null;
}
addEventListener("resize", resize);
const sx = (x) => cx + x * scale, sy = (y) => cy - y * scale;
const toBoard = (e) => ({ x: (e.clientX - cx) / scale, y: (cy - e.clientY) / scale });
const inRect = (x, y, r, m = 0) => x >= r.x0 + m && x <= r.x1 - m && y >= r.y0 + m && y <= r.y1 - m;
const speed = (b) => Math.hypot(b.vx, b.vy);
const clamp = (v, a, b) => Math.max(a, Math.min(b, v));

// ---- tilt: deviceorientation (beta front/back, gamma left/right) relative to a calibrated "level" ----
let raw = null, cal = { b: 0, g: 0 }, tilt = { x: 0, y: 0 }, fake = null, sensed = false;
addEventListener("deviceorientation", (e) => {
  if (e.beta == null || e.gamma == null) return;
  raw = { b: e.beta, g: e.gamma }; sensed = true;
});
function level() { if (raw) cal = { ...raw }; }
function screenAngle() { return ((screen.orientation?.angle ?? window.orientation ?? 0) + 360) % 360; }
function readTilt() { // degrees of downhill on the table, x right and y up the screen
  if (fake) return fake;
  if (!raw) return { x: 0, y: 0 };
  const dz = (v) => (Math.abs(v) < DEAD ? 0 : v - Math.sign(v) * DEAD);
  const dx = dz(raw.g - cal.g), dy = -dz(raw.b - cal.b); // raise the top edge and the ball rolls toward you
  switch (screenAngle()) {
    case 90: return { x: -dy, y: dx };
    case 180: return { x: -dx, y: -dy };
    case 270: return { x: dy, y: -dx };
    default: return { x: dx, y: dy };
  }
}
const slope = (deg) => Math.sin(clamp(deg * GAIN, -MAX_TILT, MAX_TILT) * DEG);

// ---- game state ----
// Ball: { x, y, vx, vy, z (height: table 0, gutter −DROP), vz, level: rack|held|table|air|gutter|hole, spin, ang, falls, practice }
let balls, active, holes, phase = "intro", rollbacks, aces, runStart = 0, runEnd = 0, practiceEnd = 0, held = null, finishedAt = 0;
function setup(practice) {
  holes = [null, null, null]; active = null; held = null; rollbacks = 0; aces = 0;
  balls = RACK.map(([x, y], i) => ({ x, y, vx: 0, vy: 0, z: -0.02, vz: 0, level: "rack", slot: i, spin: 0, ang: 0, falls: 0, practice }));
  hud();
}
function startPractice() {
  phase = "practice"; setup(true); practiceEnd = performance.now() + PRACTICE_S * 1000;
  $("practicing").classList.remove("hidden"); $("ready").classList.remove("hidden"); pop("Practice!");
}
function startCountdown() {
  phase = "countdown"; $("practicing").classList.add("hidden"); $("ready").classList.add("hidden");
  setup(false); let n = 3;
  const el = $("count"); el.classList.remove("hidden"); el.textContent = n;
  const tick = setInterval(() => {
    n--;
    if (n > 0) { el.textContent = n; return; }
    clearInterval(tick); el.classList.add("hidden");
    phase = "run"; runStart = performance.now(); pop("Go!"); report();
  }, 800);
}
const filled = () => holes.filter(Boolean).length;
function fmt(ms) { const s = Math.max(0, ms) / 1000; return `${Math.floor(s / 60)}:${(s % 60).toFixed(1).padStart(4, "0")}`; }
function hud() {
  $("in").textContent = `${phase === "run" || phase === "done" ? filled() : 0}/3`;
  $("rollbacks").textContent = phase === "run" || phase === "done" ? rollbacks : 0;
}
function pop(text) { const el = $("pop"); el.textContent = text; el.classList.remove("go"); void el.offsetWidth; el.classList.add("go"); }

function resumeRun() { // a reload mid-run: filled holes stay filled and the clock kept going
  phase = "run"; setup(false);
  holes.forEach((_, i) => {
    if (!(+DATA.holes & (1 << i))) return;
    const b = balls.find((x) => x.level === "rack"), [hx, hy] = HOLES[i];
    holes[i] = Object.assign(b, { level: "hole", x: hx, y: hy, hx, hy, sink: 1 });
  });
  rollbacks = +DATA.rollbacks || 0; aces = +DATA.aces || 0;
  const next = balls.find((x) => x.level === "rack"); if (next) next.falls = +DATA.fell || 0;
  runStart = performance.now() - (+DATA.elapsed || 0); hud(); pop("Clock's running!");
}

// Scored runs: progress at "Go" (which starts the server's clock), after every rollback and every sink.
function report() {
  if (!ACTION || phase === "practice") return Promise.resolve(null);
  const mask = holes.reduce((m, b, i) => (b ? m | (1 << i) : m), 0);
  return fetch(ACTION + "/progress", { method: "POST", keepalive: true, headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ holes_mask: mask, rollbacks }) }).then((r) => (r.ok ? r.json() : null)).catch(() => null);
}

// ---- input: grab a ball from the rack, the mat, or the return, and set it on the mat ----
let target = null, off = { x: 0, y: 0 };
function grabbable(b) {
  if (b.level === "rack") return !active;
  if (b !== active) return false;
  if (b.level === "table") return inRect(b.x, b.y, PAD, -R * 0.5);
  return b.level === "gutter" && b.y < RETURN_Y;
}
canvas.addEventListener("pointerdown", (e) => {
  if (!(phase === "practice" || phase === "run") || held) return;
  const p = toBoard(e);
  const b = balls.filter(grabbable).sort((a, c) => Math.hypot(a.x - p.x, a.y - p.y) - Math.hypot(c.x - p.x, c.y - p.y))[0];
  if (!b || Math.hypot(b.x - p.x, b.y - p.y) > GRAB) return;
  canvas.setPointerCapture(e.pointerId);
  held = b; held.from = { level: b.level, x: b.x, y: b.y }; b.level = "held"; b.vx = b.vy = 0; b.z = 0.08;
  off = { x: b.x - p.x, y: b.y - p.y }; target = { x: b.x, y: b.y }; active = b;
  navigator.vibrate?.(10);
});
canvas.addEventListener("pointermove", (e) => { if (held) { const p = toBoard(e); target = { x: p.x + off.x, y: p.y + off.y }; } });
function letGo() {
  if (!held) return;
  const b = held; held = null;
  if (inRect(b.x, b.y, PAD, -R * 0.6)) { // on the mat: set it down there
    Object.assign(b, { x: clamp(b.x, PAD.x0 + R * 0.4, PAD.x1 - R * 0.4), y: clamp(b.y, PAD.y0 + R * 0.4, PAD.y1 - R * 0.4), level: "table", z: 0, vx: 0, vy: 0 });
    return;
  }
  Object.assign(b, b.from, { vx: 0, vy: 0, z: b.from.level === "gutter" ? -DROP : b.from.level === "rack" ? -0.02 : 0 }); // anywhere else: it goes back
  if (b.level === "rack") active = null;
}
canvas.addEventListener("pointerup", letGo);
canvas.addEventListener("pointercancel", letGo);

// ---- physics (fixed 240 Hz) ----
function segment(b, [ax, ay, bx, by], rad) { // a rail of radius rad; Inside Track's wall()
  const ex = bx - ax, ey = by - ay, t = clamp(((b.x - ax) * ex + (b.y - ay) * ey) / (ex * ex + ey * ey), 0, 1);
  const px = ax + ex * t, py = ay + ey * t, dx = b.x - px, dy = b.y - py, d = Math.hypot(dx, dy), min = R + rad;
  if (d >= min || d === 0) return;
  const nx = dx / d, ny = dy / d, vn = b.vx * nx + b.vy * ny;
  b.x += nx * (min - d); b.y += ny * (min - d);
  if (vn < 0) { b.vx -= (1 + WALL_E) * vn * nx; b.vy -= (1 + WALL_E) * vn * ny; if (-vn > 0.6) navigator.vibrate?.(12); }
}
function box(b, r, inside) { // keep a ball inside rect r, or out of it
  if (inside) {
    if (b.x < r.x0 + R) { b.x = r.x0 + R; if (b.vx < 0) b.vx *= -WALL_E; }
    if (b.x > r.x1 - R) { b.x = r.x1 - R; if (b.vx > 0) b.vx *= -WALL_E; }
    if (b.y < r.y0 + R) { b.y = r.y0 + R; if (b.vy < 0) b.vy *= -WALL_E; }
    if (b.y > r.y1 - R) { b.y = r.y1 - R; if (b.vy > 0) b.vy *= -WALL_E; }
    return;
  }
  const px = clamp(b.x, r.x0, r.x1), py = clamp(b.y, r.y0, r.y1); // nearest point of the rect
  let dx = b.x - px, dy = b.y - py, d = Math.hypot(dx, dy);
  if (d >= R) return;
  if (d === 0) { // centre inside: out through the nearest side
    const opts = [[r.x0 - b.x, 0], [r.x1 - b.x, 0], [0, r.y1 - b.y]].sort((a, c) => Math.hypot(...a) - Math.hypot(...c))[0];
    b.x += opts[0]; b.y += opts[1]; dx = Math.sign(opts[0]); dy = Math.sign(opts[1]); d = 0;
  }
  const n = Math.hypot(dx, dy) || 1, nx = dx / n, ny = dy / n, vn = b.vx * nx + b.vy * ny;
  b.x += nx * (R - d); b.y += ny * (R - d);
  if (vn < 0) { b.vx -= (1 + WALL_E) * vn * nx; b.vy -= (1 + WALL_E) * vn * ny; }
}
function roll(b, decel, dt) {
  const s = speed(b), ns = Math.max(0, s - decel * dt);
  if (ns === 0) { b.vx = b.vy = 0; } else { b.vx *= ns / s; b.vy *= ns / s; }
  if (ns > MAX_V) { b.vx *= MAX_V / ns; b.vy *= MAX_V / ns; }
  if (ns > 0.02) { b.spin += ns * dt / R; b.ang = Math.atan2(b.vy, b.vx); }
}
function sink(b, i) {
  holes[i] = b; const [hx, hy] = HOLES[i];
  Object.assign(b, { level: "hole", hx, hy, sink: 0, vx: 0, vy: 0 });
  active = null; navigator.vibrate?.([30, 40, 30]);
  if (phase === "run") {
    const ace = b.falls === 0; if (ace) aces++;
    pop(ace ? "Ace!" : "In!"); hud();
    if (filled() === 3) { phase = "done"; runEnd = performance.now(); finish(report()); } else report();
  } else {
    pop(b.falls === 0 ? "Ace!" : "In!");
    if (filled() === 3) setTimeout(() => { if (phase === "practice") { setup(true); pop("Again!"); } }, 1200);
  }
}
function fall(b) { // off the edge of the table and down into the gutter
  b.level = "air"; b.vz = 0; b.vx *= 0.7; b.vy *= 0.7; b.falls++; navigator.vibrate?.(40);
  if (phase === "run") { rollbacks++; hud(); report(); }
  pop("Rollback!");
}
function step(dt) {
  const t = tilt, ax = ROLL_G * slope(t.x), ay = ROLL_G * slope(t.y);
  for (const b of balls) {
    if (b.level === "hole") { if (b.sink < 1) { b.sink = Math.min(1, b.sink + dt / 0.18); b.x += (b.hx - b.x) * 0.25; b.y += (b.hy - b.y) * 0.25; } continue; }
    if (b.level === "rack" || b.level === "held") continue;
    b.vx += ax * dt; b.vy += ay * dt;
    if (b.level === "table") {
      // Over an empty hole the ball rests on the rim, its centre lower the nearer it is to the middle, and
      // rolls in once it is past the rim; a fast ball skips across (Inside Track's lip, a wider hole).
      for (let i = 0; i < 3; i++) {
        if (holes[i]) continue;
        const [hx, hy] = HOLES[i], dx = hx - b.x, dy = hy - b.y, d = Math.hypot(dx, dy);
        if (d >= HOLE_R) continue;
        if (speed(b) > SKIM) { b.vx *= 1 - 0.6 * dt; b.vy *= 1 - 0.6 * dt; continue; } // too fast to drop: it skips across
        const q = Math.min(HOLE_R - d, R * 0.97), pull = ROLL_G * q / Math.sqrt(R * R - q * q);
        b.vx += (dx / (d || 1)) * pull * dt; b.vy += (dy / (d || 1)) * pull * dt;
        b.vx *= 1 - 3 * dt; b.vy *= 1 - 3 * dt; // knocking on the rim
        if (d < HOLE_R - R * 0.8 && speed(b) < CAPTURE_V) { sink(b, i); break; }
      }
      if (b.level !== "table") continue;
      roll(b, ROLL(speed(b)), dt);
    } else if (b.level === "air") {
      b.vz -= G * dt; b.z += b.vz * dt;
      if (b.z <= -DROP) { b.z = -DROP; b.vz = 0; b.level = "gutter"; navigator.vibrate?.(20); }
    } else if (b.level === "gutter") roll(b, GUTTER_ROLL(speed(b)), dt);
    b.x += b.vx * dt; b.y += b.vy * dt;
    if (b.level === "table") {
      RAILS.forEach((r) => segment(b, r, RAIL));
      if (b.y < TABLE.y0 + R) { b.y = TABLE.y0 + R; if (b.vy < 0) b.vy *= -WALL_E; } // the near edge has a rail
      if (b.x < TABLE.x0 || b.x > TABLE.x1 || b.y > TABLE.y1) fall(b);
    } else { // gutter and air: the table is a block in the middle of the gutter's frame
      box(b, OUT, true);
      if (b.level === "gutter" || b.z < -R) box(b, TABLE, false);
    }
  }
}

function finish(saved) {
  const time = runEnd - runStart;
  hud(); $("time").textContent = fmt(time);
  let official = null; // the server's clock is the one that counts
  saved.then((res) => { if (res?.time) { official = res.time; $("final").textContent = $("time").textContent = res.time; } });
  setTimeout(() => {
    $("final").textContent = official ?? fmt(time); $("summary").textContent = "";
    const row = $("stamps"); row.textContent = "";
    const stamps = [["Done +1", ""], ...Array.from({ length: aces }, () => ["Ace +1", "ace"])];
    $("done").classList.remove("hidden");
    stamps.forEach(([txt, kind], i) => setTimeout(() => {
      const el = document.createElement("span"); el.className = `stamp ${kind}`; el.textContent = txt; row.append(el);
      $("done").querySelector(".card").animate([{ transform: "translate(0,0)" }, { transform: "translate(-3px,3px)" }, { transform: "translate(2px,-1px)" }, { transform: "translate(0,0)" }], { duration: 180 });
      navigator.vibrate?.(25);
    }, 400 + i * 420));
    setTimeout(() => {
      $("summary").textContent = `${rollbacks} ${rollbacks === 1 ? "rollback" : "rollbacks"} · ${aces} ${aces === 1 ? "ace" : "aces"}` +
        (ACTION ? ". Fastest +3 and second +2 when it closes." : ". Free play: not scored.");
    }, 600 + stamps.length * 420);
  }, 900);
}

// ---- drawing: flat, matte, one light from the top left ----
const INK = "#2b2418";
const SUN = { x: 0.55, y: 0.75 }; // shadow offset per unit of height, down-right on screen
const rnd = ((seed) => () => ((seed = (seed * 16807) % 2147483647) / 2147483647))(51);
const LEAVES = Array.from({ length: 26 }, () => ({ x: rnd(), y: rnd(), a: rnd() * 6.3, s: 0.6 + rnd() * 0.8, k: rnd() < 0.5 ? 0 : 1 }));
const DAPPLE = Array.from({ length: 9 }, () => ({ x: rnd(), y: rnd(), r: 40 + rnd() * 70, p: rnd() * 6 }));
function leaf(g, x, y, a, s, color, vein) { // a broad jungle leaf: two curves and a midrib
  g.save(); g.translate(x, y); g.rotate(a); g.scale(s, s);
  g.fillStyle = color; g.beginPath(); g.moveTo(0, 0); g.bezierCurveTo(18, -26, 62, -24, 84, 0); g.bezierCurveTo(62, 24, 18, 26, 0, 0); g.fill();
  g.strokeStyle = vein; g.lineWidth = 2; g.beginPath(); g.moveTo(2, 0); g.lineTo(80, 0); g.stroke();
  g.lineWidth = 1.2; for (let k = 1; k < 6; k++) { g.beginPath(); g.moveTo(k * 13, 0); g.lineTo(k * 13 + 9, -14 + k); g.moveTo(k * 13, 0); g.lineTo(k * 13 + 9, 14 - k); g.stroke(); }
  g.restore();
}
function jungle() { // the jungle floor, drawn once per size
  if (bg) return bg;
  const d = devicePixelRatio || 1, c = document.createElement("canvas"); c.width = W * d; c.height = H * d;
  const g = c.getContext("2d"); g.setTransform(d, 0, 0, d, 0, 0);
  g.fillStyle = "#2c4a2b"; g.fillRect(0, 0, W, H);
  for (let i = 0; i < W * H / 60; i++) { g.fillStyle = rnd() < 0.5 ? "rgba(15,30,12,.18)" : "rgba(140,170,90,.10)"; g.fillRect(rnd() * W, rnd() * H, 2, 2); }
  g.fillStyle = "rgba(92,64,34,.35)"; g.beginPath(); g.ellipse(W * 0.5, H * 0.62, W * 0.62, H * 0.5, 0, 0, 7); g.fill(); // trodden mud under the table
  LEAVES.forEach((l) => leaf(g, l.x * W, l.y * H, l.a, l.s, l.k ? "#3f6b34" : "#335c2f", "rgba(20,40,15,.45)"));
  bg = c; return c;
}
function canopy(time) { // leaf shadows sway over everything; sun patches drift
  ctx.save(); ctx.globalCompositeOperation = "soft-light";
  DAPPLE.forEach((p) => {
    const x = p.x * W + Math.sin(time * 0.3 + p.p) * 8, y = p.y * H + Math.cos(time * 0.25 + p.p) * 6;
    const g = ctx.createRadialGradient(x, y, 0, x, y, p.r); g.addColorStop(0, "rgba(255,240,180,.55)"); g.addColorStop(1, "rgba(255,240,180,0)");
    ctx.fillStyle = g; ctx.beginPath(); ctx.arc(x, y, p.r, 0, 7); ctx.fill();
  });
  ctx.restore();
  ctx.save(); ctx.globalAlpha = 0.16;
  [[-0.05, 0.08, 2.2, 1.6], [1.02, 0.45, 3.9, 1.9], [0.08, 0.95, 5.1, 1.4], [0.95, 0.9, 0.7, 1.7]].forEach(([x, y, a, s], i) =>
    leaf(ctx, x * W, y * H, a + Math.sin(time * 0.6 + i) * 0.04, s, "#0d1a0b", "rgba(0,0,0,0)"));
  ctx.restore();
}
const rect = (r, x0 = r.x0, x1 = r.x1, y0 = r.y0, y1 = r.y1) => ctx.rect(sx(x0), sy(y1), (x1 - x0) * scale, (y1 - y0) * scale);
function bamboo(x0, y0, x1, y1, w, color = "#c9b25e") { // a bamboo pole with node rings
  const len = Math.hypot(x1 - x0, y1 - y0);
  ctx.save(); ctx.translate(x0, y0); ctx.rotate(Math.atan2(y1 - y0, x1 - x0));
  ctx.fillStyle = color; ctx.strokeStyle = INK; ctx.lineWidth = 1.5;
  ctx.beginPath(); ctx.roundRect(0, -w / 2, len, w, w / 2); ctx.fill(); ctx.stroke();
  ctx.fillStyle = "rgba(255,255,230,.35)"; ctx.fillRect(w * 0.3, -w / 2 + 2, len - w * 0.6, w * 0.22);
  ctx.strokeStyle = "rgba(60,50,15,.55)"; ctx.lineWidth = 1.5;
  for (let s = 0.32 * scale; s < len - 4; s += 0.32 * scale) { ctx.beginPath(); ctx.moveTo(s, -w / 2); ctx.lineTo(s, w / 2); ctx.stroke(); }
  ctx.restore();
}
function planks(r) { // mossy planks across the table
  const x0 = sx(r.x0), x1 = sx(r.x1), y0 = sy(r.y1), y1 = sy(r.y0), n = 9, h = (y1 - y0) / n;
  ctx.save(); ctx.beginPath(); ctx.rect(x0, y0, x1 - x0, y1 - y0); ctx.clip();
  const r2 = ((seed) => () => ((seed = (seed * 16807) % 2147483647) / 2147483647))(7);
  for (let i = 0; i < n; i++) {
    ctx.fillStyle = i % 2 ? "rgba(255,230,190,.07)" : "rgba(80,45,15,.07)"; ctx.fillRect(x0, y0 + i * h, x1 - x0, h);
    ctx.strokeStyle = "rgba(60,35,12,.5)"; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.moveTo(x0, y0 + i * h); ctx.lineTo(x1, y0 + i * h); ctx.stroke();
    ctx.strokeStyle = "rgba(90,55,20,.16)"; ctx.lineWidth = 1;
    for (let k = 0; k < 2; k++) { const yy = y0 + i * h + h * (0.33 + k * 0.33); ctx.beginPath(); ctx.moveTo(x0, yy); ctx.bezierCurveTo(x0 + (x1 - x0) * 0.3, yy - 2, x0 + (x1 - x0) * 0.6, yy + 2, x1, yy); ctx.stroke(); }
    const butt = x0 + (x1 - x0) * ((i * 0.37 + 0.2) % 1); ctx.strokeStyle = "rgba(60,35,12,.45)"; ctx.beginPath(); ctx.moveTo(butt, y0 + i * h); ctx.lineTo(butt, y0 + (i + 1) * h); ctx.stroke();
  }
  for (let i = 0; i < 40; i++) { // moss creeping in from the edges
    const edge = r2() < 0.5, x = edge ? (r2() < 0.5 ? x0 : x1) + (r2() - 0.5) * 30 : x0 + r2() * (x1 - x0), y = edge ? y0 + r2() * (y1 - y0) : (r2() < 0.5 ? y0 : y1) + (r2() - 0.5) * 30;
    ctx.fillStyle = `rgba(${70 + r2() * 30},${110 + r2() * 30},50,.35)`; ctx.beginPath(); ctx.arc(x, y, 3 + r2() * 9, 0, 7); ctx.fill();
  }
  ctx.restore();
}
const TORCHES = [[OUT.x0 - 0.0, -0.17], [OUT.x1 + 0.0, -0.17]];
function torch([x, y], time, i) { // seen from above: a bamboo pole top with a flame
  const px = sx(x), py = sy(y), u = 0.04 * scale, ox = SUN.x * 0.5 * scale, oy = SUN.y * 0.5 * scale;
  ctx.strokeStyle = "rgba(10,20,8,.35)"; ctx.lineWidth = u * 0.9; ctx.lineCap = "round";
  ctx.beginPath(); ctx.moveTo(px, py); ctx.lineTo(px + ox, py + oy); ctx.stroke();
  const f = 1 + Math.sin(time * 11 + i * 3) * 0.1 + Math.sin(time * 6.7 + i) * 0.07;
  const glow = ctx.createRadialGradient(px, py, 0, px, py, u * 6 * f);
  glow.addColorStop(0, "rgba(255,170,60,.4)"); glow.addColorStop(1, "rgba(255,140,40,0)");
  ctx.fillStyle = glow; ctx.beginPath(); ctx.arc(px, py, u * 6 * f, 0, 7); ctx.fill();
  ctx.fillStyle = "#5a3a1c"; ctx.beginPath(); ctx.arc(px, py, u, 0, 7); ctx.fill();
  ctx.strokeStyle = INK; ctx.lineWidth = 1.5; ctx.stroke();
  ctx.fillStyle = "#e8742a"; ctx.beginPath(); ctx.arc(px, py, u * 0.7 * f, 0, 7); ctx.fill();
  ctx.fillStyle = "#ffd36a"; ctx.beginPath(); ctx.arc(px - u * 0.1, py - u * 0.1, u * 0.35 * f, 0, 7); ctx.fill();
}
function drawTable(time) {
  const k = scale;
  // shadow of the whole frame, then the gutter trough
  ctx.fillStyle = "rgba(10,20,8,.4)"; ctx.beginPath(); ctx.rect(sx(OUT.x0) + 0.05 * k, sy(OUT.y1) + 0.07 * k, (OUT.x1 - OUT.x0) * k, (OUT.y1 - OUT.y0 + 0.2) * k); ctx.fill();
  ctx.fillStyle = "#7d6a2e"; ctx.beginPath(); rect(OUT); ctx.fill();
  ctx.fillStyle = "#a08a3e"; ctx.beginPath(); rect(OUT, OUT.x0 + 0.02, OUT.x1 - 0.02, OUT.y0, OUT.y1 - 0.02); ctx.fill(); // trough floor
  ctx.strokeStyle = "rgba(60,50,15,.35)"; ctx.lineWidth = 1; // split-bamboo grain along the trough
  for (let i = 1; i < 4; i++) {
    const o = (GW * i) / 4;
    ctx.beginPath(); ctx.moveTo(sx(OUT.x0 + o), sy(0)); ctx.lineTo(sx(OUT.x0 + o), sy(OUT.y1 - o)); ctx.lineTo(sx(OUT.x1 - o), sy(OUT.y1 - o)); ctx.lineTo(sx(OUT.x1 - o), sy(0)); ctx.stroke();
  }
  [OUT.x0, TABLE.x1].forEach((x) => { // the return: a woven patch at each bottom end of the gutter
    ctx.fillStyle = "rgba(232,185,35,.28)"; ctx.fillRect(sx(x), sy(RETURN_Y), GW * k, RETURN_Y * k);
    ctx.strokeStyle = "rgba(43,36,24,.3)"; ctx.lineWidth = 1;
    for (let yy = 0.04; yy < RETURN_Y; yy += 0.04) { ctx.beginPath(); ctx.moveTo(sx(x), sy(yy)); ctx.lineTo(sx(x + GW), sy(yy)); ctx.stroke(); }
  });
  ctx.save(); ctx.font = `600 ${Math.max(8, 0.045 * k)}px Futura, "Avenir Next Condensed", sans-serif`; ctx.fillStyle = "rgba(43,36,24,.65)"; ctx.textAlign = "center";
  [OUT.x0 + GW / 2, TABLE.x1 + GW / 2].forEach((x) => { ctx.save(); ctx.translate(sx(x), sy(RETURN_Y / 2)); ctx.rotate(-Math.PI / 2); ctx.fillText("RETURN", 0, 4); ctx.restore(); });
  ctx.restore();
  // the table top stands above the gutter: a drop shadow onto the trough
  ctx.fillStyle = "rgba(20,15,5,.45)"; ctx.beginPath(); ctx.rect(sx(TABLE.x0) + 0.025 * k, sy(TABLE.y1) + 0.035 * k, (TABLE.x1 - TABLE.x0) * k, (TABLE.y1 - TABLE.y0) * k); ctx.fill();
  ctx.fillStyle = "#8a5d34"; ctx.beginPath(); rect(TABLE); ctx.fill();
  planks(TABLE);
  ctx.strokeStyle = INK; ctx.lineWidth = 2; ctx.beginPath(); rect(TABLE); ctx.stroke();
  // the mat
  ctx.save(); ctx.beginPath(); rect(PAD); ctx.fillStyle = "#d9c27e"; ctx.fill(); ctx.clip();
  ctx.strokeStyle = "rgba(120,90,30,.45)"; ctx.lineWidth = 2;
  for (let v = PAD.x0 - 0.3; v < PAD.x1 + 0.3; v += 0.035) { ctx.beginPath(); ctx.moveTo(sx(v), sy(PAD.y0)); ctx.lineTo(sx(v + 0.3), sy(PAD.y1)); ctx.stroke(); ctx.beginPath(); ctx.moveTo(sx(v + 0.3), sy(PAD.y0)); ctx.lineTo(sx(v), sy(PAD.y1)); ctx.stroke(); }
  ctx.restore(); ctx.strokeStyle = INK; ctx.lineWidth = 1.5; ctx.beginPath(); rect(PAD); ctx.stroke();
  HOLES.forEach(([x, y], i) => { // holes: a colored ring round a dark well
    const r = HOLE_R * k;
    ctx.fillStyle = COLORS[i]; ctx.beginPath(); ctx.arc(sx(x), sy(y), r + 0.018 * k, 0, 7); ctx.fill();
    ctx.strokeStyle = INK; ctx.lineWidth = 1.5; ctx.stroke();
    const g = ctx.createRadialGradient(sx(x) + r * 0.3, sy(y) + r * 0.35, r * 0.1, sx(x), sy(y), r);
    g.addColorStop(0, "#2a1a0c"); g.addColorStop(1, "#0d0804");
    ctx.fillStyle = g; ctx.beginPath(); ctx.arc(sx(x), sy(y), r, 0, 7); ctx.fill();
  });
  // rails: bamboo, and one along the near edge
  RAILS.forEach(([x0, y0, x1, y1]) => {
    ctx.fillStyle = "rgba(20,15,5,.35)"; ctx.beginPath(); ctx.ellipse(sx((x0 + x1) / 2) + 3, sy((y0 + y1) / 2) + 4, Math.hypot(x1 - x0, y1 - y0) * k / 2, RAIL * k, Math.atan2(-(y1 - y0), x1 - x0), 0, 7); ctx.fill();
    bamboo(sx(x0), sy(y0), sx(x1), sy(y1), RAIL * 2 * k);
  });
  bamboo(sx(OUT.x0), sy(0) + RAIL * k * 0.5, sx(OUT.x1), sy(0) + RAIL * k * 0.5, RAIL * 1.8 * k, "#b59c4c");
  // the rack: a bamboo tray below the table
  ctx.fillStyle = "rgba(10,20,8,.35)"; ctx.beginPath(); ctx.roundRect(sx(-0.22) + 4, sy(-0.075) + 5, 0.44 * k, 0.12 * k, 0.06 * k); ctx.fill();
  ctx.fillStyle = "#b59c4c"; ctx.strokeStyle = INK; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.roundRect(sx(-0.22), sy(-0.075), 0.44 * k, 0.12 * k, 0.06 * k); ctx.fill(); ctx.stroke();
  RACK.forEach(([x, y]) => { ctx.fillStyle = "rgba(60,45,10,.45)"; ctx.beginPath(); ctx.arc(sx(x), sy(y), R * k * 1.05, 0, 7); ctx.fill(); });
  TORCHES.forEach((t, i) => torch(t, time, i));
}
function drawBall(b) {
  const z = b.level === "hole" ? -0.02 * b.sink : b.z, r = R * scale * (1 + z * 1.6), px = sx(b.x), py = sy(b.y);
  const sh = (z + (b.level === "gutter" || b.level === "air" ? DROP : 0) + R * 0.6) * scale;
  ctx.fillStyle = `rgba(20,12,4,${b.level === "held" ? 0.22 : 0.38})`;
  ctx.beginPath(); ctx.ellipse(px + SUN.x * sh, py + SUN.y * sh, r, r * 0.9, 0.6, 0, 7); ctx.fill();
  const g = ctx.createRadialGradient(px - r * 0.4, py - r * 0.45, r * 0.05, px, py, r); // lit from the upper left
  if (b.practice) { g.addColorStop(0, "#ffffff"); g.addColorStop(0.6, "#ece6d6"); g.addColorStop(1, "#b9b09a"); }
  else { g.addColorStop(0, "#d79a5e"); g.addColorStop(0.55, "#9a5f2e"); g.addColorStop(1, "#5b3418"); }
  ctx.fillStyle = g; ctx.beginPath(); ctx.arc(px, py, r, 0, 7); ctx.fill();
  ctx.save(); ctx.beginPath(); ctx.arc(px, py, r, 0, 7); ctx.clip();
  ctx.translate(px, py); ctx.rotate(-b.ang + Math.PI / 2);
  const o = Math.sin(b.spin) * r * 0.9; // grain rings roll past
  ctx.strokeStyle = b.practice ? "rgba(120,110,90,.3)" : "rgba(70,38,15,.35)"; ctx.lineWidth = 1;
  for (let k = -2; k <= 2; k++) { ctx.beginPath(); ctx.ellipse(0, o + k * r * 0.35, r * 1.1, r * 0.18, 0, 0, 7); ctx.stroke(); }
  ctx.restore();
  ctx.strokeStyle = "rgba(43,24,10,.7)"; ctx.lineWidth = 1; ctx.beginPath(); ctx.arc(px, py, r, 0, 7); ctx.stroke();
  if (b.level === "hole") { ctx.fillStyle = `rgba(0,0,0,${0.35 * b.sink})`; ctx.beginPath(); ctx.arc(px, py, r, 0, 7); ctx.fill(); }
  if (grabbable(b) && !held && (phase === "practice" || phase === "run") && b.level !== "rack") { // a pulse: grab me
    ctx.strokeStyle = `rgba(232,185,35,${0.5 + 0.4 * Math.sin(performance.now() / 180)})`; ctx.lineWidth = 3;
    ctx.beginPath(); ctx.arc(px, py, r + 5, 0, 7); ctx.stroke();
  }
}
function draw(now) {
  const time = (now - t0) / 1000;
  ctx.drawImage(jungle(), 0, 0, W, H);
  const order = { gutter: 0, air: 1, hole: 2, rack: 2, table: 3, held: 4 };
  const low = balls.filter((b) => b.level === "gutter" || (b.level === "air" && b.z < -R));
  ctx.save(); drawTable(time); ctx.restore();
  low.forEach(drawBall); // the gutter is drawn under the table edge shadow already; balls in it sit on top
  balls.filter((b) => !low.includes(b)).sort((a, b) => order[a.level] - order[b.level]).forEach(drawBall);
  canopy(time);
}

const t0 = performance.now();
let last = t0, acc = 0;
const STEP = 1 / 240;
function frame(now) {
  const dt = Math.min(0.05, (now - last) / 1000); acc += dt; last = now;
  const want = readTilt(), kk = 1 - Math.exp(-dt * 14); // smooth the sensor
  tilt = { x: tilt.x + (want.x - tilt.x) * kk, y: tilt.y + (want.y - tilt.y) * kk };
  while (acc >= STEP) {
    if (held && target) { const k = 1 - Math.exp(-STEP * 25); held.x += (target.x - held.x) * k; held.y += (target.y - held.y) * k; }
    step(STEP); acc -= STEP;
  }
  if (phase === "practice") {
    const left = practiceEnd - now; $("clock").textContent = fmt(left).replace(/\.\d$/, "");
    if (left <= 0) startCountdown();
  }
  if (phase === "run") $("time").textContent = fmt(now - runStart);
  const bx = clamp(tilt.x / MAX_TILT, -1, 1) * 15, by = clamp(-tilt.y / MAX_TILT, -1, 1) * 15; // bubble runs uphill
  $("bubble").style.transform = `translate(${-bx}px, ${-by}px)`;
  draw(now);
  requestAnimationFrame(frame);
}

// ---- start: phones only. iOS asks for motion access on a tap; no tilt events at all means no sensor. ----
const phone = matchMedia("(pointer: coarse)").matches && "DeviceOrientationEvent" in window;
function noGo(text) { $("how").textContent = text; $("hint").textContent = ""; $("go").classList.add("hidden"); }
async function wake() { try { await navigator.wakeLock?.request("screen"); } catch {} }
document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible" && phase !== "intro") wake(); });
if (!phone && !DEBUG) noGo("Tilt Table needs a phone. Open this page on your phone to play.");
$("go").onclick = async () => {
  if (!DEBUG) {
    let answer = "";
    try { answer = await DeviceOrientationEvent.requestPermission?.(); } catch { answer = "denied"; } // iOS asks; others may just say "prompt"
    if (answer === "denied") return noGo("Tilt Table needs motion access. Allow it for this site, then reload.");
    const t = performance.now();
    while (!sensed && performance.now() - t < 1500) await new Promise((r) => setTimeout(r, 50));
    if (!sensed) return noGo("No tilt readings. Tilt Table needs a phone with motion access allowed.");
  }
  screen.orientation?.lock?.("portrait").catch(() => {});
  wake(); level();
  $("intro").classList.add("hidden");
  if (RESUME) resumeRun(); else startPractice();
};
$("ready").onclick = () => { level(); startCountdown(); };
$("level").onclick = () => { level(); pop("Level"); };
$("again").onclick = () => { $("done").classList.add("hidden"); startPractice(); };
if (ACTION) {
  $("again").textContent = "See results"; $("again").onclick = () => location.reload();
  $("hint").textContent = RESUME ? "Your run is under way: the clock kept running. Let's go picks it back up."
    : "Scored, one run. Practice up to 3 minutes, then the clock runs until all three are in, even if you leave.";
}

if (DEBUG) {
  addEventListener("keydown", (e) => {
    const k = { ArrowLeft: [-12, 0], ArrowRight: [12, 0], ArrowUp: [0, 12], ArrowDown: [0, -12] }[e.key];
    if (k) fake = { x: k[0], y: k[1] };
  });
  addEventListener("keyup", () => { fake = null; });
  window.__tt = {
    tilt: (x, y) => { fake = x == null ? null : { x, y }; },
    state: () => ({ phase, rollbacks, aces, filled: filled(), time: phase === "done" ? runEnd - runStart : 0,
      balls: balls.map((b) => ({ level: b.level, x: +b.x.toFixed(3), y: +b.y.toFixed(3), v: +speed(b).toFixed(3), falls: b.falls })) }),
    screen: (x, y) => ({ x: sx(x), y: sy(y) }),
    holes: HOLES, pad: PAD,
    sim: (x, y, vx, vy, secs = 2) => { // roll a lone ball on a level table, synchronously; where does it end up?
      const keep = [balls, holes, active, tilt], b = { x, y, vx, vy, z: 0, vz: 0, level: "table", spin: 0, ang: 0, falls: 0, practice: true };
      balls = [b]; holes = [null, null, null]; active = b; tilt = { x: 0, y: 0 };
      for (let i = 0; i < secs * 240 && b.level === "table"; i++) step(STEP);
      [balls, holes, active, tilt] = keep; return { level: b.level, x: +b.x.toFixed(3), y: +b.y.toFixed(3) };
    },
  };
}
resize(); setup(true); requestAnimationFrame(frame);
if ("serviceWorker" in navigator) navigator.serviceWorker.register("/sw.js").catch(() => {});
}
