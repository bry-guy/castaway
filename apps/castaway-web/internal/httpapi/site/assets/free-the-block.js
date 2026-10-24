{ // own block: page scripts share one global scope
// Free the Block (Klotski), jungle theme. Blocks slide in four directions. A drag slides one block along the axis
// it moves on, and a drag that lands on a cell is one slide. Consecutive slides of one block count as one move,
// as on the server (internal/freetheblock), which replays the same list to score it.
const COLS = 4, ROWS = 5;
// Piece ids match internal/freetheblock: 0 idol (2×2), 1–4 verticals (1×2), 5 horizontal (2×1), 6–9 soldiers.
// Each entry: [width, height, start x, start y]. The 横刀立马 layout: empty cells at the bottom centre.
const START = [[2, 2, 1, 0], [1, 2, 0, 0], [1, 2, 3, 0], [1, 2, 0, 2], [1, 2, 3, 2], [2, 1, 1, 2], [1, 1, 1, 3], [1, 1, 2, 3], [1, 1, 0, 4], [1, 1, 3, 4]];
const GOAL = [1, 3]; // the idol's top-left cell once it's out through the bottom gap
const NAMES = { u: "up", d: "down", l: "left", r: "right" }; // wire codes from the saved run
const DELTA = { up: [0, -1], down: [0, 1], left: [-1, 0], right: [1, 0] };
const CODES = { up: "u", down: "d", left: "l", right: "r" };
const INK = "#2b2418", HUD_H = 44;
const ACTION = document.body.dataset.action || "", RESUME = document.body.dataset.moves || "";
const $ = (id) => document.getElementById(id);
const canvas = $("c"), ctx = canvas.getContext("2d");

let pos, shown, moves, count, lastPiece, done, drag = null, floor = null, last = performance.now();
let W = 0, H = 0, c = 40, bx = 0, by = 0; // c: px per cell; (bx, by): the board's top-left corner

function reset() {
  pos = START.map(([, , x, y]) => [x, y]);
  shown = pos.map(([x, y]) => [x, y]);
  moves = []; count = 0; lastPiece = -1; done = false; drag = null;
  $("moves").textContent = "0";
  $("done").classList.add("hidden");
}

function grid() {
  const g = Array.from({ length: ROWS }, () => new Array(COLS).fill(-1));
  pos.forEach(([x, y], p) => {
    for (let j = 0; j < START[p][1]; j++) for (let i = 0; i < START[p][0]; i++) g[y + j][x + i] = p;
  });
  return g;
}
function fits(g, p, x, y) {
  const [w, h] = START[p];
  if (x < 0 || y < 0 || x + w > COLS || y + h > ROWS) return false;
  for (let j = 0; j < h; j++) for (let i = 0; i < w; i++) {
    const o = g[y + j][x + i];
    if (o !== -1 && o !== p) return false;
  }
  return true;
}
// Whole cells block p can slide in direction (dx, dy) from where it is.
function room(g, p, dx, dy) {
  let n = 0;
  while (fits(g, p, pos[p][0] + (n + 1) * dx, pos[p][1] + (n + 1) * dy)) n++;
  return n;
}
function slide(p, dir, steps) {
  const [dx, dy] = DELTA[dir], g = grid();
  for (let k = 1; k <= steps; k++) if (!fits(g, p, pos[p][0] + k * dx, pos[p][1] + k * dy)) return false;
  pos[p] = [pos[p][0] + steps * dx, pos[p][1] + steps * dy];
  if (p !== lastPiece) count++;
  lastPiece = p;
  moves.push({ piece: p, dir, steps });
  $("moves").textContent = count;
  return true;
}
const solved = () => pos[0][0] === GOAL[0] && pos[0][1] === GOAL[1];

// The saved run: 3 characters a slide (piece letter, direction code, steps), replayed on load.
function resume(code) {
  for (let i = 0; i + 2 < code.length; i += 3) {
    const dir = NAMES[code[i + 1]];
    if (dir) slide(code.charCodeAt(i) - 97, dir, +code[i + 2]);
  }
}

// Scored runs save the whole list after every slide; the server only accepts a list that extends the saved one.
function report(finished) {
  if (!ACTION) return; // practice is never saved
  fetch(ACTION + "/progress", { method: "POST", keepalive: true, headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ moves, finished }) }).catch(() => {});
}

