{ // own block: page scripts share one global scope
// Stack 'Em Up, side view, phone held upright. The phone is the pole: a tray balances on a ball at its top, and
// tilting or jerking the phone swings the pole's top from side to side. Weights hang under the tray's ends, so an
// empty tray or a short stack hangs level by itself; once the tower's weight sits higher than the weights', it
// tips like a broom on your hand and you have to keep moving under it. One finger pulls letter blocks from a
// slotted rack (each one you pull lets the next drop down) and stacks them until the tower reads SURVIVOR from the
// top. Tip too far, let a tall tower topple, hit the tray on the pole or stack a block past the edge of the one
// below, and the tower falls: every block goes back to the rack (a drop).
const DEG = Math.PI / 180;
const BW = 0.1, BH = 0.09;                // a block (m)
const TRAY_W = 0.36, TRAY_T = 0.025;      // the tray
const STAND_TOP = 0.42;                   // the ball on the pole's top, on screen
const BALL_R = 0.025;
const RIM = 60 * DEG;                     // past this tilt against the pole the tray hits it
const M_TRAY = 3, M_BLOCK = 1;
const KEEL_U = 0.16, KEEL_V = -0.15;      // the two hanging weights, under the tray's ends
let KEEL_M = 1.1;                         // each; heavier keeps taller stacks hanging level
let STICK_L = 0.6;                        // the pole: how far its top swings for a tilt of the phone (m)
let SHAKE = 1;                            // how much the hand's own sideways jolts push the pole's top
let G = 4.5;                              // gravity for the balancing, slowed so a tall tower is humanly catchable
let DAMP = 1;                             // air drag on the tray's swing (1/s)
const FOLLOW_W = 25;                      // the pole follows the phone this briskly (rad/s), smoothing the sensor
const MAX_TILT = 40 * DEG, DEAD = 0.5 * DEG; // phone roll used, and ignored jitter
let FALL_TILT = 20 * DEG;                 // blocks slide off the tray past this, from level in the world
const HOLD_S = 3, PRACTICE_S = 180;
const SCORED_WORD = "SURVIVOR", PRACTICE_WORD = "ISLAND";
const RACK = { x: 0.34, y: 0.8, pitch: 0.1 }; // the slotted rack, top right: slot 0 (the one you pull) at the bottom
const VIEW = { x0: -0.32, x1: 0.46, y0: 0, y1: 1.62 };
const DEBUG = location.search.includes("debug");
// Scored runs: the server puts data-action on <body>, and data-resume (drops, elapsed ms, and the height the
// stack had) when the player already started. The clock is the server's.
const DATA = document.body.dataset, ACTION = DATA.action || "", RESUME = DATA.resume === "1";
if (!ACTION) { // free play only: ?len=0.6 ?keel=1.1 ?g=4.5 ?damp=1 ?shake=1 ?fall=20 to tune on a phone without a deploy
  const q = new URLSearchParams(location.search), n = (k) => (q.has(k) && Number.isFinite(+q.get(k)) ? +q.get(k) : null);
  STICK_L = n("len") ?? STICK_L; KEEL_M = n("keel") ?? KEEL_M; G = n("g") ?? G; DAMP = n("damp") ?? DAMP;
  SHAKE = n("shake") ?? SHAKE; FALL_TILT = (n("fall") ?? FALL_TILT / DEG) * DEG;
}

// <physics> (pure: no page state; a self-check can evaluate this part alone)
// One step. s: { a (the tray's tilt from level in the world, + is right side down), aV, phi (the pole's tilt, + is
// its top to the right), phiV, want (the phone's tilt, which the pole follows), hand (the hand's sideways
// acceleration, m/s²), stack (block offsets along the tray) }. The tray hinges on the pole's top. Its load (tray,
// weights, blocks) has its weight hc above the hinge and ub along: gravity pulls it over by hc·sin a + ub·cos a,
// and the hinge accelerating sideways (ap) swings it back the other way by hc·cos a. Below the hinge (hc < 0) it
// hangs level; above, it's a broom: move the pole's top toward the lean to get under it.
function physics(s, dt) {
  const w = FOLLOW_W, phiA0 = w * w * (s.want - s.phi) - 2 * w * s.phiV; // a critically damped follower: smooth, so its acceleration is too
  s.phiV += phiA0 * dt; s.phi += s.phiV * dt;
  let phiA = phiA0;
  if (Math.abs(s.phi) > MAX_TILT) { s.phi = Math.sign(s.phi) * MAX_TILT; phiA = -s.phiV / dt; s.phiV = 0; }
  const ap = STICK_L * (phiA * Math.cos(s.phi) - s.phiV ** 2 * Math.sin(s.phi)) + SHAKE * s.hand;
  const parts = [{ m: M_TRAY, u: 0, v: TRAY_T / 2, i: M_TRAY * (TRAY_W ** 2 + TRAY_T ** 2) / 12 },
    { m: KEEL_M, u: -KEEL_U, v: KEEL_V, i: 0 }, { m: KEEL_M, u: KEEL_U, v: KEEL_V, i: 0 }];
  s.stack.forEach((u, k) => parts.push({ m: M_BLOCK, u, v: TRAY_T + BH * (k + 0.5), i: M_BLOCK * (BW ** 2 + BH ** 2) / 12 }));
  let m = 0, ub = 0, hc = 0, I = 0;
  parts.forEach((q) => { m += q.m; ub += q.m * q.u; hc += q.m * q.v; I += q.i + q.m * (q.u ** 2 + q.v ** 2); });
  ub /= m; hc /= m;
  const c = Math.cos(s.a), n = Math.sin(s.a);
  s.aV += (m * (G * (hc * n + ub * c) - ap * (hc * c - ub * n)) / I - DAMP * s.aV) * dt;
  s.a += s.aV * dt;
}
// toppled: the first block (0 = bottom) whose blocks above, tilted a from level in the world, have their weight past
// its edge, or -1. The tray is "block" -1's support, wide as the tray. A tall tower goes over at a small tilt.
function toppled(stack, a = 0) {
  for (let k = stack.length - 1; k >= 0; k--) {
    const above = stack.slice(k), com = above.reduce((x, y) => x + y, 0) / above.length;
    const under = k === 0 ? 0 : stack[k - 1], half = k === 0 ? TRAY_W / 2 : BW / 2;
    if (Math.abs((com - under) * Math.cos(a) + (BH * above.length / 2) * Math.sin(a)) > half) return k;
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
  cy = H - (H - top - (VIEW.y1 - VIEW.y0) * scale) / 2 + VIEW.y0 * scale; // centred below the HUD
  ctx.setTransform(d, 0, 0, d, 0, 0); bg = null;
}
addEventListener("resize", resize);
const sx = (x) => cx + x * scale, sy = (y) => cy - y * scale;
const toWorld = (e) => ({ x: (e.clientX - cx) / scale, y: (cy - e.clientY) / scale });
const clamp = (v, a, b) => Math.max(a, Math.min(b, v));

// ---- tilt: the phone's roll in the screen plane (held upright), from deviceorientation, relative to a calibrated
// "level". Gravity in the phone's frame is (sin γ cos β, −sin β); near flat that has no direction, so keep the last.
let raw = null, cal = 0, fake = null, sensed = false;
addEventListener("deviceorientation", (e) => {
  if (e.beta == null || e.gamma == null) return;
  raw = { b: e.beta * DEG, g: e.gamma * DEG }; sensed = true;
});
let handRaw = 0; // the hand's sideways acceleration (m/s², gravity removed), smoothed
addEventListener("devicemotion", (e) => { const x = e.acceleration?.x; if (x != null) handRaw += (x - handRaw) * 0.35; });
const handNow = () => (Math.abs(handRaw) < 0.15 ? 0 : clamp(handRaw - Math.sign(handRaw) * 0.15, -6, 6));
function roll() {
  if (!raw) return null;
  const x = Math.sin(raw.g) * Math.cos(raw.b), y = Math.sin(raw.b);
  return Math.hypot(x, y) < 0.3 ? null : Math.atan2(x, y);
}
function level() { const r = roll(); if (r != null) cal = r; }
function readPhi(prev) {
  if (fake != null) return fake;
  const r = roll();
  if (r == null) return prev;
  let d = Math.atan2(Math.sin(r - cal), Math.cos(r - cal));
  d = Math.abs(d) < DEAD ? 0 : d - Math.sign(d) * DEAD;
  return clamp(d, -MAX_TILT, MAX_TILT);
}

// ---- game state ----
// s: the physics state above. word: the blocks to stack, top to bottom, so the first placed is its last letter;
// the rack holds the rest in placing order, bottom up. flying: blocks of a fallen tower, in the world.
let s, word, phase = "intro", drops = 0, runStart = 0, runEnd = 0, practiceEnd = 0, holdFrom = 0, flying = [], dropping = null;
let block = null, lift = 0, liftV = 0; // the block in hand ({ id, letter, x, y, tx, ty, home }); the rack's drop
function setup(practice) {
  word = practice ? PRACTICE_WORD : SCORED_WORD;
  s = { a: 0, aV: 0, phi: s?.phi ?? 0, phiV: 0, want: s?.want ?? 0, hand: 0, stack: [] }; settle();
  flying = []; dropping = null; block = null; lift = 0; holdFrom = 0; hud();
}
const placed = () => s.stack.length;
const nextLetter = () => word[word.length - 1 - placed()];
const rackFirst = () => placed() + (dropping ? 1 : 0) + (block && !block.home ? 1 : 0); // placing index in slot 0
const rackSlot = (k) => ({ x: RACK.x, y: RACK.y + BH / 2 + (k + lift) * RACK.pitch });
function holdUpright(then) {
  const el = $("count"); el.classList.add("msg"); el.classList.remove("hidden"); el.textContent = "Hold your phone upright";
  setTimeout(() => { el.classList.remove("msg"); el.classList.add("hidden"); level(); then(); }, 1400);
}
function startPractice() {
  phase = "practice"; setup(true); practiceEnd = performance.now() + PRACTICE_S * 1000;
  $("practicing").classList.remove("hidden"); $("ready").classList.remove("hidden"); pop("Practice!");
}
function startCountdown() {
  phase = "countdown"; $("practicing").classList.add("hidden"); $("ready").classList.add("hidden");
  setup(false); holdUpright(countdown);
}
function countdown() {
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
// The phone is the pole, so on screen the pole stands still and the world tilts: the tray's tilt on screen is its
// tilt in the world less the pole's. It hinges on the ball at (0, STAND_TOP).
const trayTilt = () => s.a - s.phi;
function trayFrame() {
  const rot = trayTilt(), rc = Math.cos(rot), rn = Math.sin(rot), qx = 0, qy = STAND_TOP;
  return { rot, at: (u, v) => ({ x: qx + u * rc + v * rn, y: qy - u * rn + v * rc }),
    from: (x, y) => { const dx = x - qx, dy = y - qy; return { u: dx * rc - dy * rn, v: dx * rn + dy * rc }; } };
}
let rimT = 0; // how long an empty tray has lain against the pole
function settle(nudge = true) { // a fresh tray, level in the world (as far as the pole allows), with a nudge too small to see
  rimT = 0; s.a = clamp(0, s.phi - RIM * 0.8, s.phi + RIM * 0.8) + (nudge ? (Math.random() - 0.5) * 0.4 * DEG : 0); s.aV = s.phiV;
}
const onRim = () => Math.abs(trayTilt()) >= RIM - 0.5 * DEG;

// ---- input: one finger pulls the bottom block out of the rack (anywhere on the rack grabs it) ----
canvas.addEventListener("pointerdown", (e) => {
  if (!(phase === "practice" || phase === "run") || block || dropping || placed() >= word.length) return;
  const p = toWorld(e), bottom = rackSlot(0);
  if (Math.abs(p.x - RACK.x) > BW * 0.9 || p.y < bottom.y - BH || p.y > RACK.y + RACK.pitch * (word.length - placed()) + 0.05) return;
  block = { id: e.pointerId, letter: nextLetter(), x: bottom.x, y: bottom.y, tx: p.x, ty: p.y + 0.07 };
  lift = 1; liftV = 0; // the rest drop down a slot
  try { canvas.setPointerCapture(e.pointerId); } catch {} navigator.vibrate?.(10);
});
canvas.addEventListener("pointermove", (e) => {
  if (block?.id === e.pointerId && !block.home) { const p = toWorld(e); block.tx = p.x; block.ty = p.y + 0.07; } // it rides above the finger
});
function letGo(e) {
  if (block?.id !== e.pointerId || block.home) return;
  if (release(block)) block = null;
  else { block.home = true; const r = rackSlot(0); block.tx = r.x; block.ty = r.y; } // back into the rack
}
canvas.addEventListener("pointerup", letGo);
canvas.addEventListener("pointercancel", letGo);
document.addEventListener("gesturestart", (e) => e.preventDefault());
document.addEventListener("touchmove", (e) => e.preventDefault(), { passive: false });

// A block let go over the top of the stack drops onto it, keeping where it was along the tray; anywhere else it
// goes back to the rack. One whose middle is past the edge of the block below tips off and goes back too.
function canLand(p) {
  const top = TRAY_T + BH * placed(), under = placed() ? s.stack[placed() - 1] : 0;
  return p.v > top - BH * 0.2 && p.v < top + 0.4 && Math.abs(p.u - under) < (placed() ? BW : TRAY_W / 2 + BW / 2);
}
function release(b) {
  const p = trayFrame().from(b.x, b.y), top = TRAY_T + BH * placed();
  if (!canLand(p)) return false;
  dropping = { letter: b.letter, u: p.u, v: Math.max(p.v, top + BH / 2), vv: 0 };
  return true;
}
function land(d) { // the dropped block reaches the top of the stack
  dropping = null;
  if (placed() > 0 ? Math.abs(d.u - s.stack[placed() - 1]) > BW / 2 : Math.abs(d.u) > TRAY_W / 2 || onRim()) { // an empty tray on the rim: it slides off
    const w = trayFrame().at(d.u, TRAY_T + BH * (placed() + 0.5));
    flying.push({ letter: d.letter, x: w.x, y: w.y, vx: Math.sign(d.u - (s.stack.at(-1) ?? 0)) * 0.6, vy: 0.4, ang: trayFrame().rot, spin: 5 });
    lift = 1; liftV = 0; pop("Missed!"); return;
  }
  s.stack.push(d.u); navigator.vibrate?.(15);
  s.aV += d.u * 0.8; // the knock of setting it down
  if (toppled(s.stack, s.a) >= 0) return fall("Toppled!");
  hud(); report();
  if (placed() === word.length) { holdFrom = performance.now(); pop("Hold it!"); }
  else pop(word.slice(word.length - placed()));
}
// The tower falls: its blocks fly off the tray, then go back to the rack. In a run, that's a drop.
function fall(why) {
  if (!s.stack.length) return;
  const f = trayFrame(), lean = Math.sign(s.a) || 1;
  s.stack.forEach((u, k) => {
    const w = f.at(u, TRAY_T + BH * (k + 0.5)), r = TRAY_T + BH * (k + 0.5);
    flying.push({ letter: word[word.length - 1 - k], x: w.x, y: w.y, vx: (s.aV - s.phiV) * r * Math.cos(f.rot) + lean * 0.8 + (k - 3) * 0.05,
      vy: 0.3 + 0.1 * k, ang: f.rot, spin: lean * (2 + k) });
  });
  s.stack = []; holdFrom = 0; lift = 1; liftV = 0; navigator.vibrate?.([40, 30, 40]);
  settle();
  pop(why);
  if (phase === "run") { drops++; report(); }
  hud();
}

// ---- the game step (fixed 240 Hz) ----
function step(dt) {
  if (block) {
    const k = 1 - Math.exp(-dt * (block.home ? 14 : 30)); block.x += (block.tx - block.x) * k; block.y += (block.ty - block.y) * k;
    if (block.home) { lift += (1 - lift) * k; if (Math.hypot(block.tx - block.x, block.ty - block.y) < 0.004) { block = null; lift = 0; } }
  }
  if (lift > 0 && !block?.home) { liftV += 12 * dt; lift = Math.max(0, lift - liftV * dt); if (!lift) liftV = 0; } // the rack drops a slot
  const gx = 9.8 * Math.sin(s.phi), gy = -9.8 * Math.cos(s.phi); // real gravity, on the tilted screen
  if (!(phase === "practice" || phase === "run")) { physics(s, dt); settle(false); return stepRest(dt, gx, gy); } // the tray waits, level, until play
  physics(s, dt);
  if (Math.abs(trayTilt()) >= RIM) { // the tray hits the pole: it stops there
    const d = Math.sign(trayTilt()); s.a = s.phi + d * RIM;
    if ((s.aV - s.phiV) * d > 0) s.aV = s.phiV - (s.aV - s.phiV) * 0.35; // and bounces back
    if (s.stack.length) fall("Hit the pole!");
    else if ((rimT += dt) > 0.6) settle(); // an empty one lying there is set level again
  } else {
    if (Math.abs(trayTilt()) < RIM - DEG) rimT = 0;
    if (s.stack.length && Math.abs(s.a) > FALL_TILT) fall("Too far!");
    else if (toppled(s.stack, s.a) >= 0) fall("Toppled!");
  }
  if (dropping) {
    dropping.vv -= 9.8 * dt; dropping.v += dropping.vv * dt;
    if (dropping.v <= TRAY_T + BH * (placed() + 0.5)) land(dropping);
  }
  stepRest(dt, gx, gy);
}
function stepRest(dt, gx, gy) { // blocks in the air
  for (const b of flying) { b.vx += gx * dt; b.vy += gy * dt; b.x += b.vx * dt; b.y += b.vy * dt; b.ang += b.spin * dt; }
  flying = flying.filter((b) => b.y > VIEW.y0 - 0.3 && b.x > VIEW.x0 - 0.3 && b.x < VIEW.x1 + 0.3);
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
      else { pop("Stacked!"); setTimeout(() => { if (phase === "practice") { s.stack = []; lift = 1; hud(); pop("Again!"); } }, 1200); }
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
  const sand = sy(0.06);
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
function drawRack() { // a slotted bamboo rack, top right: pull the bottom block out to the left
  const k = scale, n = word.length, first = rackFirst(), top = RACK.y + RACK.pitch * n + 0.01;
  const xl = RACK.x - BW / 2 - 0.014, xr = RACK.x + BW / 2 + 0.014;
  bamboo(sx(xr), sy(RACK.y - 0.03), sx(xr), sy(top + 0.03), 0.022 * k, "#b59c4c");
  bamboo(sx(xl), sy(RACK.y + RACK.pitch), sx(xl), sy(top + 0.03), 0.022 * k, "#b59c4c"); // open at the bottom slot
  ctx.fillStyle = "#8a5d34"; ctx.strokeStyle = INK; ctx.lineWidth = 1.2;
  for (let j = 0; j <= n; j++) { // the slats between slots
    const y = sy(RACK.y + j * RACK.pitch - 0.004);
    ctx.beginPath(); ctx.rect(sx(j ? xl : xl + 0.02), y - 0.004 * k, (xr - xl - (j ? 0 : 0.02)) * k, 0.008 * k); ctx.fill(); ctx.stroke();
  }
  ctx.save(); ctx.beginPath(); ctx.rect(0, sy(top), W, sy(RACK.y) - sy(top) + 2); ctx.clip(); // blocks show inside the rack only
  for (let i = first; i < n; i++) { const p = rackSlot(i - first); drawBlock(p.x, p.y, 0, word[n - 1 - i]); }
  ctx.restore();
  if (!block && !dropping && first < n && (phase === "practice" || phase === "run")) { // grab me: a pulsing ring and a nudge left
    const p = rackSlot(0), t = performance.now() / 1000, pulse = 0.5 + 0.5 * Math.sin(t * 5);
    ctx.strokeStyle = `rgba(232,185,35,${0.45 + 0.45 * pulse})`; ctx.lineWidth = 3;
    ctx.beginPath(); ctx.roundRect(sx(p.x - BW / 2) - 5, sy(p.y + BH / 2) - 5, BW * k + 10, BH * k + 10, 6); ctx.stroke();
    const ax = sx(p.x - BW / 2) - 14 - 6 * pulse, ay = sy(p.y);
    ctx.fillStyle = "#e8b923"; ctx.strokeStyle = INK; ctx.lineWidth = 1.5;
    ctx.beginPath(); ctx.moveTo(ax - 10, ay); ctx.lineTo(ax, ay - 8); ctx.lineTo(ax, ay + 8); ctx.closePath(); ctx.fill(); ctx.stroke();
  }
}
function drawRig() { // the pole (your phone) running off the bottom, its ball, the tray with its weights, the tower
  const k = scale, f = trayFrame();
  bamboo(sx(0), sy(VIEW.y0 - 0.05), sx(0), sy(STAND_TOP - BALL_R * 1.6), 0.04 * k);
  ctx.strokeStyle = INK; ctx.lineWidth = 2; // the weights' rods, hanging from the tray's ends
  [-1, 1].forEach((d) => {
    const top = f.at(d * KEEL_U, 0), wt = f.at(d * KEEL_U, KEEL_V), r = (0.018 + 0.012 * Math.sqrt(KEEL_M)) * k;
    ctx.beginPath(); ctx.moveTo(sx(top.x), sy(top.y)); ctx.lineTo(sx(wt.x), sy(wt.y)); ctx.stroke();
    ctx.fillStyle = "#5c5348"; ctx.beginPath(); ctx.arc(sx(wt.x), sy(wt.y), r, 0, 7); ctx.fill(); ctx.stroke();
  });
  const bx = sx(0), by = sy(STAND_TOP - BALL_R), r = BALL_R * k;
  const g = ctx.createRadialGradient(bx - r * 0.4, by - r * 0.4, r * 0.1, bx, by, r); g.addColorStop(0, "#d79a5e"); g.addColorStop(1, "#5b3418");
  ctx.fillStyle = g; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.arc(bx, by, r, 0, 7); ctx.fill(); ctx.stroke();
  const c = [[-TRAY_W / 2, 0], [TRAY_W / 2, 0], [TRAY_W / 2, TRAY_T], [-TRAY_W / 2, TRAY_T]].map(([u, v]) => f.at(u, v));
  ctx.fillStyle = "#a0703e"; ctx.lineWidth = 2; ctx.beginPath();
  c.forEach((p, i) => (i ? ctx.lineTo(sx(p.x), sy(p.y)) : ctx.moveTo(sx(p.x), sy(p.y)))); ctx.closePath(); ctx.fill(); ctx.stroke();
  s.stack.forEach((u, i) => { const p = f.at(u, TRAY_T + BH * (i + 0.5)); drawBlock(p.x, p.y, f.rot, word[word.length - 1 - i]); });
  if (dropping) { const p = f.at(dropping.u, dropping.v); drawBlock(p.x, p.y, f.rot, dropping.letter); }
  if (block && !block.home && placed() < word.length) { // where it would land
    const p = f.from(block.x, block.y);
    if (canLand(p)) { const g = f.at(p.u, TRAY_T + BH * placed() + BH / 2); drawBlock(g.x, g.y, f.rot, "", 0.25); }
  }
}
function draw() {
  ctx.drawImage(jungle(), 0, 0, W, H);
  drawRack(); drawRig();
  for (const b of flying) drawBlock(b.x, b.y, b.ang, b.letter);
  if (block) drawBlock(block.x, block.y, 0, block.letter);
}

let last = performance.now(), acc = 0;
const STEP = 1 / 240;
function frame(now) {
  acc += Math.min(0.05, (now - last) / 1000); last = now;
  s.want = readPhi(s.want); if (fake == null) s.hand = handNow();
  while (acc >= STEP) { step(STEP); acc -= STEP; }
  frameLogic(now);
  $("bubble").style.transform = `translateX(${-clamp(s.phi / MAX_TILT, -1, 1) * 15}px)`; // runs uphill; at its edge the pole can't swing further
  draw();
  requestAnimationFrame(frame);
}

// ---- start: phones only. iOS asks for motion access on a tap; no tilt events at all means no sensor. ----
const phone = matchMedia("(pointer: coarse)").matches && "DeviceOrientationEvent" in window;
function noGo(text) { $("how").textContent = text; document.querySelector(".flat").style.display = "none"; $("hint").textContent = ""; $("go").classList.add("hidden"); }
async function wake() { try { await navigator.wakeLock?.request("screen"); } catch {} }
document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible" && phase !== "intro") wake(); });
if (!phone && !DEBUG) noGo("Stack 'Em Up needs a phone: you balance the tower by tilting it. Open this page on your phone to play.");
$("go").onclick = async () => {
  if (!DEBUG) {
    let answer = "";
    try { [answer] = await Promise.all([DeviceOrientationEvent.requestPermission?.(), window.DeviceMotionEvent?.requestPermission?.()]); } catch { answer = "denied"; } // iOS asks; others may just say "prompt"
    if (answer === "denied") return noGo("Stack 'Em Up needs motion access. Allow it for this site, then reload.");
    const t = performance.now();
    while (!sensed && performance.now() - t < 1500) await new Promise((r) => setTimeout(r, 50));
    if (!sensed) return noGo("No tilt readings. Stack 'Em Up needs a phone with motion access allowed.");
  }
  screen.orientation?.lock?.("portrait").catch(() => {});
  wake();
  $("intro").classList.add("hidden");
  holdUpright(() => (RESUME ? resumeRun() : startPractice()));
};
$("ready").onclick = () => startCountdown();
$("level").onclick = () => { level(); pop("Level"); };
$("again").onclick = () => { $("done").classList.add("hidden"); startPractice(); };
if (ACTION) {
  $("again").textContent = "See results"; $("again").onclick = () => location.reload();
  $("hint").textContent = RESUME ? "Your run is under way: the clock kept running, and the tower fell when you left (a drop)."
    : "Scored, one run: practice up to 3 minutes, then the clock runs until SURVIVOR stands, even if you leave.";
}

if (DEBUG) { // desktop testing: arrows tilt the phone a degree at a time; the mouse stacks
  addEventListener("keydown", (e) => {
    const d = { ArrowLeft: -1, ArrowRight: 1 }[e.key];
    if (d) fake = clamp((fake ?? 0) + d * DEG, -MAX_TILT, MAX_TILT);
  });
  window.__se = { state: () => ({ phase, drops, placed: placed(), a: s.a / DEG, tray: trayTilt() / DEG, phi: s.phi / DEG, len: STICK_L, keel: KEEL_M, shake: SHAKE, stack: [...s.stack] }),
    tilt: (deg) => { fake = deg == null ? null : deg * DEG; }, rack: () => rackSlot(0), screen: (x, y) => ({ x: sx(x), y: sy(y) }),
    trayAt: (u, v) => trayFrame().at(u, v), jolt: (ms2, ms) => { s.hand = ms2; setTimeout(() => { s.hand = 0; }, ms); }, physics, toppled, s: () => s };
}
resize(); setup(true); requestAnimationFrame(frame);
if ("serviceWorker" in navigator) navigator.serviceWorker.register("/sw.js").catch(() => {});
}
