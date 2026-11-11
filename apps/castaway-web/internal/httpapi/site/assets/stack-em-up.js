{ // own block: page scripts share one global scope
// Stack 'Em Up, side view, phone held upright. A tray rests on two balls in a cup on a fixed stand; the phone is the
// balancing device: tilting it tilts the stand. One finger pulls letter blocks from a slotted rack (each one you
// pull lets the next drop down) and stacks them on the tray until the tower reads SURVIVOR from the top. A short
// stack rights itself, but once its weight sits higher than the cup's curve the tray tips unless you tilt the
// phone against it. Tip too far or stack a block past the edge of the one below, and the tower falls: every block
// goes back to the rack (a drop).
const DEG = Math.PI / 180;
const BW = 0.1, BH = 0.09;                // a block (m)
const TRAY_W = 0.36, TRAY_T = 0.025;      // the tray
let CRADLE_R = 0.08;                      // the tray rocks on the balls as if on a rocker this round: small, so it wobbles
const STAND_TOP = 0.42;                   // where the tray rests
const M_TRAY = 1.5, M_BLOCK = 1;
let G = 4.5;                              // slowed down: real gravity tips a 0.7 m tower too fast to catch
let DAMP = 4;                             // rocking friction (1/s)
let SLOSH = 0.3;                          // how much the balls rolling in the cup throws the tray
let CUP_R = 0.1, CUP_DAMP = 35;            // the cup the balls roll in, and their rolling friction (1/s)
let CUP_LIP = 24 * DEG;                   // past this the outer ball reaches the drawn rim (RIM_A) and rolls over, tray and all
const BALL_R = 0.028;
const MAX_TILT = 40 * DEG, DEAD = 0.5 * DEG; // phone roll used, and ignored jitter
let FALL_TILT = 20 * DEG;                 // the tower starts sliding past this, from level in the world (wood on wood)
const HOLD_S = 3, PRACTICE_S = 180;
const SCORED_WORD = "SURVIVOR", PRACTICE_WORD = "ISLAND";
const RACK = { x: 0.34, y: 0.8, pitch: 0.1 }; // the slotted rack, top right: slot 0 (the one you pull) at the bottom
const VIEW = { x0: -0.32, x1: 0.46, y0: 0, y1: 1.62 };
const DEBUG = location.search.includes("debug");
// Scored runs: the server puts data-action on <body>, and data-resume (drops, elapsed ms, and the height the
// stack had) when the player already started. The clock is the server's.
const DATA = document.body.dataset, ACTION = DATA.action || "", RESUME = DATA.resume === "1";
if (!ACTION) { // free play only: ?r= ?cupr= ?cupdamp= ?slosh= ?lip= ?fall= ?g= ?damp= to tune on a phone without a deploy
  const q = new URLSearchParams(location.search), n = (k) => (q.has(k) && Number.isFinite(+q.get(k)) ? +q.get(k) : null);
  CRADLE_R = n("r") ?? CRADLE_R; CUP_R = n("cupr") ?? CUP_R; CUP_DAMP = n("cupdamp") ?? CUP_DAMP; SLOSH = n("slosh") ?? SLOSH;
  CUP_LIP = (n("lip") ?? CUP_LIP / DEG) * DEG; FALL_TILT = (n("fall") ?? FALL_TILT / DEG) * DEG; G = n("g") ?? G; DAMP = n("damp") ?? DAMP;
}

// <physics> (pure: no page state; a self-check can evaluate this part alone)
// Tray-frame parts: u along the tray from its middle, v up from its underside.
function parts(stack) {
  const out = [{ m: M_TRAY, u: 0, v: TRAY_T / 2, i: M_TRAY * (TRAY_W ** 2 + TRAY_T ** 2) / 12 }];
  stack.forEach((x, k) => out.push({ m: M_BLOCK, u: x, v: TRAY_T + BH * (k + 0.5), i: M_BLOCK * (BW ** 2 + BH ** 2) / 12 }));
  return out;
}
// One step. s: { psi (where the balls sit in the cup, as an angle from its bottom, + is right), psiV, th (tray tilt
// on the balls, + is right side down), thV, phi (the phone's roll, + is right side down), stack (block offsets
// along the tray) }. The balls roll in the cup toward wherever gravity points; the tray rocks on them as on a curve
// of radius CRADLE_R, so the contact is CRADLE_R·th along the tray, and gravity turns the tray about the contact.
// The tray lags the balls (SLOSH, less as it gets heavier), so they throw it about. Shortcut: its weight doesn't push the balls back.
function physics(s, dt) {
  const kick = ((G / CUP_R) * Math.sin(s.phi - s.psi) - CUP_DAMP * s.psiV) * dt;
  s.psiV += kick; s.psi += s.psiV * dt;
  const contact = CRADLE_R * s.th, a = s.th - s.psi + s.phi; // a: the tray's tilt from level in the world
  let M = 0, I = 0, mu = 0, mv = 0;
  for (const p of parts(s.stack)) { M += p.m; mu += p.m * p.u; mv += p.m * p.v; I += p.i + p.m * ((p.u - contact) ** 2 + p.v ** 2); }
  s.thV += SLOSH * kick * M_TRAY / M; // a light, empty tray gets thrown about; a loaded one sits heavier
  const lever = (mu / M - contact) * Math.cos(a) + (mv / M) * Math.sin(a); // weight's offset from the contact
  s.thV += (M * G * lever / I - DAMP * s.thV) * dt;
  s.th += s.thV * dt;
  return contact;
}
// toppled: the first block (0 = bottom) whose blocks above have their weight past its edge, or -1. The tray is "block" -1's
// support, wide as the tray.
function toppled(stack, a = 0) { // a: the tray's tilt from level in the world; a tall tower goes over at a small one
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
let gone = null; // the tray and balls after they rolled off the stand: { x, y, vx, vy, ang, spin, t }
let block = null, lift = 0, liftV = 0; // the block in hand ({ id, letter, x, y, tx, ty, home }); the rack's drop
function setup(practice) {
  word = practice ? PRACTICE_WORD : SCORED_WORD;
  s = { psi: 0, psiV: 0, th: 0, thV: 0, sv: 0, phi: s?.phi ?? 0, stack: [] }; gone = null;
  flying = []; dropping = null; block = null; lift = 0; holdFrom = 0; hud();
}
const placed = () => s.stack.length;
const nextLetter = () => word[word.length - 1 - placed()];
const falling = () => flying.filter((b) => b.down).length; // blocks of a fallen tower not yet on the ground
const rackFirst = () => placed() + falling() + (dropping ? 1 : 0) + (block && !block.home ? 1 : 0); // placing index in slot 0
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
// The balls' tops sit on a circle inside the cup; the tray rocks on them and touches them at `contact` along its
// underside. rot: the tray's tilt on screen (+ is right side down).
const CUP_Y = STAND_TOP - 2 * BALL_R - 0.006; // the cup's floor
function trayFrame(st = s) {
  const s = st, sp = Math.sin(s.psi), cp = Math.cos(s.psi), c = Math.cos(s.th), n = Math.sin(s.th);
  const bx = CUP_R * sp - sp * (STAND_TOP - CUP_Y), by = CUP_Y + CUP_R * (1 - cp) + cp * (STAND_TOP - CUP_Y); // the balls' tops
  const contact = CRADLE_R * s.th;
  const lx = CRADLE_R * n - contact * c, ly = -CRADLE_R + CRADLE_R * c + contact * n; // the tray's middle, in the balls' frame
  const qx = bx + lx * cp - ly * sp, qy = by + lx * sp + ly * cp;
  const rot = s.th - s.psi, rc = Math.cos(rot), rn = Math.sin(rot);
  return { contact, rot, at: (u, v) => ({ x: qx + u * rc + v * rn, y: qy - u * rn + v * rc }),
    from: (x, y) => { const dx = x - qx, dy = y - qy; return { u: dx * rc - dy * rn, v: dx * rn + dy * rc }; } };
}

// The tray's underside can't pass the cup's rim: pushed back out, it bounces. Returns whether it touched.
const RIM_A = 50 * DEG; // the cup is drawn this far round from its bottom, each side
function hitRim(st) {
  let hit = false;
  for (const d of [-1, 1]) {
    const f = trayFrame(st), R = CUP_R + 0.014, r = f.from(d * R * Math.sin(RIM_A), CUP_Y + CUP_R - R * Math.cos(RIM_A)); // the rim's outer corner
    if (r.v <= 0 || Math.abs(r.u) > TRAY_W / 2 || Math.sign(r.u) !== d) continue;
    st.th -= d * r.v / Math.abs(r.u); // turn that side back up, clear of the rim
    if (st.thV * d > 0) st.thV *= -0.3;
    hit = true;
  }
  return hit;
}

// ---- input: one finger pulls the bottom block out of the rack (anywhere on the rack grabs it) ----
canvas.addEventListener("pointerdown", (e) => {
  if (!(phase === "practice" || phase === "run") || block || dropping || falling() || gone || placed() >= word.length) return;
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
  if (gone) return false;
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
  if (placed() > 0 ? Math.abs(d.u - s.stack[placed() - 1]) > BW / 2 : Math.abs(d.u) > TRAY_W / 2) {
    const w = trayFrame().at(d.u, TRAY_T + BH * (placed() + 0.5));
    flying.push({ letter: d.letter, x: w.x, y: w.y, vx: Math.sign(d.u - (s.stack.at(-1) ?? 0)) * 0.6, vy: 0.4, ang: trayFrame().rot, spin: 5 });
    lift = 1; liftV = 0; pop("Missed!"); return;
  }
  s.stack.push(d.u); navigator.vibrate?.(15);
  s.thV += (d.u - CRADLE_R * s.th) * 0.8; // the knock of setting it down
  if (toppled(s.stack, s.th - s.psi + s.phi) >= 0) return fall("Toppled!");
  hud(); report();
  if (placed() === word.length) { holdFrom = performance.now(); pop("Hold it!"); }
  else pop(word.slice(word.length - placed()));
}
// The tower falls: its blocks fly off the tray. Once they're all on the ground they go back to the rack, the tray is
// set flat, and in a run that's a drop (see landed).
function fall(why) {
  if (!s.stack.length) return;
  const f = trayFrame(), lean = Math.sign(s.th - s.psi + s.phi) || 1;
  s.stack.forEach((u, k) => {
    const w = f.at(u, TRAY_T + BH * (k + 0.5)), r = TRAY_T + BH * (k + 0.5);
    const slid = s.sv * Math.cos(f.rot); // a sliding tower just carries on off the end; a toppling one is thrown
    flying.push({ letter: word[word.length - 1 - k], x: w.x, y: w.y, vx: s.thV * r * Math.cos(s.th) + (slid || lean * 0.8 + (k - 3) * 0.05),
      vy: slid ? -s.sv * Math.sin(f.rot) : 0.3 + 0.1 * k, ang: f.rot, spin: lean * (slid ? 1 : 2 + k), down: true });
  });
  s.stack = []; s.sv = 0; holdFrom = 0; navigator.vibrate?.([40, 30, 40]);
  pop(why); hud();
}
function landed() { // the fallen tower is all on the ground
  flying = flying.filter((b) => !b.down); lift = 1; liftV = 0;
  if (!gone) s.th = s.thV = s.psi = s.psiV = 0; // a fresh start: the tray sits flat, the balls at the bottom of the cup
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
  if (gone) { // off the stand: it falls, then a fresh tray is set on the balls
    gone.vx += gx * dt; gone.vy += gy * dt; gone.x += gone.vx * dt; gone.y += gone.vy * dt; gone.ang += gone.spin * dt;
    if (gone.y < 0.03 || (gone.t -= dt) <= 0) { gone = null; s.psi = s.psiV = s.th = s.thV = 0; } // on the ground
    return stepRest(dt, gx, gy);
  }
  if (Math.abs(s.psi) > CUP_LIP) { // over the rim: balls, tray and tower all go
    const f = trayFrame(), m = f.at(0, 0), side = Math.sign(s.psi);
    const v = s.psiV * CUP_R;
    fall("Off the stand!");
    gone = { x: m.x, y: m.y, vx: v + side * 0.3, vy: 0.2, ang: f.rot, spin: side * 3, t: 4 };
    if (phase !== "run") pop("Off the stand!");
    return stepRest(dt, gx, gy);
  }
  if (!(phase === "practice" || phase === "run")) { s.th = s.thV = s.psi = s.psiV = 0; return stepRest(dt, gx, gy); } // the tray waits, flat, until play
  const contact = physics(s, dt), tilt = s.th - s.psi + s.phi;
  if (s.stack.length) { // past FALL_TILT the tower slides down the tray (friction holds it below); off the end, it falls
    const mu = Math.tan(FALL_TILT), g = Math.sin(tilt), n = Math.cos(tilt);
    if (s.sv || Math.abs(g) > mu * n) {
      const v = s.sv + G * (g - mu * n * Math.sign(s.sv || g)) * dt;
      s.sv = s.sv && Math.sign(v) !== Math.sign(s.sv) ? 0 : v; // friction stops it, never pushes it back
      s.stack = s.stack.map((u) => u + s.sv * dt);
    }
  }
  if (s.stack.length && (Math.abs(s.stack[0]) > TRAY_W / 2 || Math.abs(contact) > TRAY_W / 2 - 0.02)) fall(Math.abs(contact) > TRAY_W / 2 - 0.02 ? "Off the balls!" : "Slid off!");
  else if (toppled(s.stack, tilt) >= 0) fall("Toppled!");
  else if (hitRim(s) && s.stack.length) fall("Hit the bowl!");
  if (!s.stack.length) { // an empty tray never falls off: it settles back over the balls
    if (Math.abs(contact) > TRAY_W / 2 - 0.02) s.th -= (contact - Math.sign(contact) * (TRAY_W / 2 - 0.02)) / CRADLE_R;
    s.th = clamp(s.th, -FALL_TILT, FALL_TILT);
  }
  if (dropping) {
    dropping.vv -= 9.8 * dt; dropping.v += dropping.vv * dt;
    if (dropping.v <= TRAY_T + BH * (placed() + 0.5)) land(dropping);
  }
  stepRest(dt, gx, gy);
}
function stepRest(dt, gx, gy) { // blocks in the air
  for (const b of flying) {
    if (b.rest) continue;
    b.vx += gx * dt; b.vy += gy * dt; b.x += b.vx * dt; b.y += b.vy * dt; b.ang += b.spin * dt;
    if (b.down && b.y < BH / 2) { b.y = BH / 2; b.rest = true; } // hit the sand
    if (b.down && (b.x < VIEW.x0 - 0.3 || b.x > VIEW.x1 + 0.3)) b.rest = true; // off the side: as good as landed
  }
  flying = flying.filter((b) => b.down || (b.y > VIEW.y0 - 0.3 && b.x > VIEW.x0 - 0.3 && b.x < VIEW.x1 + 0.3));
  if (falling() && flying.every((b) => !b.down || b.rest)) landed();
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
function drawTray(at, ang) { // the tray, its middle-underneath at `at`, and its two balls underneath
  const k = scale, br = BALL_R, roll = (CRADLE_R * s.th + CUP_R * s.psi) / br; // the balls roll as things move over them
  ctx.save(); ctx.translate(sx(at.x), sy(at.y)); ctx.rotate(ang);
  [-1, 1].forEach((side) => {
    const x = side * br * 1.15 * k, y = br * k, r = br * k;
    const g = ctx.createRadialGradient(x - r * 0.4, y - r * 0.4, r * 0.1, x, y, r); g.addColorStop(0, "#d79a5e"); g.addColorStop(1, "#5b3418");
    ctx.fillStyle = g; ctx.strokeStyle = INK; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.arc(x, y, r, 0, 7); ctx.fill(); ctx.stroke();
    ctx.strokeStyle = "rgba(43,24,10,.5)"; ctx.beginPath(); ctx.moveTo(x, y); ctx.lineTo(x + Math.cos(roll) * r, y + Math.sin(roll) * r); ctx.stroke();
  });
  ctx.fillStyle = "#a0703e"; ctx.strokeStyle = INK; ctx.lineWidth = 2;
  ctx.beginPath(); ctx.rect(-TRAY_W / 2 * k, -TRAY_T * k, TRAY_W * k, TRAY_T * k); ctx.fill(); ctx.stroke();
  ctx.restore();
}
function drawRig() { // the stand, the cup, then the balls, the tray and the tower unless they rolled off
  const k = scale, ccx = sx(0), ccy = sy(CUP_Y + CUP_R), A = Math.PI / 2, w = RIM_A;
  bamboo(sx(0), sy(0.02), sx(0), sy(CUP_Y - 0.02), 0.045 * k);
  ctx.fillStyle = "#8a5d34"; ctx.strokeStyle = INK; ctx.lineWidth = 1.5; // the foot
  ctx.beginPath(); ctx.roundRect(sx(-0.09), sy(0.05), 0.18 * k, 0.035 * k, 4); ctx.fill(); ctx.stroke();
  ctx.fillStyle = "#6b4423"; // the cup: a round bowl, open on top
  ctx.beginPath(); ctx.arc(ccx, ccy, CUP_R * k, A - w, A + w); ctx.arc(ccx, ccy, (CUP_R + 0.014) * k, A + w, A - w, true); ctx.closePath(); ctx.fill(); ctx.stroke();
  if (gone) return drawTray(gone, gone.ang);
  const f = trayFrame();
  drawTray(f.at(0, 0), f.rot);
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
  s.phi = readPhi(s.phi);
  while (acc >= STEP) { step(STEP); acc -= STEP; }
  frameLogic(now);
  $("bubble").style.transform = `translateX(${-clamp(s.phi / CUP_LIP, -1, 1) * 15}px)`; // runs uphill; at the edge, over the rim
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
    try { answer = await DeviceOrientationEvent.requestPermission?.(); } catch { answer = "denied"; } // iOS asks; others may just say "prompt"
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
  window.__se = { state: () => ({ phase, drops, placed: placed(), th: s.th / DEG, phi: s.phi / DEG, stack: [...s.stack] }),
    tilt: (deg) => { fake = deg == null ? null : deg * DEG; }, rack: () => rackSlot(0), screen: (x, y) => ({ x: sx(x), y: sy(y) }),
    tray: (u, v) => trayFrame().at(u, v), physics, toppled, s: () => s };
}
resize(); setup(true); requestAnimationFrame(frame);
if ("serviceWorker" in navigator) navigator.serviceWorker.register("/sw.js").catch(() => {});
}
