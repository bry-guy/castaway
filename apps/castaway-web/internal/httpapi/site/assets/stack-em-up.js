{ // own block: page scripts share one global scope
// Stack 'Em Up, side view, phone held sideways. One thumb holds a bamboo stick; on top, a tray rests on two balls
// in a cup. The other thumb drags letter blocks from the table onto the tray, one at a time, until the tower
// reads SURVIVOR from the top. Sliding the stick moves the balls under the tray. A short stack rights itself, but
// once its weight sits higher than the cup's curve the tray tips unless you keep the balls under it. Let go of
// the stick, tip too far, or stack a block past the edge of the one below, and the tower falls: every block goes
// back to the table (a drop).
const DEG = Math.PI / 180;
const BW = 0.1, BH = 0.09;                // a block (m)
const TRAY_W = 0.36, TRAY_T = 0.025;      // the tray
const CRADLE_R = 0.22;                    // the cup's curve: the tray rocks as if on a rocker this round
const STICK_TOP = 1.0;                    // where the tray rests, above the hand
const M_TRAY = 1.5, M_BLOCK = 1;
const G = 4.5;                            // slowed down: real gravity tips a 0.7 m tower too fast to catch
const DAMP = 0.8;                         // rocking friction (1/s)
const HAND_W = 14;                        // how quickly the stick follows the thumb (critically damped, rad/s)
const SLIDE_MAX = 0.13, SLIDE_GAIN = 0.5; // support travel; metres of travel per metre of thumb on screen
const FALL_TILT = 28 * DEG;               // blocks slide off past this
const HOLD_S = 3, PRACTICE_S = 180;
const SCORED_WORD = "SURVIVOR", PRACTICE_WORD = "ISLAND";
const TABLE = { x0: 0.55, x1: 1.55, y: 0.8 };
const VIEW = { x0: -0.5, x1: 1.62, y0: 0.7, y1: 1.92 };
const DEBUG = location.search.includes("debug");
// Scored runs: the server puts data-action on <body>, and data-resume (drops, elapsed ms, and the height the
// stack had) when the player already started. The clock is the server's.
const DATA = document.body.dataset, ACTION = DATA.action || "", RESUME = DATA.resume === "1";

// <physics> (pure: no page state; a self-check can evaluate this part alone)
// Tray-frame parts: u along the tray from its middle, v up from its underside.
function parts(stack) {
  const out = [{ m: M_TRAY, u: 0, v: TRAY_T / 2, i: M_TRAY * (TRAY_W ** 2 + TRAY_T ** 2) / 12 }];
  stack.forEach((x, k) => out.push({ m: M_BLOCK, u: x, v: TRAY_T + BH * (k + 0.5), i: M_BLOCK * (BW ** 2 + BH ** 2) / 12 }));
  return out;
}
// One step. s: { th (tray tilt, + is right side down), thV, slide (support under the tray, m), slideV, slideTarget,
// held, stack (block offsets along the tray) }. The tray rocks on a curve of radius CRADLE_R, so the contact is
// CRADLE_R·th along the tray from wherever the support sits; gravity turns the tray about the contact.
function physics(s, dt) {
  const want = s.held ? s.slideTarget : 0;
  s.slideV += (HAND_W ** 2 * (want - s.slide) - 2 * HAND_W * s.slideV) * dt;
  s.slide += s.slideV * dt;
  const contact = CRADLE_R * s.th + s.slide;
  let M = 0, I = 0, mu = 0, mv = 0;
  for (const p of parts(s.stack)) { M += p.m; mu += p.m * p.u; mv += p.m * p.v; I += p.i + p.m * ((p.u - contact) ** 2 + p.v ** 2); }
  const lever = (mu / M - contact) * Math.cos(s.th) + (mv / M) * Math.sin(s.th); // weight's offset from the contact
  s.thV += (M * G * lever / I - DAMP * s.thV) * dt;
  s.th += s.thV * dt;
  return contact;
}
// toppled: the first block (0 = bottom) whose blocks above sit past its edge, or -1. The tray is "block" -1's
// support, wide as the tray.
function toppled(stack) {
  for (let k = stack.length - 1; k >= 0; k--) {
    const above = stack.slice(k), com = above.reduce((a, x) => a + x, 0) / above.length;
    const under = k === 0 ? 0 : stack[k - 1], half = k === 0 ? TRAY_W / 2 : BW / 2;
    if (Math.abs(com - under) > half) return k;
  }
  return -1;
}
// </physics>

const $ = (id) => document.getElementById(id);
const canvas = $("c"), ctx = canvas.getContext("2d");
let scale = 1, cx = 0, cy = 0, W = 0, H = 0, bg = null;
function resize() {
  const d = devicePixelRatio || 1, top = 40;
  W = innerWidth; H = innerHeight; canvas.width = W * d; canvas.height = H * d;
  scale = Math.min(W / (VIEW.x1 - VIEW.x0), (H - top) / (VIEW.y1 - VIEW.y0));
  cx = W / 2 - ((VIEW.x0 + VIEW.x1) / 2) * scale;
  cy = top + (H - top) / 2 + ((VIEW.y0 + VIEW.y1) / 2) * scale;
  ctx.setTransform(d, 0, 0, d, 0, 0); bg = null;
}
addEventListener("resize", resize);
const sx = (x) => cx + x * scale, sy = (y) => cy - y * scale;
const toWorld = (e) => ({ x: (e.clientX - cx) / scale, y: (cy - e.clientY) / scale });
const clamp = (v, a, b) => Math.max(a, Math.min(b, v));

// ---- game state ----
// s: the physics state above. word: the blocks to stack, top to bottom, so the first placed is its last letter.
// table: letters still on the table (index into word order). flying: blocks of a fallen tower, in the world.
let s, word, phase = "intro", drops = 0, runStart = 0, runEnd = 0, practiceEnd = 0, holdFrom = 0, flying = [], dropping = null;
function setup(practice) {
  word = practice ? PRACTICE_WORD : SCORED_WORD;
  s = { th: 0, thV: 0, slide: 0, slideV: 0, slideTarget: 0, held: !!stick, stack: [] };
  flying = []; dropping = null; holdFrom = 0; hud();
}
const placed = () => s.stack.length;
const nextLetter = () => word[word.length - 1 - placed()];
function slot(i) { // where the i-th placed block waits on the table
  const n = word.length, gap = (TABLE.x1 - TABLE.x0 - 0.14) / Math.max(1, n - 1);
  return { x: TABLE.x0 + 0.07 + i * gap, y: TABLE.y + BH / 2 };
}
function startPractice() {
  phase = "practice"; setup(true); practiceEnd = performance.now() + PRACTICE_S * 1000;
  $("practicing").classList.remove("hidden"); $("ready").classList.remove("hidden"); pop("Practice!");
}
function startCountdown() {
  phase = "countdown"; $("practicing").classList.add("hidden"); $("ready").classList.add("hidden");
  setup(false);
  let n = 3;
  const el = $("count"); el.classList.remove("hidden"); el.textContent = n;
  const tick = setInterval(() => {
    n--;
    if (n > 0) { el.textContent = n; return; }
    clearInterval(tick); el.classList.add("hidden");
    phase = "run"; runStart = performance.now(); pop("Go!"); report();
  }, 800);
}
function fmt(ms) { const t = Math.max(0, ms) / 1000; return `${Math.floor(t / 60)}:${(t % 60).toFixed(1).padStart(4, "0")}`; }
function hud() {
  const live = phase === "run" || phase === "done";
  $("height").textContent = `${live || phase === "practice" ? placed() : 0}/${word.length}`;
  $("drops").textContent = live ? drops : 0;
}
function pop(text) { const el = $("pop"); el.textContent = text; el.classList.remove("go"); void el.offsetWidth; el.classList.add("go"); }

function resumeRun() { // a reload mid-run: the tower is gone (a drop if it had blocks) and the clock kept going
  phase = "run"; setup(false);
  drops = (+DATA.drops || 0) + (+DATA.height > 0 ? 1 : 0);
  runStart = performance.now() - (+DATA.elapsed || 0); hud(); report(); pop("Clock's running!");
}

// Scored runs: progress at "Go" (which starts the server's clock), after every block and every drop, and when the
// full tower has been held. Saves go one at a time, always the newest state, and a failed save retries until it
// lands. A save the server refuses is shown, never swallowed (as in Tilt Table).
let saving = null, pending = null;
function saveNote(text) {
  let el = $("savenote");
  if (!el) { el = document.createElement("p"); el.id = "savenote"; el.setAttribute("role", "status");
    el.style.cssText = "position:fixed;left:50%;bottom:12px;transform:translateX(-50%);margin:0;padding:6px 12px;border-radius:8px;background:#c8372d;color:#fff;font:600 14px sans-serif;z-index:50;max-width:90vw;text-align:center"; document.body.append(el); }
  el.textContent = text; el.hidden = !text;
}
function save(body) {
  if (!ACTION) return Promise.resolve(null);
  pending = body;
  return saving || (saving = (async () => {
    let res = null;
    for (let tries = 0; pending;) {
      const sent = pending;
      try {
        const r = await fetch(ACTION + "/progress", { method: "POST", keepalive: true, redirect: "manual", headers: { "Content-Type": "application/json" }, body: JSON.stringify(sent) });
        if (r.ok) { res = await r.json(); tries = 0; if (pending === sent) pending = null; saveNote(""); continue; }
        if (r.type === "opaqueredirect" || r.status === 401) { saveNote("Not saved: you're signed out. Sign in again in another tab, then keep playing."); }
        else if (r.status === 409 || r.status === 400) { const e = await r.json().catch(() => ({})); saveNote(`Not saved: ${e.error || "the server refused this run"}. Reload the page.`); pending = null; break; }
        else saveNote("Saving… (retrying)");
      } catch { saveNote("Not saved yet: no connection. Retrying…"); }
      await new Promise((ok) => setTimeout(ok, Math.min(8000, 500 * 2 ** tries++)));
    }
    saving = null;
    return res;
  })());
}
function report(finished = false) {
  if (!ACTION || phase === "practice") return Promise.resolve(null);
  return save({ height: finished ? word.length : Math.min(placed(), word.length - 1), drops });
}

// ---- the tray in the world ----
// The support (the balls) sits at x = slide; the tray touches it at `contact` along its underside.
function trayFrame() {
  const contact = CRADLE_R * s.th + s.slide, c = Math.cos(s.th), n = Math.sin(s.th);
  const px = s.slide + CRADLE_R * n, py = STICK_TOP - CRADLE_R + CRADLE_R * c; // on the rocker's curve
  const qx = px - contact * c, qy = py + contact * n;                          // the tray's middle, underneath
  return { contact, at: (u, v) => ({ x: qx + u * c + v * n, y: qy - u * n + v * c }),
    from: (x, y) => { const dx = x - qx, dy = y - qy; return { u: dx * c - dy * n, v: dx * n + dy * c }; } };
}

// ---- input: one thumb on the stick (left of the table), one on the blocks ----
let stick = null, block = null; // { id, x0, slide0 } and { id, letter, x, y, tx, ty }
const stickZone = (e) => toWorld(e).x < (TABLE.x0 + 0.25) / 2;
canvas.addEventListener("pointerdown", (e) => {
  if (!(phase === "practice" || phase === "run" || phase === "countdown")) return;
  if (stickZone(e)) {
    if (stick) return;
    stick = { id: e.pointerId, x0: e.clientX, slide0: s.slideTarget }; s.held = true;
  } else {
    if (block || phase === "countdown" || dropping || placed() >= word.length) return;
    const p = toWorld(e), at = slot(placed());
    if (Math.abs(p.x - at.x) > BW * 1.1 || Math.abs(p.y - at.y) > BH * 1.6) return;
    block = { id: e.pointerId, letter: nextLetter(), x: at.x, y: at.y, tx: p.x, ty: p.y + 0.07, ang: 0 };
  }
  try { canvas.setPointerCapture(e.pointerId); } catch {} navigator.vibrate?.(10);
});
canvas.addEventListener("pointermove", (e) => {
  if (stick?.id === e.pointerId) s.slideTarget = clamp(stick.slide0 + SLIDE_GAIN * (e.clientX - stick.x0) / scale, -SLIDE_MAX, SLIDE_MAX);
  if (block?.id === e.pointerId) { const p = toWorld(e); block.tx = p.x; block.ty = p.y + 0.07; } // the block rides above the thumb
});
function letGo(e) {
  if (stick?.id === e.pointerId) {
    stick = null; s.held = false;
    if (placed() > 0) fall("Dropped the stick!");
  }
  if (block?.id === e.pointerId) { release(block); block = null; }
}
canvas.addEventListener("pointerup", letGo);
canvas.addEventListener("pointercancel", letGo);
// iOS: no pinch zoom or page bounce while two thumbs are down.
document.addEventListener("gesturestart", (e) => e.preventDefault());
document.addEventListener("touchmove", (e) => e.preventDefault(), { passive: false });

// A block let go over the top of the stack drops onto it, keeping where it was along the tray; anywhere else it
// goes back to the table. One whose middle is past the edge of the block below tips off and goes back too.
function release(b) {
  const f = trayFrame(), p = f.from(b.x, b.y), top = TRAY_T + BH * placed();
  const under = placed() ? s.stack[placed() - 1] : 0, reach = placed() ? BW : TRAY_W / 2 + BW / 2;
  if (!(p.v > top - BH * 0.2 && p.v < top + 0.4 && Math.abs(p.u - under) < reach)) return;
  dropping = { letter: b.letter, u: p.u, v: Math.max(p.v, top + BH / 2), vv: 0 };
}
function land(d) { // the dropped block reaches the top of the stack
  dropping = null;
  if (placed() > 0 ? Math.abs(d.u - s.stack[placed() - 1]) > BW / 2 : Math.abs(d.u) > TRAY_W / 2) {
    const f = trayFrame(), w = f.at(d.u, TRAY_T + BH * (placed() + 0.5));
    flying.push({ letter: d.letter, x: w.x, y: w.y, vx: Math.sign(d.u - (s.stack.at(-1) ?? 0)) * 0.6, vy: 0.4, ang: s.th, spin: 5, t: 0 });
    pop("Missed!"); return;
  }
  s.stack.push(d.u); navigator.vibrate?.(15);
  s.thV += (d.u - (CRADLE_R * s.th + s.slide)) * 0.8; // the knock of setting it down
  if (toppled(s.stack) >= 0) return fall("Toppled!");
  hud(); report();
  if (placed() === word.length) { holdFrom = performance.now(); pop("Hold it!"); }
  else pop(word.slice(word.length - placed()));
}
// The tower falls: its blocks fly off the tray, then go back to the table. In a run, that's a drop.
function fall(why) {
  if (!s.stack.length) return;
  const f = trayFrame();
  s.stack.forEach((u, k) => {
    const w = f.at(u, TRAY_T + BH * (k + 0.5)), r = TRAY_T + BH * (k + 0.5);
    flying.push({ letter: word[word.length - 1 - k], x: w.x, y: w.y, vx: s.thV * r * Math.cos(s.th) + Math.sin(s.th) * 0.8 + (k - 3) * 0.05,
      vy: 0.3 + 0.1 * k, ang: s.th, spin: (Math.sign(s.th) || 1) * (2 + k), t: 0 });
  });
  s.stack = []; holdFrom = 0; navigator.vibrate?.([40, 30, 40]);
  pop(why);
  if (phase === "run") { drops++; report(); }
  hud();
}

// ---- the game step (fixed 240 Hz) ----
function step(dt) {
  if (block) { const k = 1 - Math.exp(-dt * 30); block.x += (block.tx - block.x) * k; block.y += (block.ty - block.y) * k; }
  const contact = physics(s, dt);
  if (s.stack.length && (Math.abs(s.th) > FALL_TILT || Math.abs(contact) > TRAY_W / 2 - 0.02)) fall(Math.abs(s.th) > FALL_TILT ? "Too far!" : "Off the balls!");
  if (!s.stack.length) { // an empty tray never falls off: it settles back over the balls
    if (Math.abs(contact) > TRAY_W / 2 - 0.02) s.th -= (contact - Math.sign(contact) * (TRAY_W / 2 - 0.02)) / CRADLE_R;
    s.th = clamp(s.th, -FALL_TILT, FALL_TILT);
  }
  if (dropping) {
    dropping.vv -= 9.8 * dt; dropping.v += dropping.vv * dt;
    if (dropping.v <= TRAY_T + BH * (placed() + 0.5)) land(dropping);
  }
  for (const b of flying) { b.t += dt; b.vy -= 9.8 * dt; b.x += b.vx * dt; b.y += b.vy * dt; b.ang += b.spin * dt; }
  flying = flying.filter((b) => b.y > VIEW.y0 - 0.5);
}
function frameLogic(now) {
  if (phase === "practice") {
    const left = practiceEnd - now; $("clock").textContent = fmt(left).replace(/\.\d$/, "");
    if (left <= 0) startCountdown();
  }
  if (phase === "run") $("time").textContent = fmt(now - runStart);
  const holding = holdFrom && placed() === word.length;
  $("hold").classList.toggle("hidden", !holding);
  if (holding) {
    const left = HOLD_S - (now - holdFrom) / 1000;
    $("hold").textContent = `Hold ${Math.ceil(left)}`;
    if (left <= 0) {
      holdFrom = 0;
      if (phase === "run") { phase = "done"; runEnd = now; finish(report(true)); }
      else { pop("Stacked!"); setTimeout(() => { if (phase === "practice") { s.stack = []; hud(); pop("Again!"); } }, 1200); }
    }
  }
  $("grip").classList.toggle("hidden", !!stick || !(phase === "practice" || phase === "run" || phase === "countdown"));
}

function finish(saved) {
  const time = runEnd - runStart;
  hud(); $("time").textContent = fmt(time);
  let official = null; // the server's clock is the one that counts
  saved.then((res) => { if (res?.time) { official = res.time; $("final").textContent = $("time").textContent = res.time; } });
  setTimeout(() => {
    $("final").textContent = official ?? fmt(time); $("summary").textContent = "";
    const row = $("stamps"); row.textContent = "";
    const stamps = [["Done +1", ""], ...(drops === 0 ? [["Clean +2", "ace"]] : [])];
    $("done").classList.remove("hidden");
    stamps.forEach(([txt, kind], i) => setTimeout(() => {
      const el = document.createElement("span"); el.className = `stamp ${kind}`; el.textContent = txt; row.append(el);
      navigator.vibrate?.(25);
    }, 400 + i * 420));
    setTimeout(() => {
      $("summary").textContent = `${drops} ${drops === 1 ? "drop" : "drops"}` + (ACTION ? ". Fastest +3 and second +2 when it closes." : ". Free play: not scored.");
    }, 600 + stamps.length * 420);
  }, 900);
}

// ---- drawing: flat, matte, light from the top left (Tilt Table's jungle, from the side) ----
const INK = "#2b2418";
const rnd = ((seed) => () => ((seed = (seed * 16807) % 2147483647) / 2147483647))(51);
const FRONDS = Array.from({ length: 22 }, () => ({ x: rnd(), y: rnd() * 0.75, a: rnd() * 6.3, s: 0.6 + rnd() * 0.9, k: rnd() < 0.5 }));
function leaf(g, x, y, a, sz, color, vein) {
  g.save(); g.translate(x, y); g.rotate(a); g.scale(sz, sz);
  g.fillStyle = color; g.beginPath(); g.moveTo(0, 0); g.bezierCurveTo(18, -26, 62, -24, 84, 0); g.bezierCurveTo(62, 24, 18, 26, 0, 0); g.fill();
  g.strokeStyle = vein; g.lineWidth = 2; g.beginPath(); g.moveTo(2, 0); g.lineTo(80, 0); g.stroke();
  g.restore();
}
function jungle() { // sky through the canopy, fronds, a sand floor; drawn once per size
  if (bg) return bg;
  const d = devicePixelRatio || 1, c = document.createElement("canvas"); c.width = W * d; c.height = H * d;
  const g = c.getContext("2d"); g.setTransform(d, 0, 0, d, 0, 0);
  const sky = g.createLinearGradient(0, 0, 0, H); sky.addColorStop(0, "#9cc9a4"); sky.addColorStop(0.6, "#4f7f4a"); sky.addColorStop(1, "#2c4a2b");
  g.fillStyle = sky; g.fillRect(0, 0, W, H);
  FRONDS.forEach((l) => leaf(g, l.x * W, l.y * H, l.a, l.s, l.k ? "rgba(40,80,40,.55)" : "rgba(30,60,30,.5)", "rgba(20,40,15,.35)"));
  const sand = sy(TABLE.y - 0.12);
  g.fillStyle = "#c9ad74"; g.fillRect(0, sand, W, H - sand);
  g.fillStyle = "rgba(120,90,40,.25)"; for (let i = 0; i < W / 3; i++) g.fillRect(rnd() * W, sand + rnd() * (H - sand), 2, 2);
  bg = c; return c;
}
function bamboo(x0, y0, x1, y1, w, color = "#c9b25e") {
  const len = Math.hypot(x1 - x0, y1 - y0);
  ctx.save(); ctx.translate(x0, y0); ctx.rotate(Math.atan2(y1 - y0, x1 - x0));
  ctx.fillStyle = color; ctx.strokeStyle = INK; ctx.lineWidth = 1.5;
  ctx.beginPath(); ctx.roundRect(0, -w / 2, len, w, w / 2); ctx.fill(); ctx.stroke();
  ctx.fillStyle = "rgba(255,255,230,.35)"; ctx.fillRect(w * 0.3, -w / 2 + 2, len - w * 0.6, w * 0.22);
  ctx.strokeStyle = "rgba(60,50,15,.55)";
  for (let k = 0.3 * scale; k < len - 4; k += 0.3 * scale) { ctx.beginPath(); ctx.moveTo(k, -w / 2); ctx.lineTo(k, w / 2); ctx.stroke(); }
  ctx.restore();
}
function drawBlock(x, y, ang, letter, alpha = 1) { // a wooden block, letter burned on its face
  const w = BW * scale, h = BH * scale;
  ctx.save(); ctx.globalAlpha = alpha; ctx.translate(sx(x), sy(y)); ctx.rotate(ang);
  ctx.fillStyle = "rgba(20,12,4,.3)"; ctx.fillRect(-w / 2 + 3, -h / 2 + 4, w, h);
  const g = ctx.createLinearGradient(-w / 2, -h / 2, w / 2, h / 2); g.addColorStop(0, "#e9c88f"); g.addColorStop(1, "#b98a4e");
  ctx.fillStyle = g; ctx.strokeStyle = INK; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.rect(-w / 2, -h / 2, w, h); ctx.fill(); ctx.stroke();
  ctx.strokeStyle = "rgba(110,70,30,.25)"; ctx.lineWidth = 1;
  for (let k = -1; k <= 1; k++) { ctx.beginPath(); ctx.moveTo(-w / 2, k * h * 0.3); ctx.bezierCurveTo(-w / 6, k * h * 0.3 - 2, w / 6, k * h * 0.3 + 2, w / 2, k * h * 0.3); ctx.stroke(); }
  if (letter) { ctx.fillStyle = "#7a2418"; ctx.font = `700 ${h * 0.68}px Futura, "Avenir Next Condensed", sans-serif`; ctx.textAlign = "center"; ctx.textBaseline = "middle"; ctx.fillText(letter, 0, h * 0.04); }
  ctx.restore();
}
function drawTable() {
  const k = scale, top = sy(TABLE.y), x0 = sx(TABLE.x0 - 0.04), x1 = sx(TABLE.x1 + 0.04);
  [TABLE.x0 + 0.03, TABLE.x1 - 0.03].forEach((x) => bamboo(sx(x), top, sx(x), sy(TABLE.y - 0.14), 0.035 * k, "#b59c4c"));
  ctx.fillStyle = "rgba(20,12,4,.3)"; ctx.fillRect(x0 + 4, top + 5, x1 - x0, 0.03 * k);
  ctx.fillStyle = "#8a5d34"; ctx.strokeStyle = INK; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.rect(x0, top, x1 - x0, 0.03 * k); ctx.fill(); ctx.stroke();
  for (let i = placed() + (dropping ? 1 : 0) + (block ? 1 : 0); i < word.length; i++) {
    const p = slot(i);
    drawBlock(p.x, p.y, 0, word[word.length - 1 - i]);
  }
  const next = placed() + (dropping ? 1 : 0); // the next block pulses: grab me
  if (!block && next < word.length && (phase === "practice" || phase === "run")) {
    const p = slot(next);
    ctx.strokeStyle = `rgba(232,185,35,${0.5 + 0.4 * Math.sin(performance.now() / 180)})`; ctx.lineWidth = 3;
    ctx.strokeRect(sx(p.x - BW / 2) - 4, sy(p.y + BH / 2) - 4, BW * k + 8, BH * k + 8);
  }
}
function drawRig(time) { // the stick, the cup and its two balls, the tray and the tower
  const f = trayFrame(), k = scale, br = 0.028;
  const cupY = STICK_TOP - 2 * br - 0.006; // the cup's floor
  bamboo(sx(s.slide * 0.6), H + 10, sx(s.slide), sy(cupY - 0.02), 0.04 * k);
  ctx.fillStyle = "#6b4423"; ctx.strokeStyle = INK; ctx.lineWidth = 1.5; // the cup
  ctx.beginPath(); ctx.ellipse(sx(s.slide), sy(cupY + 0.005), 0.085 * k, 0.04 * k, 0, 0, Math.PI); ctx.closePath(); ctx.fill(); ctx.stroke();
  const roll = (f.contact - s.slide) / br; // the balls roll as the tray rocks over them
  [-1, 1].forEach((side) => {
    const p = f.at(f.contact + side * br * 1.15, -br), x = sx(p.x), y = sy(p.y), r = br * k;
    const g = ctx.createRadialGradient(x - r * 0.4, y - r * 0.4, r * 0.1, x, y, r); g.addColorStop(0, "#d79a5e"); g.addColorStop(1, "#5b3418");
    ctx.fillStyle = g; ctx.beginPath(); ctx.arc(x, y, r, 0, 7); ctx.fill(); ctx.strokeStyle = INK; ctx.stroke();
    ctx.strokeStyle = "rgba(43,24,10,.5)"; ctx.beginPath(); ctx.moveTo(x, y); ctx.lineTo(x + Math.cos(roll) * r, y + Math.sin(roll) * r); ctx.stroke();
  });
  const corners = [[-TRAY_W / 2, 0], [TRAY_W / 2, 0], [TRAY_W / 2, TRAY_T], [-TRAY_W / 2, TRAY_T]].map(([u, v]) => f.at(u, v));
  ctx.fillStyle = "#a0703e"; ctx.strokeStyle = INK; ctx.lineWidth = 2; ctx.beginPath();
  corners.forEach((p, i) => (i ? ctx.lineTo(sx(p.x), sy(p.y)) : ctx.moveTo(sx(p.x), sy(p.y)))); ctx.closePath(); ctx.fill(); ctx.stroke();
  s.stack.forEach((u, i) => { const p = f.at(u, TRAY_T + BH * (i + 0.5)); drawBlock(p.x, p.y, s.th, word[word.length - 1 - i]); });
  if (dropping) { const p = f.at(dropping.u, dropping.v); drawBlock(p.x, p.y, s.th, dropping.letter); }
  if (block && placed() < word.length) { // where it would land
    const p = f.from(block.x, block.y), top = TRAY_T + BH * placed(), under = placed() ? s.stack[placed() - 1] : 0;
    if (p.v > top - BH * 0.2 && p.v < top + 0.4 && Math.abs(p.u - under) < (placed() ? BW : TRAY_W / 2 + BW / 2)) {
      const g = f.at(p.u, top + BH / 2); drawBlock(g.x, g.y, s.th, "", 0.25);
    }
  }
  void time;
}
function drawGrip() { // the left thumb's spot, and how far the stick has slid
  const y = H - 46, x = sx(0) - 0.18 * scale;
  ctx.fillStyle = "rgba(236,230,214,.18)"; ctx.strokeStyle = "rgba(236,230,214,.5)"; ctx.lineWidth = 2;
  ctx.beginPath(); ctx.roundRect(x - SLIDE_MAX / SLIDE_GAIN * scale * 0.5, y - 14, SLIDE_MAX / SLIDE_GAIN * scale, 28, 14); ctx.fill(); ctx.stroke();
  ctx.fillStyle = stick ? "#e8b923" : `rgba(232,185,35,${0.5 + 0.4 * Math.sin(performance.now() / 180)})`;
  ctx.beginPath(); ctx.arc(x + s.slide / SLIDE_MAX * SLIDE_MAX / SLIDE_GAIN * scale * 0.5, y, 12, 0, 7); ctx.fill(); ctx.strokeStyle = INK; ctx.stroke();
}
function draw(now) {
  const time = (now - t0) / 1000;
  ctx.drawImage(jungle(), 0, 0, W, H);
  drawTable(); drawRig(time);
  if (phase !== "intro") drawGrip();
  for (const b of flying) drawBlock(b.x, b.y, b.ang, b.letter);
  if (block) drawBlock(block.x, block.y, 0, block.letter);
}

const t0 = performance.now();
let last = t0, acc = 0;
const STEP = 1 / 240;
function frame(now) {
  acc += Math.min(0.05, (now - last) / 1000); last = now;
  while (acc >= STEP) { step(STEP); acc -= STEP; }
  frameLogic(now);
  draw(now);
  requestAnimationFrame(frame);
}

// ---- start: phones only (two thumbs on a touch screen) ----
const phone = matchMedia("(pointer: coarse)").matches && navigator.maxTouchPoints > 1;
function noGo(text) { $("how").textContent = text; $("hint").textContent = ""; $("go").classList.add("hidden"); }
async function wake() { try { await navigator.wakeLock?.request("screen"); } catch {} }
document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible" && phase !== "intro") wake(); });
if (!phone && !DEBUG) noGo("Stack 'Em Up needs a phone: one thumb holds the stick, the other stacks. Open this page on your phone to play.");
$("go").onclick = () => {
  screen.orientation?.lock?.("landscape").catch(() => {});
  wake();
  $("intro").classList.add("hidden");
  if (RESUME) resumeRun(); else startPractice();
};
$("ready").onclick = () => startCountdown();
$("again").onclick = () => { $("done").classList.add("hidden"); startPractice(); };
if (ACTION) {
  $("again").textContent = "See results"; $("again").onclick = () => location.reload();
  $("hint").textContent = RESUME ? "Your run is under way: the clock kept running, and the tower fell when you left (a drop)."
    : "Scored, one run: practice up to 3 minutes, then the clock runs until SURVIVOR stands, even if you leave.";
}

if (DEBUG) { // desktop testing: hold Shift for the stick, arrows slide it; the mouse stacks
  addEventListener("keydown", (e) => {
    if (e.key === "Shift" && !stick) { stick = { id: -1, x0: 0, slide0: s.slideTarget }; s.held = true; }
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") s.slideTarget = clamp(s.slideTarget + (e.key === "ArrowLeft" ? -0.01 : 0.01), -SLIDE_MAX, SLIDE_MAX);
  });
  addEventListener("keyup", (e) => { if (e.key === "Shift") letGo({ pointerId: -1 }); });
  window.__se = { state: () => ({ phase, drops, placed: placed(), th: s.th / DEG, slide: s.slide, stack: [...s.stack] }), physics, toppled, s: () => s };
}
resize(); setup(true); requestAnimationFrame(frame);
if ("serviceWorker" in navigator) navigator.serviceWorker.register("/sw.js").catch(() => {});
}
