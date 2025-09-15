**MML Retune Transpiler (Go)**

Target dialect: sakuramml-rust. Package: `github.com/anosatsuk124/mml-retune`. CLI: `mml-retune`.

Implements the emit-relative-octave variant for `TUNE{}`/`TUNE(n){}` scopes. Chord notations are ignored (no special handling for `[]` or `''`). Outside scopes are passed through unchanged.

**What It Does**
- Inside `TUNE{...}` or `TUNE(n){...}` only, user-defined tokens are mapped to absolute Hz using an A4 reference (`baseHz`), snapped to the nearest 12‑TET note, and the deviation is corrected with PB.
- Outputs relative octave marks only: prepends `<`/`>` (or `oN` with an optional threshold) to move to the target octave from the current in-scope octave.
- Immediately emits `BR(m)` at scope open: `BR(defaultBR)` for `TUNE{}`, `BR(n)` for `TUNE(n){}`. The matching `}` is removed from output.
- Only tokens and their immediate trailing `+/-` are transformed. All other MML syntax remains intact.

**Install**
- Requires Go 1.21+
- Clone the repo and build:
  - `go build ./cmd/mml-retune`
  - or `go install github.com/anosatsuk124/mml-retune/cmd/mml-retune@latest`

**CLI**
- `mml-retune [-c config.json] [--initial-octave N] [--relative-threshold K]`
  - Input: stdin, Output: stdout, Exit 0 on success.
  - `-c, --config`: Default config for `TUNE{}` / `TUNE(n){}`. Optional if you only use `TUNE("NAME"){}` with embedded configs.
  - `--initial-octave N`: Fallback octave when no left-context `oN` exists (default 5)
  - `--relative-threshold K`: If `|Δ| > K`, emit `oN` instead of repeated `<`/`>` (disabled by default)

**Embedded Configs**
- Define named configs in safe comment blocks and reference with `TUNE("NAME"){ ... }`.
- Syntax:
  ```
  /* !JSON: "NAME"
  { ... config JSON ... }
  */
  TUNE("NAME"){ ... }
  ```
- Rules:
  - `!JSON:` is mandatory; `"NAME"` must match `[A-Za-z_][A-Za-z0-9_]*`.
  - JSON schema is identical to the main config.
  - These blocks are stripped from output; only affect transformation.
  - Duplicate names or malformed blocks are errors.
  - `TUNE("NAME"){}` switches the active config and emits `BR(config.BendRangeSemitones)` at scope entry.
  - `TUNE{}` and `TUNE(n){}` use the default config passed via `-c`.

**Config JSON**
```
{
  "baseHz": 440,
  "notes": { "sa": 0, "re": -110, "ga": "220/3*2" },
  "plusHz": "55",              // optional = 0
  "minusHz": 55,                // optional = 0
  "bendRangeSemitones": 2       // default BR for TUNE{...}
}
```
- `notes[token]` is a number or arithmetic expression using `+-*/()`.
- All expressions are evaluated in `float64`.
- `baseHz > 0` required.

**Scope Syntax**
- `TUNE{ ... }` → outputs `BR(defaultBR)` immediately; the closing `}` is removed.
- `TUNE(n){ ... }` → outputs `BR(n)`; closing `}` is removed.
- Regex: `TUNE\s*(\(\s*\d+\s*\))?\s*\{`. Nested scopes are an error.

**Token Replacement**
- Matches any key in `notes` with longest‑match priority, no boundary checks.
- Counts only the immediate trailing `[+-]+` after the token as custom increments.
- Any subsequent contiguous non‑whitespace tail (length, dots, `&` etc.) is preserved after the note.

**Frequency and 12‑TET**
- `Δnote = eval(notes[token])`
- `k_plus, k_minus` = counts of trailing `+`/`-`
- `Δpm = k_plus * plusHz - k_minus * minusHz`
- `f_target = baseHz + Δnote + Δpm` (must be `> 0`)
- Nearest 12‑TET: `f(n) = baseHz * 2^(n/12)` with `n = round(12*log2(f_target/baseHz))`, tie‑break by smaller `|n|`.
- Split `n` into `pc` and `octAbs` with `A4` as `a` at `octAbs=4`. Pitch classes: `c c+ d d+ e f f+ g g+ a a+ b`.

**Relative Octave Output**
- Tracks a scope‑local `curOct`.
- Initialization: From left context at scope start:
  1) Find the last `oN` to the left; 2) Apply net `<`/`>` effect after that up to scope start; 3) If none, use `--initial-octave` (default 5).
- On each converted note: `Δ = octAbs - curOct`.
  - `Δ>0` → prepend `>` `Δ` times; `Δ<0` → prepend `<` `|Δ|` times; `Δ=0` → nothing. Then update `curOct=octAbs`.
  - Optional large‑jump optimization: if `|Δ| > K`, emit `o<octAbs>` instead of repeated `<`/`>`.
- Output order per replacement:
  - `<relOctFix>PB(pb)<pc><tail>PB(0)`

**Pitch Bend (PB)**
- Detune semitones: `Δsemitones = 12 * log2(f_target / f(n))`.
- `PB ∈ [-8192, 8191]`, where `BR(m)` maps ±m semitones to full scale.
- `pb = round(8192 * Δsemitones / BR)`; clipped to range. On clip, appends `// WARN: bend overflow` after the fragment.

**Algorithm (single‑pass)**
- State machine: `Outside` / `Inside{ br:int, curOct:int }`.
- Outside: detect `TUNE(…){` → emit `BR(…)`, init `curOct` from left context, enter Inside; otherwise copy.
- Inside: `}` → exit; `oN`/`<`/`>` update `curOct` and copy through; otherwise try notes longest‑match → evaluate, compute nearest 12‑TET, relative fix, PB, and emit replacement. Non‑matches copy through.

**Errors**
- Nested `TUNE`, unclosed `TUNE{`, extra `}`.
- `baseHz<=0`, `f_target<=0`.
- Expression errors; invalid JSON.

**Example**
Config:
```
{
  "baseHz": 440,
  "notes": { "sa": 0, "re": -110, "ga": "220/3*2" },
  "plusHz": "55",
  "minusHz": 55,
  "bendRangeSemitones": 2
}
```
Input:
```
t120 o5 l8 < a >
TUNE{
  sa++ re- ga16.
}
r4
```

**Embedded Config Example**
```
/* !JSON: "JI_WESTERN"
{
  "baseHz": 440,
  "notes": {
    "a": 0,
    "b": "440*(9/8)-440",
    "c": "440*(6/5)-440",
    "d": "440*(4/3)-440",
    "e": "440*(3/2)-440",
    "f": "440*(8/5)-440",
    "g": "440*(16/9)-440"
  },
  "plusHz": "55",
  "minusHz": 55,
  "bendRangeSemitones": 2
}
*/

TUNE("JI_WESTERN"){
  a b c d e f g
}
```
Output (values illustrative):
```
t120 o5 l8 < a >
BR(2)
PB(0)>aPB(0) <PB(-384)fPB(0) <<PB(512)d+16.PB(0)
r4
```

Note: No special handling for chords. Outside scopes pass through unchanged.

**Packages**
- `config`: config and arithmetic expression evaluator.
- `tuning`: 12‑TET helpers and PB calculation.
- `scanner`: single‑pass rewriter with longest‑match tokenization.
- `cmd/mml-retune`: CLI entrypoint.

**Quick Test**
- Build: `go build ./cmd/mml-retune`
- Run: `mml-retune -c config.json < input.mml > output.mml`
