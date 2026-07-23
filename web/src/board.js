// board.js — pure-DOM chess board rendered from FEN. Zero game logic: the
// WASM core owns rules; this file only draws and reports clicks.
//
// Exposes window.Board = { render, setSelectable, onSquareClick }.
(() => {
  "use strict";

  const GLYPHS = {
    K: "♔", Q: "♕", R: "♖", B: "♗", N: "♘", P: "♙",
    k: "♚", q: "♛", r: "♜", b: "♝", n: "♞", p: "♟",
  };

  const FILES = "abcdefgh";

  let clickHandler = null;
  let selected = null;   // "e2"
  let targets = [];      // ["e3","e4"]
  let lastFen = "";
  let flipped = false;

  function squareName(fileIdx, rankIdx) {
    return FILES[fileIdx] + (rankIdx + 1);
  }

  // parse the piece-placement field of a FEN into {square: pieceChar}
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

  function render(el, fen, opts = {}) {
    lastFen = fen;
    flipped = !!opts.flipped;
    const pieces = parseFen(fen);
    el.innerHTML = "";
    for (let row = 0; row < 8; row++) {
      for (let col = 0; col < 8; col++) {
        const rankIdx = flipped ? row : 7 - row;
        const fileIdx = flipped ? 7 - col : col;
        const sq = squareName(fileIdx, rankIdx);
        const cell = document.createElement("button");
        cell.type = "button";
        cell.className = "sq " + ((fileIdx + rankIdx) % 2 ? "light" : "dark");
        cell.dataset.sq = sq;
        const piece = pieces[sq];
        if (piece) {
          cell.textContent = GLYPHS[piece];
          cell.classList.add(piece === piece.toUpperCase() ? "white-piece" : "black-piece");
        }
        if (sq === selected) cell.classList.add("selected");
        if (targets.includes(sq)) cell.classList.add("target");
        if (opts.lastMove && (sq === opts.lastMove.slice(0, 2) || sq === opts.lastMove.slice(2, 4))) {
          cell.classList.add("last-move");
        }
        cell.addEventListener("click", () => clickHandler && clickHandler(sq, piece || null));
        el.appendChild(cell);
      }
    }
  }

  window.Board = {
    render,
    // selection state is owned by app.js; board just re-renders with it
    setSelection(el, sel, tgts, opts) {
      selected = sel;
      targets = tgts || [];
      render(el, lastFen, opts);
    },
    onSquareClick(fn) { clickHandler = fn; },
    pieceIsWhite: (p) => p && p === p.toUpperCase(),
  };
})();
