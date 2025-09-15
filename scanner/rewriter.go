package scanner

import (
    "bytes"
    "errors"
    "fmt"
    "math"
    "regexp"
    "strings"
    "unicode"

    "github.com/anosatsuk124/mml-retune/config"
    "github.com/anosatsuk124/mml-retune/tuning"
)

type Rewriter struct {
    // Default config (used for TUNE{} and TUNE(n){})
    Cfg            *config.Config
    // Named configs loaded from embedded JSON comments
    Named          map[string]*config.Config
    InitialOct     *int // nil => default 5
    RelativeThresh *int // nil => disabled; when |Δ|>K use oN
    // Mode: when true, token's base pitch follows current octave; when false, use absolute (legacy)
    FollowOctave   bool
}

var (
    // TUNE header: optional ("NAME") or (number)
    // Captures: 1=name, 2=number
    reTuneHdr = regexp.MustCompile(`(?i)TUNE\s*(?:\(\s*(?:"([A-Za-z_][A-Za-z0-9_]*)"|(\d+))\s*\))?\s*\{`)
    reON      = regexp.MustCompile(`o\s*(\d+)`)
)

func (rw *Rewriter) Rewrite(src string) (string, error) {
    if rw == nil {
        return "", errors.New("nil rewriter")
    }

    // Prepass: extract and strip embedded JSON configs
    clean, named, err := extractEmbeddedConfigs(src)
    if err != nil {
        return "", err
    }
    // Merge into rw.Named (runtime-provided map takes precedence if keys collide?)
    // For safety, disallow collisions: if both provided and differ, error.
    if len(named) > 0 {
        if rw.Named == nil { rw.Named = map[string]*config.Config{} }
        for k, v := range named {
            if _, exists := rw.Named[k]; exists {
                return "", fmt.Errorf("duplicate embedded config name: %s", k)
            }
            rw.Named[k] = v
        }
    }

    type state int
    const (
        outside state = iota
        inside
    )
    st := outside
    i := 0
    var out strings.Builder
    curOct := 5
    if rw.InitialOct != nil { curOct = *rw.InitialOct } else { curOct = 5 }
    bendRange := 0 // will be set on entering a TUNE scope
    var activeCfg *config.Config
    braceDepth := 0

    // Prebuild keys for longest-match
    var keys []string

    for i < len(clean) {
        switch st {
        case outside:
            // Try to match TUNE header at position i
            if loc := reTuneHdr.FindStringSubmatchIndex(clean[i:]); loc != nil && loc[0] == 0 {
                // header spans i..i+loc[1]
                sub := reTuneHdr.FindStringSubmatch(clean[i:])
                name := ""
                nStr := ""
                if len(sub) >= 3 {
                    name = sub[1]
                    nStr = sub[2]
                }

                // Select config and BR
                br := bendRange
                if name != "" {
                    cfg := rw.Named[name]
                    if cfg == nil { return "", fmt.Errorf("unknown TUNE(\"%s\"): no embedded config", name) }
                    activeCfg = cfg
                    br = activeCfg.BendRange
                } else if nStr != "" {
                    // numerical BR with default config
                    activeCfg = rw.Cfg
                    if activeCfg == nil { return "", errors.New("TUNE(n){...} requires a default config (-c) but none was provided") }
                    var n int
                    fmt.Sscanf(nStr, "%d", &n)
                    br = n
                    if br <= 0 { return "", fmt.Errorf("invalid BR in TUNE(n): %s", nStr) }
                } else {
                    // bare TUNE: default config and its default BR
                    activeCfg = rw.Cfg
                    if activeCfg == nil { return "", errors.New("TUNE{...} requires a default config (-c) but none was provided") }
                    br = activeCfg.BendRange
                }

                // Output BR header with spaces around
                out.WriteString(fmt.Sprintf(" BR(%d) ", br))

                // Initialize curOct from left context
                left := clean[:i]
                curOct = rw.initOctaveFromLeft(left)

                // advance
                i += loc[1]
                st = inside
                // set current bend range for inside block
                bendRange = br
                // refresh keys for active config
                keys = activeCfg.KeysDesc()
                // initialize brace depth: we've just consumed the opening '{' of TUNE
                braceDepth = 1
                continue
            }
            // else: copy one rune
            out.WriteByte(clean[i])
            i++

        case inside:
            // Nested TUNE detection is an error
            if loc := reTuneHdr.FindStringSubmatchIndex(clean[i:]); loc != nil && loc[0] == 0 {
                return "", errors.New("nested TUNE is not allowed")
            }
            // Handle braces: maintain TUNE-scope depth; only depth==0 ends TUNE
            if clean[i] == '{' {
                braceDepth++
                out.WriteByte(clean[i])
                i++
                continue
            }
            if clean[i] == '}' {
                braceDepth--
                if braceDepth == 0 {
                    // end of TUNE scope; consume but do not output
                    i++
                    st = outside
                    continue
                }
                // inner brace close; output it
                out.WriteByte(clean[i])
                i++
                continue
            }
            // Handle oN update inside
            if m := reON.FindStringIndex(clean[i:]); m != nil && m[0] == 0 {
                // write through and update curOct
                s := clean[i : i+m[1]]
                out.WriteString(s)
                sub := reON.FindStringSubmatch(s)
                var n int
                fmt.Sscanf(sub[1], "%d", &n)
                curOct = n
                i += m[1]
                continue
            }
            // Handle '<' or '>' explicitly to track curOct
            if clean[i] == '<' || clean[i] == '>' {
                if clean[i] == '<' { curOct-- } else { curOct++ }
                out.WriteByte(clean[i])
                i++
                continue
            }

            // Try longest-match against tokens
            matched := false
            for _, k := range keys {
                if strings.HasPrefix(clean[i:], k) {
                    // Found token
                    matched = true
                    token := k
                    j := i + len(token)
                    // Count immediate +/- sequence
                    kp, km := 0, 0
                    for j < len(clean) {
                        if clean[j] == '+' { kp++; j++ } else if clean[j] == '-' { km++; j++ } else { break }
                    }
                    // Tail: contiguous non-whitespace chars after +/-; stop at whitespace or '}'
                    tailStart := j
                    for j < len(clean) {
                        r := clean[j]
                        if unicode.IsSpace(rune(r)) || r == '}' { break }
                        j++
                    }
                    tail := clean[tailStart:j]

                    // Evaluate frequency target
                    dn, ok, err := activeCfg.EvalDelta(token)
                    if err != nil { return "", err }
                    if !ok { // should not happen as token came from keys
                        matched = false
                        break
                    }
                    dpm, err := activeCfg.EvalPM(kp, km)
                    if err != nil { return "", err }
                    baseA := activeCfg.BaseHz
                    if baseA <= 0 { return "", errors.New("baseHz must be > 0") }
                    // base note frequency relative to A4
                    fNote := baseA + dn
                    var fTarget float64
                    if rw.FollowOctave {
                        // Find reference octave of this note, then shift to current octave
                        nRef := tuning.Nearest12TET(baseA, fNote)
                        _, octRef := tuning.SplitN(nRef)
                        shift := curOct - octRef
                        fAtCur := fNote * math.Pow(2, float64(shift))
                        fTarget = fAtCur + dpm
                    } else {
                        // Absolute mode (legacy)
                        fTarget = fNote + dpm
                    }
                    if fTarget <= 0 {
                        return "", fmt.Errorf("f_target<=0 for token %s", token)
                    }

                    // Nearest 12-TET and octave split
                    n := tuning.Nearest12TET(activeCfg.BaseHz, fTarget)
                    pc, octAbs := tuning.SplitN(n)
                    // Relative octave fix (compute against current textual curOct)
                    prevCur := curOct
                    delta := octAbs - prevCur
                    relFix := relativeFix(delta, rw.RelativeThresh, octAbs)

                    // PB calculation
                    det := tuning.DetuneSemitones(activeCfg.BaseHz, fTarget, n)
                    pb, clipped := tuning.PBValue(det, bendRange)

                    // Emit
                    out.WriteString(relFix)
                    out.WriteString(" ")
                    out.WriteString(fmt.Sprintf("PB(%d)", pb))
                    out.WriteString(" ")
                    out.WriteString(pc)
                    out.WriteString(tail)
                    out.WriteString(" ")
                    out.WriteString("PB(0)")
                    out.WriteString(" ")
                    if clipped {
                        out.WriteString(" // WARN: bend overflow")
                    }

                    // advance
                    i = j
                    // After emitting, update curOct to reflect only the textual tail effects
                    // (notes themselves do not change curOct; preserve author's octave context)
                    if len(tail) > 0 {
                        curOct += countChar(tail, '>')
                        curOct -= countChar(tail, '<')
                    }
                    break
                }
            }
            if matched { continue }
            // Fallback: copy one byte
            out.WriteByte(clean[i])
            i++
        }
    }

    if st == inside {
        return "", errors.New("unclosed TUNE{ ... }")
    }

    return out.String(), nil
}