// ---- layout ----
function resize() {
  const d = devicePixelRatio || 1;
  W = innerWidth; H = innerHeight;
  canvas.width = W * d; canvas.height = H * d; ctx.setTransform(d, 0, 0, d, 0, 0);
  c = Math.min((W * 0.9) / 5.4, (H - HUD_H - 40) / 6.9); // the platform is 5 cells wide and 6 tall, plus its edge
  bx = W / 2 - 2 * c;
  by = HUD_H + (H - HUD_H) / 2 - 2.5 * c;
  floor = null;
}
const px = (x) => bx + x * c, py = (y) => by + y * c;
const cellAt = (cx, cy) => [Math.floor((cx - bx) / c), Math.floor((cy - by) / c)];
function rr(x, y, w, h, r) {
  ctx.beginPath(); ctx.moveTo(x + r, y); ctx.arcTo(x + w, y, x + w, y + h, r); ctx.arcTo(x + w, y + h, x, y + h, r);
  ctx.arcTo(x, y + h, x, y, r); ctx.arcTo(x, y, x + w, y, r); ctx.closePath();
}
const shadowAt = (h) => [0.55 * h, 0.8 * h]; // the sun is upper left: everything casts down and right

// ---- input: drag a block along one axis; release snaps it to the nearest whole cell ----
canvas.addEventListener("pointerdown", (e) => {
  if (done) return;
  const [col, row] = cellAt(e.clientX, e.clientY);
  if (col < 0 || row < 0 || col >= COLS || row >= ROWS) return;
  const g = grid(), p = g[row][col];
  if (p < 0) return;
  canvas.setPointerCapture(e.pointerId);
  drag = { p, x0: e.clientX, y0: e.clientY, axis: null, off: 0,
    range: { x: [-room(g, p, -1, 0), room(g, p, 1, 0)], y: [-room(g, p, 0, -1), room(g, p, 0, 1)] } };
});
canvas.addEventListener("pointermove", (e) => {
  if (!drag) return;
  const dx = (e.clientX - drag.x0) / c, dy = (e.clientY - drag.y0) / c;
  if (!drag.axis) {
    if (Math.hypot(dx, dy) < 0.15) return;
    const pref = Math.abs(dx) > Math.abs(dy) ? "x" : "y", other = pref === "x" ? "y" : "x";
    const open = (a) => drag.range[a][0] !== 0 || drag.range[a][1] !== 0;
    drag.axis = open(pref) ? pref : other;
  }
  const [lo, hi] = drag.range[drag.axis];
  drag.off = Math.max(lo, Math.min(hi, drag.axis === "x" ? dx : dy));
});
function release() {
  if (!drag) return;
  const { p, axis, off } = drag;
  drag = null;
  const steps = Math.round(Math.abs(off));
  if (!axis || steps < 1) return; // a tap or a nudge snaps back
  const dir = axis === "x" ? (off < 0 ? "left" : "right") : (off < 0 ? "up" : "down");
  if (!slide(p, dir, steps)) return;
  if (solved()) finish(); else report(false);
}
canvas.addEventListener("pointerup", release);
canvas.addEventListener("pointercancel", release);

// ---- end card: the idol is out; stamps slam down like Inside Track's ----
function finish() {
  done = true;
  report(true);
  setTimeout(() => {
    const row = $("stamps"), stamps = [["IDOL FREE", "idol"], [`${count} MOVES`, "moves"]];
    row.textContent = ""; $("final").textContent = "0"; $("finalNote").textContent = "";
    $("again").textContent = ACTION ? "See results" : "Play again";
    $("done").classList.remove("hidden");
    stamps.forEach(([txt, kind], i) => setTimeout(() => {
      const el = document.createElement("span");
      el.className = `stamp ${kind}`; el.textContent = txt; row.append(el);
      $("done").querySelector(".card").animate([{ transform: "translate(0,0)" }, { transform: "translate(-3px,3px)" }, { transform: "translate(2px,-1px)" }, { transform: "translate(0,0)" }], { duration: 180 });
      navigator.vibrate?.(25);
      if (i === stamps.length - 1) $("final").textContent = count;
    }, 500 + i * 450));
    setTimeout(() => { $("finalNote").textContent = ACTION ? "Results when the game closes." : "Practice · not saved."; }, 700 + stamps.length * 450);
  }, 700);
}

