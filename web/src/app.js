// Thin view layer. All protocol, crypto, and game logic lives in the Go WASM
// core (kibitz.wasm); this file only renders state and forwards user intents
// via the two-function bridge:
//   window.kibitz_send(json)  — UI → core   (installed by the WASM core)
//   window.kibitzOnEvent(json) — core → UI  (defined here)
//
// M0 stub: load the core if present; the real UI arrives in M1-8.

window.kibitzOnEvent = (json) => {
  console.log("kibitz event:", json);
};

(async () => {
  if (typeof Go === "undefined") return; // wasm_exec.js not served yet
  try {
    const go = new Go();
    const result = await WebAssembly.instantiateStreaming(
      fetch("kibitz.wasm"),
      go.importObject,
    );
    go.run(result.instance);
  } catch (err) {
    console.warn("kibitz.wasm not available:", err);
  }
})();
