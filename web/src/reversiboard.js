// reversiboard.js — the reversi module: 8×8 green grid, legal-move dots,
// click to place. Passes are computed by the core; the UI just toasts them.
(() => {
  "use strict";

  window.GameModules = window.GameModules || {};
  window.GameModules.reversi = { label: "⚪ Reversi", paneId: "game-reversi", create };

  function create(ctx) {
    const { $, send, toast } = ctx;
    let g = null;
    let visible = false;
    let lastPassToasted = false;
    let flips = new Set(); // squares to flip-animate this render

    const soloMode = () => ctx.solo && ctx.solo();
    const isPlayer = () => soloMode() || (g && (g.p1Id === ctx.self() || g.p2Id === ctx.self()));
    const myTurn = () => g && (soloMode() || g.turnId === ctx.self());
    const over = () => g && g.outcome !== "";

    function render() {
      if (!visible || !g) return;
      const statusEl = $("reversi-status");
      if (!g.playing) {
        statusEl.textContent = "Waiting for the game to start…";
        return;
      }
      const score = ` · ⚫${g.black} ⚪${g.white}`;
      if (over()) {
        statusEl.textContent = g.outcome + score;
      } else if (soloMode()) {
        statusEl.textContent = `${g.turnId === g.p1Id ? "⚫" : "⚪"} to move` + score;
      } else {
        statusEl.textContent = (myTurn() ? "Your move" : ctx.name(g.turnId) + " to move") +
          score + (isPlayer() ? ` · you are ${g.p1Id === ctx.self() ? "⚫" : "⚪"}` : "");
      }
      $("reversi-resign").classList.toggle("hidden", !isPlayer() || over());

      const el = $("reversi-board");
      el.classList.toggle("my-turn", myTurn() && !over());
      el.innerHTML = "";
      const legal = new Set(myTurn() ? (g.legal || []) : []);
      for (let sq = 0; sq < 64; sq++) {
        const cell = document.createElement("button");
        cell.type = "button";
        cell.className = "rv-cell";
        const v = g.board[sq];
        if (v !== 0) {
          const disc = document.createElement("span");
          disc.className = "rv-disc " + (v > 0 ? "black" : "white");
          if (flips.has(sq)) disc.classList.add("flip");
          cell.appendChild(disc);
        }
        if (sq === g.lastSq) cell.classList.add("last", "place-pop");
        if (legal.has(sq)) {
          cell.classList.add("legal");
          cell.addEventListener("click", () => send({ type: "reversi.place", sq }));
        }
        el.appendChild(cell);
      }
      flips = new Set();
    }

    $("reversi-resign").addEventListener("click", () => {
      if (confirm("Resign reversi?")) send({ type: "reversi.resign" });
    });

    function outcomeWon() {
      if (!isPlayer() || g.outcome.startsWith("draw")) return null;
      const iAmBlack = g.p1Id === ctx.self();
      return g.outcome.startsWith(iAmBlack ? "black wins" : "white wins");
    }

    return {
      onEvent(type, e) {
        if (type !== "reversi.state") return;
        const prev = g;
        g = e;
        // Discs whose color flipped since last state get the flip animation.
        flips = new Set();
        if (prev && prev.board) {
          let flipped = false;
          for (let i = 0; i < 64; i++) {
            if (prev.board[i] !== 0 && g.board[i] !== 0 && prev.board[i] !== g.board[i]) {
              flips.add(i);
              flipped = true;
            }
          }
          const placed = prev.board[g.lastSq] === 0 && g.board[g.lastSq] !== 0;
          if ((placed || flipped) && window.fx) window.fx.sound.move();
        }
        if (g.passed && !lastPassToasted) {
          toast(myTurn() ? "Opponent had no move — you go again." : "No legal move — turn passed.");
        }
        lastPassToasted = g.passed;
        render();
        if (prev && prev.playing && prev.outcome === "" && over() && window.fx) {
          window.fx.celebrate($("game-reversi"), outcomeWon(), g.outcome);
        }
      },
      setVisible(v) { visible = v; if (v) render(); },
      card() {
        if (!g || !g.playing) return { status: "idle" };
        if (over()) return { status: "over", detail: g.outcome };
        return { status: "live", myTurn: myTurn() };
      },
    };
  }
})();