// ---- drawing: flat, matte, one light from the upper left ----
const floorPattern = () => {
  if (floor) return floor;
  const s = document.createElement("canvas"); s.width = s.height = 256;
  const g = s.getContext("2d");
  g.fillStyle = "#2f5d3a"; g.fillRect(0, 0, 256, 256);
  for (let i = 0; i < 90; i++) { // leaf litter and damp patches
    g.fillStyle = Math.random() < 0.5 ? "rgba(15,40,20,.22)" : "rgba(150,190,100,.1)";
    g.beginPath(); g.ellipse(Math.random() * 256, Math.random() * 256, 6 + Math.random() * 16, 3 + Math.random() * 8, Math.random() * 3, 0, 7); g.fill();
  }
  for (let i = 0; i < 400; i++) {
    g.fillStyle = Math.random() < 0.5 ? "rgba(10,30,15,.25)" : "rgba(200,230,150,.08)";
    g.fillRect(Math.random() * 256, Math.random() * 256, 1.5, 1.5);
  }
  return (floor = ctx.createPattern(s, "repeat"));
};
// Palm fronds framing the screen edges: [x, y as fractions of the screen, angle phase, size].
const PALMS = [[-0.05, 0, 0.2, 1], [1.05, 0, 0.8, 1], [-0.05, 1, 2.4, 1], [1.05, 1, 4.0, 1], [-0.1, 0.5, 1.0, 0.7], [1.1, 0.5, 2.2, 0.7]];
function palm([fx, fy, a, s], time, shadow) {
  const x = fx * W, y = fy * H, L = Math.max(W, H) * 0.24 * s;
  ctx.save();
  if (shadow) ctx.translate(...shadowAt(0.4 * c));
  for (let f = 0; f < 7; f++) {
    const ang = a + (f * 2 * Math.PI) / 7 + Math.sin(time * 0.6 + f + a) * 0.05;
    ctx.save(); ctx.translate(x, y); ctx.rotate(ang);
    ctx.beginPath(); ctx.moveTo(0, 0); ctx.quadraticCurveTo(L * 0.5, -L * 0.2, L, 0); ctx.quadraticCurveTo(L * 0.5, L * 0.2, 0, 0);
    ctx.fillStyle = shadow ? "rgba(8,28,12,.35)" : f % 2 ? "#3f8a4e" : "#2c6b3c";
    ctx.fill();
    if (!shadow) { ctx.strokeStyle = "rgba(15,45,22,.5)"; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.moveTo(L * 0.05, 0); ctx.lineTo(L * 0.95, 0); ctx.stroke(); }
    ctx.restore();
  }
  ctx.restore();
}
// A vine hanging between two points, with leaves along it.
function vine(x0, y0, x1, y1, sag, time) {
  const cx = (x0 + x1) / 2 + Math.sin(time) * 6, cy = (y0 + y1) / 2 + sag;
  ctx.strokeStyle = "#1f4a2a"; ctx.lineWidth = 4; ctx.lineCap = "round";
  ctx.beginPath(); ctx.moveTo(x0, y0); ctx.quadraticCurveTo(cx, cy, x1, y1); ctx.stroke();
  ctx.fillStyle = "#3f8a4e";
  for (let i = 1; i < 7; i++) {
    const t = i / 7, x = (1 - t) * (1 - t) * x0 + 2 * (1 - t) * t * cx + t * t * x1, y = (1 - t) * (1 - t) * y0 + 2 * (1 - t) * t * cy + t * t * y1;
    ctx.beginPath(); ctx.ellipse(x, y + 8, 9, 4, i % 2 ? 0.6 : -0.6, 0, 7); ctx.fill();
  }
}
function planks(x0, y0, w, h) { // flat plank seams, grain and butt joints, clipped to the slab
  const n = 6, ph = h / n;
  for (let i = 0; i < n; i++) {
    ctx.fillStyle = i % 2 ? "rgba(255,230,190,.07)" : "rgba(80,45,15,.07)"; ctx.fillRect(x0, y0 + i * ph, w, ph);
    ctx.strokeStyle = "rgba(70,40,15,.5)"; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.moveTo(x0, y0 + i * ph); ctx.lineTo(x0 + w, y0 + i * ph); ctx.stroke();
    ctx.strokeStyle = "rgba(90,55,20,.2)"; ctx.lineWidth = 1;
    for (let k = 0; k < 3; k++) {
      const yy = y0 + i * ph + ph * (0.25 + k * 0.25);
      ctx.beginPath(); ctx.moveTo(x0, yy); ctx.bezierCurveTo(x0 + w * 0.3, yy - 2, x0 + w * 0.6, yy + 2, x0 + w, yy); ctx.stroke();
    }
    const butt = x0 + w * ((i * 0.37) % 1);
    ctx.strokeStyle = "rgba(70,40,15,.4)"; ctx.beginPath(); ctx.moveTo(butt, y0 + i * ph); ctx.lineTo(butt, y0 + (i + 1) * ph); ctx.stroke();
  }
}
// The raised challenge platform: shadow on the floor, the slab's edge, then the planked top.
function platform() {
  const x0 = bx - 0.5 * c, y0 = by - 0.5 * c, w = 5 * c, h = 6 * c, t = 0.3 * c, r = 0.22 * c;
  const [ox, oy] = shadowAt(t + 0.3 * c);
  ctx.fillStyle = "rgba(8,28,12,.42)"; rr(x0 + ox, y0 + oy, w, h, r); ctx.fill();
  ctx.fillStyle = "#6e4a24"; rr(x0, y0 + t, w, h, r); ctx.fill();
  ctx.strokeStyle = INK; ctx.lineWidth = 2.5; rr(x0, y0 + t, w, h, r); ctx.stroke();
  ctx.fillStyle = "#b98a55"; rr(x0, y0, w, h, r); ctx.fill();
  ctx.save(); rr(x0, y0, w, h, r); ctx.clip(); planks(x0, y0, w, h); ctx.restore();
  ctx.strokeStyle = INK; rr(x0, y0, w, h, r); ctx.stroke();
}
// The lashed bamboo frame around the grid, with nodes, corner lashings and the bottom gap.
function bamboo() {
  const fx = bx - 0.24 * c, fy = by - 0.24 * c, fw = 4 * c + 0.48 * c, fh = 5 * c + 0.48 * c, b = 0.24 * c;
  ctx.fillStyle = "#9bb35f"; rr(fx, fy, fw, fh, 0.1 * c); ctx.fill();
  ctx.strokeStyle = "#4f6b2a"; ctx.lineWidth = 2; rr(fx, fy, fw, fh, 0.1 * c); ctx.stroke();
  ctx.strokeStyle = "#6f8a3c"; ctx.lineWidth = 3;
  for (let x = fx + 0.9 * c; x < fx + fw - 0.5 * c; x += 0.9 * c) { // nodes along the top and bottom
    ctx.beginPath(); ctx.moveTo(x, fy); ctx.lineTo(x, fy + b); ctx.moveTo(x, fy + fh - b); ctx.lineTo(x, fy + fh); ctx.stroke();
  }
  for (let y = fy + 0.9 * c; y < fy + fh - 0.5 * c; y += 0.9 * c) { // and down the sides
    ctx.beginPath(); ctx.moveTo(fx, y); ctx.lineTo(fx + b, y); ctx.moveTo(fx + fw - b, y); ctx.lineTo(fx + fw, y); ctx.stroke();
  }
  ctx.fillStyle = "#4a3219"; ctx.fillRect(bx, by, 4 * c, 5 * c); // the grid's dark wood
  ctx.fillStyle = "#2a1a0b"; ctx.fillRect(px(1), py(5), 2 * c, b); // the bottom gap the idol leaves by
  ctx.strokeStyle = "#8a6a3a"; ctx.lineWidth = 3;
  for (const [lx, ly] of [[fx, fy], [fx + fw - b, fy], [fx, fy + fh - b], [fx + fw - b, fy + fh - b]]) { // lashings
    ctx.beginPath(); ctx.moveTo(lx + 3, ly + 3); ctx.lineTo(lx + b - 3, ly + b - 3); ctx.moveTo(lx + b - 3, ly + 3); ctx.lineTo(lx + 3, ly + b - 3); ctx.stroke();
  }
  ctx.strokeStyle = INK; ctx.lineWidth = 2; ctx.strokeRect(bx, by, 4 * c, 5 * c);
}
function holes(g) {
  for (let row = 0; row < ROWS; row++) for (let col = 0; col < COLS; col++) {
    if (g[row][col] !== -1) continue;
    ctx.fillStyle = "#2a1a0b"; rr(px(col) + 0.04 * c, py(row) + 0.04 * c, 0.92 * c, 0.92 * c, 0.12 * c); ctx.fill();
    ctx.strokeStyle = "rgba(0,0,0,.45)"; ctx.lineWidth = 2; rr(px(col) + 0.04 * c, py(row) + 0.04 * c, 0.92 * c, 0.92 * c, 0.12 * c); ctx.stroke();
  }
}
function block(p, lifted) {
  const [w, h] = START[p], i = 0.07 * c;
  const X = px(shown[p][0]) + i, Y = py(shown[p][1]) + i, WW = w * c - 2 * i, HH = h * c - 2 * i, r = 0.14 * c;
  const [ox, oy] = shadowAt(lifted ? 0.22 * c : 0.1 * c);
  ctx.fillStyle = `rgba(20,12,4,${lifted ? 0.5 : 0.38})`; rr(X + ox, Y + oy, WW, HH, r); ctx.fill();
  const g = ctx.createLinearGradient(X, Y, X + WW, Y + HH);
  if (p === 0) { g.addColorStop(0, "#f7d77a"); g.addColorStop(0.55, "#d9a441"); g.addColorStop(1, "#9a6a1c"); }
  else if (p >= 6) { g.addColorStop(0, "#d9a766"); g.addColorStop(0.6, "#b07a43"); g.addColorStop(1, "#7a4d22"); }
  else { g.addColorStop(0, "#c4915a"); g.addColorStop(0.6, "#9a6434"); g.addColorStop(1, "#66401c"); }
  ctx.fillStyle = g; rr(X, Y, WW, HH, r); ctx.fill();
  ctx.save(); rr(X, Y, WW, HH, r); ctx.clip();
  ctx.strokeStyle = p === 0 ? "rgba(120,70,10,.25)" : "rgba(60,32,10,.28)"; ctx.lineWidth = 1;
  for (let k = 1; k < 5; k++) {
    const yy = Y + (HH * k) / 5;
    ctx.beginPath(); ctx.moveTo(X, yy); ctx.bezierCurveTo(X + WW * 0.3, yy - 3, X + WW * 0.6, yy + 3, X + WW, yy); ctx.stroke();
  }
  ctx.restore();
  ctx.strokeStyle = "rgba(60,30,8,.35)"; ctx.lineWidth = 2; rr(X + 3, Y + 3, WW - 6, HH - 6, r * 0.7); ctx.stroke(); // carved groove
  if (p === 0) { // the idol: gold, a carved face in a ring
    const s = Math.min(WW, HH), mx = X + WW / 2, my = Y + HH / 2;
    ctx.strokeStyle = "rgba(90,50,8,.55)"; ctx.lineWidth = 2; ctx.beginPath(); ctx.arc(mx, my, s * 0.4, 0, 7); ctx.stroke();
    ctx.fillStyle = "#6b3f0e"; ctx.beginPath(); ctx.arc(mx, my, s * 0.26, 0, 7); ctx.fill();
    ctx.fillStyle = "#f7d77a";
    for (const side of [-1, 1]) { ctx.beginPath(); ctx.arc(mx + side * s * 0.1, my - s * 0.05, s * 0.035, 0, 7); ctx.fill(); }
    ctx.strokeStyle = "#f7d77a"; ctx.lineWidth = 2.5; ctx.beginPath(); ctx.arc(mx, my + s * 0.07, s * 0.1, 0.15 * Math.PI, 0.85 * Math.PI); ctx.stroke();
  }
  ctx.strokeStyle = INK; ctx.lineWidth = 2; rr(X, Y, WW, HH, r); ctx.stroke();
}
function torch(x, y, time, i) { // a bamboo torch seen from above: the pole's shadow falls with the sun
  const u = 0.045 * c, [ox, oy] = shadowAt(0.5 * c);
  ctx.strokeStyle = "rgba(8,28,12,.35)"; ctx.lineWidth = u * 0.9; ctx.lineCap = "round";
  ctx.beginPath(); ctx.moveTo(x, y); ctx.lineTo(x + ox, y + oy); ctx.stroke();
  const f = 1 + Math.sin(time * 11 + i * 3) * 0.1 + Math.sin(time * 6.7 + i) * 0.07;
  const glow = ctx.createRadialGradient(x, y, 0, x, y, u * 5 * f);
  glow.addColorStop(0, "rgba(255,170,60,.35)"); glow.addColorStop(1, "rgba(255,140,40,0)");
  ctx.fillStyle = glow; ctx.beginPath(); ctx.arc(x, y, u * 5 * f, 0, 7); ctx.fill();
  ctx.fillStyle = "#5a3a1c"; ctx.beginPath(); ctx.arc(x, y, u, 0, 7); ctx.fill();
  ctx.strokeStyle = INK; ctx.lineWidth = 1.5; ctx.stroke();
  ctx.fillStyle = "#e8742a"; ctx.beginPath(); ctx.arc(x, y, u * 0.7 * f, 0, 7); ctx.fill();
  ctx.fillStyle = "#ffd36a"; ctx.beginPath(); ctx.arc(x - u * 0.1, y - u * 0.1, u * 0.35 * f, 0, 7); ctx.fill();
}
function draw(time) {
  ctx.fillStyle = floorPattern(); ctx.fillRect(0, 0, W, H);
  PALMS.forEach((p) => palm(p, time, true));
  vine(-10, 0, W * 0.42, 0, H * 0.07, time);
  vine(W + 10, 0, W * 0.58, 0, H * 0.07, time);
  PALMS.forEach((p) => palm(p, time, false));
  platform();
  bamboo();
  const g = grid();
  holes(g);
  const lifted = drag && drag.axis ? drag.p : -1;
  pos.forEach((_, p) => { if (p !== lifted) block(p, false); });
  if (lifted >= 0) block(lifted, true);
  const x0 = bx - 0.5 * c, y0 = by - 0.5 * c, w = 5 * c;
  torch(x0 + 0.13 * c, y0 + 0.13 * c, time, 0);
  torch(x0 + w - 0.13 * c, y0 + 0.13 * c, time, 1);
}
function frame(now) {
  const dt = Math.min(0.05, (now - last) / 1000), k = 1 - Math.exp(-dt * 26);
  last = now;
  pos.forEach(([x, y], p) => { // blocks ease toward their cell; the dragged one follows the finger
    let tx = x, ty = y;
    if (drag && drag.p === p && drag.axis) { if (drag.axis === "x") tx += drag.off; else ty += drag.off; }
    shown[p][0] += (tx - shown[p][0]) * k; shown[p][1] += (ty - shown[p][1]) * k;
  });
  draw(now / 1000);
  requestAnimationFrame(frame);
}

$("go").onclick = () => $("intro").classList.add("hidden");
if (!ACTION) { $("practicing").classList.remove("hidden"); $("restart").classList.remove("hidden"); $("restart").onclick = reset; }
$("again").onclick = ACTION ? () => location.reload() : reset;
addEventListener("resize", resize);
resize(); reset();
if (RESUME) resume(RESUME);
if (solved()) { $("intro").classList.add("hidden"); finish(); }
requestAnimationFrame(frame);
}