func (rw *Rewriter) initOctaveFromLeft(left string) int {
    // default
    init := 5
    if rw.InitialOct != nil { init = *rw.InitialOct }
    // find last oN
    var lastIdx, lastVal = -1, init
    for _, loc := range reON.FindAllStringSubmatchIndex(left, -1) {
        lastIdx = loc[1] // end index of match
        var n int
        fmt.Sscanf(left[loc[2]:loc[3]], "%d", &n)
        lastVal = n
    }
    cur := lastVal
    start := 0
    if lastIdx >= 0 { start = lastIdx }
    // apply net effect of < and > between start and end
    for i := start; i < len(left); i++ {
        if left[i] == '<' { cur-- } else if left[i] == '>' { cur++ }
    }
    return cur
}

func relativeFix(delta int, thresh *int, octAbs int) string {
    if delta == 0 { return "" }
    if thresh != nil && abs(delta) > *thresh {
        return fmt.Sprintf("o%d", octAbs)
    }
    if delta > 0 {
        return strings.Repeat(">", delta)
    }
    return strings.Repeat("<", -delta)
}

func abs(x int) int { if x < 0 { return -x }; return x }
func countChar(s string, ch byte) int {
    c := 0
    for i := 0; i < len(s); i++ {
        if s[i] == ch { c++ }
    }
    return c
}

