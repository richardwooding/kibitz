// board.js — the chess module: pure-DOM board rendered from FEN plus the
// chess input/render glue. Zero game logic: the WASM core owns rules; this
// file draws, reports clicks, and forwards intents.
//
// Registers itself in window.GameModules; app.js is only a router.
(() => {
  "use strict";

  const GLYPHS = {
    K: "♔", Q: "♕", R: "♖", B: "♗", N: "♘", P: "♙",
    k: "♚", q: "♛", r: "♜", b: "♝", n: "♞", p: "♟",
  };
  const FILES = "abcdefgh";

  function squareName(fileIdx, rankIdx) {
    return FILES[fileIdx] + (rankIdx + 1);
  }

  function parseFen(fen) {
    const placement = fen.split(" ")[0];
    const out = {};
    const ranks = placement.split("/");
    for (let r = 0; r < 8; r++) {
      let file = 0;
      for (const ch of ranks[r]) {
        if (ch >= "1" && ch <= "8") {
          file += Number(ch);
        } else {
          out[squareName(file, 7 - r)] = ch;
          file++;
        }
      }
    }
    return out;
  }

  function pieceCount(fen) {
    return (fen.split(" ")[0].match(/[a-zA-Z]/g) || []).length;
  }

  window.GameModules = window.GameModules || {};
  window.GameModules.chess = { label: "♞ Chess", paneId: "game-chess", create };

  function create(ctx) {
    const { $, send, toast } = ctx;
    let g = null;             // last chess.state
    let selected = null;      // selected square
    let selectedPiece = null; // FEN char on it
    let visible = false;
    let animatedUci = null;   // last move we've already slide-animated

    const isPlayer = () => g && (g.whiteId === ctx.self() || g.blackId === ctx.self());
    const myTurn = () => g && g.turnId === ctx.self() && !over();
    const over = () => g && g.outcome !== "*";
    const opts = () => ({
      flipped: g && g.blackId === ctx.self(),
      lastMove: g && g.lastUci,
    });

    function render(el, fen, o, targets) {
      const pieces = parseFen(fen);
      el.classList.toggle("my-turn", myTurn());
      el.innerHTML = "";
      for (let row = 0; row < 8; row++) {
        for (let col = 0; col < 8; col++) {
          const rankIdx = o.flipped ? row : 7 - row;
          const fileIdx = o.flipped ? 7 - col : col;
          const sq = squareName(fileIdx, rankIdx);
          const cell = document.createElement("button");
          cell.type = "button";
          cell.className = "sq " + ((fileIdx + rankIdx) % 2 ? "light" : "dark");
          cell.dataset.sq = sq;
          const piece = pieces[sq];
          if (piece) {
            const gl = document.createElement("span");
            gl.className = "slide-piece";
            gl.textContent = GLYPHS[piece];
            cell.appendChild(gl);
            cell.classList.add(piece === piece.toUpperCase() ? "white-piece" : "black-piece");
          }
          // Coordinate labels along the bottom rank and left file (Lichess-style).
          if (row === 7) cell.appendChild(coord("file", FILES[fileIdx]));
          if (col === 0) cell.appendChild(coord("rank", String(rankIdx + 1)));
          if (sq === selected) cell.classList.add("selected");
          if (targets && targets.includes(sq)) cell.classList.add("target");
          if (o.lastMove && (sq === o.lastMove.slice(0, 2) || sq === o.lastMove.slice(2, 4))) {
            cell.classList.add("last-move");
          }
          cell.addEventListener("click", () => onSquare(sq, piece || null));
          el.appendChild(cell);
        }
      }
    }

    function coord(kind, text) {
      const s = document.createElement("span");
      s.className = "coord " + kind;
      s.textContent = text;
      return s;
    }

    // slideLastMove animates the moved piece from its origin square to its
    // destination, after the board has been (re)built.
    function slideLastMove(el, uci) {
      if (!window.fx || !uci || uci.length < 4) return;
      const from = uci.slice(0, 2), to = uci.slice(2, 4);
      const fromCell = el.querySelector(`.sq[data-sq="${from}"]`);
      const toPiece = el.querySelector(`.sq[data-sq="${to}"] .slide-piece`);
      if (!fromCell || !toPiece) return;
      const a = fromCell.getBoundingClientRect();
      const b = toPiece.closest(".sq").getBoundingClientRect();
      window.fx.slideFrom(toPiece, a.left - b.left, a.top - b.top);
    }

    function onSquare(sq, piece) {
      if (!g || !g.playing || over() || !isPlayer()) return;
      if (selected && selected !== sq) {
        const wasTarget = [...document.querySelectorAll("#board .sq.target")]
          .some((c) => c.dataset.sq === sq);
        if (wasTarget) {
          if (!myTurn()) { toast("Not your turn."); return; }
          const promo = (selectedPiece === "P" && sq[1] === "8") ||
                        (selectedPiece === "p" && sq[1] === "1") ? "q" : "";
          send({ type: "chess.move", uci: selected + sq + promo });
          selected = null;
          selectedPiece = null;
          render($("board"), g.fen, opts(), []);
          return;
        }
      }
      const mineIsWhite = g.whiteId === ctx.self();
      if (piece && (piece === piece.toUpperCase()) === mineIsWhite) {
        selected = sq;
        selectedPiece = piece;
        send({ type: "chess.targets", from: sq, id: Date.now() });
      } else {
        selected = null;
        selectedPiece = null;
        render($("board"), g.fen, opts(), []);
      }
    }

    function renderPane() {
      if (!visible || !g) return;
      const el = $("status-line");
      if (!g.playing) {
        el.textContent = "Waiting for the game to start…";
        return;
      }
      if (over()) {
        const result = g.outcome === "1/2-1/2" ? "Draw" :
          (g.outcome === "1-0" ? "White wins" : "Black wins");
        el.textContent = `${result} — ${g.method}`;
      } else {
        el.textContent = (g.turnId === ctx.self() ? "Your move" :
          ctx.name(g.turnId) + " to move") +
          (isPlayer() ? "" : " (you're kibitzing)");
      }
      $("btn-resign").classList.toggle("hidden", !isPlayer() || over());
      $("btn-draw").classList.toggle("hidden", !isPlayer() || over());
      selected = null;
      selectedPiece = null;
      render($("board"), g.fen, opts(), []);
    }

    // one-time control wiring
    $("btn-resign").addEventListener("click", () => {
      if (confirm("Resign the game?")) send({ type: "chess.resign" });
    });
    $("btn-draw").addEventListener("click", () => send({ type: "chess.offerDraw" }));
    $("btn-agree-draw").addEventListener("click", () => send({ type: "chess.agreeDraw" }));

    function outcomeWon() {
      if (!isPlayer() || g.outcome === "1/2-1/2") return null;
      return g.outcome === (g.whiteId === ctx.self() ? "1-0" : "0-1");
    }

    return {
      onEvent(type, e) {
        switch (type) {
          case "chess.state": {
            const prev = g;
            g = e;
            $("btn-agree-draw").classList.add("hidden");
            // Move/capture sound + slide when a new move landed.
            if (window.fx && g.lastUci && g.lastUci !== animatedUci && prev && prev.fen) {
              const captured = pieceCount(g.fen) < pieceCount(prev.fen);
              captured ? window.fx.sound.capture() : window.fx.sound.move();
            }
            renderPane();
            if (visible && g.lastUci && g.lastUci !== animatedUci) {
              slideLastMove($("board"), g.lastUci);
            }
            animatedUci = g.lastUci;
            if (prev && prev.playing && prev.outcome === "*" && over() && window.fx) {
              const result = g.outcome === "1/2-1/2" ? "Draw" :
                (g.outcome === "1-0" ? "White wins" : "Black wins");
              window.fx.celebrate($("game-chess"), outcomeWon(), `${result} — ${g.method}`);
            }
            break;
          }
          case "chess.targets":
            if (visible && e.from === selected) {
              render($("board"), g.fen, opts(), e.targets);
            }
            break;
          case "chess.drawOffered":
            if (isPlayer()) {
              $("btn-agree-draw").classList.remove("hidden");
              toast("Draw offered — accept?");
            } else {
              toast("A draw was offered.");
            }
            break;
        }
      },
      setVisible(v) { visible = v; if (v) renderPane(); },
      card() {
        if (!g || !g.playing) return { status: "idle" };
        if (over()) {
          const result = g.outcome === "1/2-1/2" ? "Draw" :
            (g.outcome === "1-0" ? "1-0" : "0-1");
          return { status: "over", detail: result };
        }
        return { status: "live", myTurn: myTurn() };
      },
    };
  }
})();
