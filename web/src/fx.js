// fx.js — shared presentation layer for the game modules: asset-free
// WebAudio sound (with a persisted mute), a confetti burst, a win banner,
// and small animation helpers. Registered on window.fx and loaded before the
// board modules. Everything here is pure browser — no assets, CSP-clean.
(() => {
  "use strict";

  const reducedMotion = () =>
    window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  // ---- sound --------------------------------------------------------------
  // Synthesized on demand from oscillators, so nothing is downloaded. The
  // AudioContext can only start after a user gesture, so it's created lazily
  // and resumed on each play (all our sounds fire from clicks/moves).

  let ctx = null;
  let muted = localStorage.getItem("kibitz.muted") === "1";

  function ac() {
    if (!ctx) {
      const AC = window.AudioContext || window.webkitAudioContext;
      if (!AC) return null;
      ctx = new AC();
    }
    if (ctx.state === "suspended") ctx.resume().catch(() => {});
    return ctx;
  }

  // tone plays one shaped note. freq may be [from, to] for a glide.
  function tone(freq, durMs, opts = {}) {
    if (muted) return;
    const c = ac();
    if (!c) return;
    const now = c.currentTime;
    const dur = durMs / 1000;
    const osc = c.createOscillator();
    const gain = c.createGain();
    osc.type = opts.type || "sine";
    const f0 = Array.isArray(freq) ? freq[0] : freq;
    const f1 = Array.isArray(freq) ? freq[1] : freq;
    osc.frequency.setValueAtTime(f0, now + (opts.at || 0));
    if (f1 !== f0) osc.frequency.exponentialRampToValueAtTime(f1, now + (opts.at || 0) + dur);
    const peak = opts.gain ?? 0.18;
    gain.gain.setValueAtTime(0.0001, now + (opts.at || 0));
    gain.gain.exponentialRampToValueAtTime(peak, now + (opts.at || 0) + 0.01);
    gain.gain.exponentialRampToValueAtTime(0.0001, now + (opts.at || 0) + dur);
    osc.connect(gain).connect(c.destination);
    osc.start(now + (opts.at || 0));
    osc.stop(now + (opts.at || 0) + dur + 0.02);
  }

  function chord(freqs, durMs, opts = {}) {
    freqs.forEach((f, i) => tone(f, durMs, { ...opts, at: (opts.at || 0) + i * (opts.stagger ?? 0.09) }));
  }

  const sound = {
    move: () => tone(660, 70, { type: "sine", gain: 0.12 }),
    capture: () => { tone(300, 90, { type: "triangle", gain: 0.2 }); tone(160, 130, { type: "triangle", gain: 0.18, at: 0.04 }); },
    drop: () => tone([520, 200], 260, { type: "sine", gain: 0.2 }),
    turn: () => tone(880, 120, { type: "sine", gain: 0.1 }),
    splash: () => tone([420, 240], 200, { type: "sine", gain: 0.12 }),
    hit: () => { tone(150, 180, { type: "square", gain: 0.22 }); tone([200, 60], 220, { type: "sawtooth", gain: 0.12, at: 0.02 }); },
    sunk: () => chord([180, 140, 100], 240, { type: "square", gain: 0.16, stagger: 0.07 }),
    win: () => chord([523.25, 659.25, 783.99, 1046.5], 260, { type: "triangle", gain: 0.18 }),
    lose: () => chord([392, 330, 262], 300, { type: "triangle", gain: 0.16, stagger: 0.12 }),
    toggleMute() {
      muted = !muted;
      localStorage.setItem("kibitz.muted", muted ? "1" : "0");
      if (!muted) this.turn(); // audible confirmation + unlocks the context
      return muted;
    },
    isMuted: () => muted,
  };

  // ---- confetti + win banner ---------------------------------------------

  function confetti(container) {
    if (reducedMotion() || !container) return;
    const colors = ["#7c5cff", "#f0c93c", "#e04343", "#3fbf7f", "#4aa3ff", "#f6f078"];
    const layer = document.createElement("div");
    layer.className = "confetti-layer";
    for (let i = 0; i < 60; i++) {
      const p = document.createElement("i");
      p.className = "confetti-bit";
      const x = Math.random() * 100;
      const delay = Math.random() * 0.25;
      const dur = 1.1 + Math.random() * 0.9;
      const rot = (Math.random() * 720 - 360) | 0;
      p.style.cssText =
        `left:${x}%;background:${colors[i % colors.length]};` +
        `animation-delay:${delay}s;animation-duration:${dur}s;` +
        `--rot:${rot}deg;--drift:${(Math.random() * 60 - 30) | 0}px`;
      layer.appendChild(p);
    }
    container.appendChild(layer);
    setTimeout(() => layer.remove(), 2400);
  }

  // celebrate shows the shared win banner over `container` and, if `won`,
  // fires confetti. `won` may be null for a neutral end (draw / spectator).
  function celebrate(container, won, text) {
    const banner = document.getElementById("win-banner");
    if (banner) {
      banner.textContent = text;
      banner.className = "win-banner show " + (won === true ? "win" : won === false ? "lose" : "neutral");
      clearTimeout(celebrate._t);
      celebrate._t = setTimeout(() => banner.classList.remove("show"), 4200);
      banner.onclick = () => banner.classList.remove("show");
    }
    if (won === true) confetti(container);
    if (won === true) sound.win();
    else if (won === false) sound.lose();
  }

  // ---- animation helpers --------------------------------------------------

  // slideFrom makes `el` appear to travel from (dxPx,dyPx) away to its real
  // spot — a FLIP slide that works even though the board is rebuilt each
  // render. No-op under reduced motion.
  function slideFrom(el, dxPx, dyPx) {
    if (reducedMotion() || !el) return;
    el.style.transition = "none";
    el.style.transform = `translate(${dxPx}px, ${dyPx}px)`;
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        el.style.transition = "transform 180ms ease-out";
        el.style.transform = "translate(0, 0)";
      });
    });
  }

  window.fx = { sound, confetti, celebrate, slideFrom, reducedMotion };
})();