// ---------------------------
// Embedded config extraction
// ---------------------------

func extractEmbeddedConfigs(src string) (string, map[string]*config.Config, error) {
    if src == "" { return src, nil, nil }
    named := map[string]*config.Config{}
    var out strings.Builder
    i := 0
    for i < len(src) {
        // look for comment start
        if i+1 < len(src) && src[i] == '/' && src[i+1] == '*' {
            // find comment end
            end := strings.Index(src[i+2:], "*/")
            if end < 0 {
                return "", nil, errors.New("unterminated comment block")
            }
            end += i + 2
            content := src[i+2 : end]
            // check for !JSON:
            trimmed := strings.TrimLeft(content, " \t\r\n")
            if strings.HasPrefix(trimmed, "!JSON:") {
                rest := strings.TrimSpace(trimmed[len("!JSON:"):])
                // expect "NAME"
                if len(rest) == 0 || rest[0] != '"' {
                    return "", nil, errors.New("!JSON: expects \"NAME\" immediately after colon")
                }
                nameEnd := strings.IndexByte(rest[1:], '"')
                if nameEnd < 0 {
                    return "", nil, errors.New("!JSON: missing closing quote for name")
                }
                name := rest[1 : 1+nameEnd]
                if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(name) {
                    return "", nil, fmt.Errorf("invalid embedded config name: %q", name)
                }
                // after the name, find first '{'
                afterName := rest[1+nameEnd+1:]
                idx := strings.IndexByte(afterName, '{')
                if idx < 0 {
                    return "", nil, errors.New("!JSON: missing JSON object after name")
                }
                jsonStartInRest := 1 + nameEnd + 1 + idx
                jsonText, _, err := extractJSONObject(rest, jsonStartInRest)
                if err != nil {
                    return "", nil, err
                }
                // parse config
                cfg, err := config.Load(bytes.NewReader([]byte(jsonText)))
                if err != nil {
                    return "", nil, fmt.Errorf("embedded config %s: %w", name, err)
                }
                if _, dup := named[name]; dup {
                    return "", nil, fmt.Errorf("duplicate embedded config name: %s", name)
                }
                named[name] = cfg
                // skip emitting this comment block (remove it)
                i = end + 2
                continue
            }
            // normal comment: keep as-is
            out.WriteString(src[i : end+2])
            i = end + 2
            continue
        }
        out.WriteByte(src[i])
        i++
    }
    return out.String(), named, nil
}

// extractJSONObject expects s[start] == '{' and returns the full JSON text and end index.
func extractJSONObject(s string, start int) (text string, end int, err error) {
    if start < 0 || start >= len(s) || s[start] != '{' {
        return "", 0, errors.New("internal: extractJSONObject start is not '{'")
    }
    depth := 0
    inStr := false
    esc := false
    for i := start; i < len(s); i++ {
        c := s[i]
        if inStr {
            if esc {
                esc = false
            } else if c == '\\' {
                esc = true
            } else if c == '"' {
                inStr = false
            }
            continue
        }
        switch c {
        case '"':
            inStr = true
        case '{':
            depth++
        case '}':
            depth--
            if depth == 0 {
                return s[start : i+1], i, nil
            }
        }
    }
    return "", 0, errors.New("unterminated JSON object in embedded config")
}
