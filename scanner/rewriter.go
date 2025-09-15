package scanner

import (
    "errors"
    "fmt"
    "regexp"
    "strings"
    "unicode"

    "github.com/anosatsuk124/mml-retune/config"
    "github.com/anosatsuk124/mml-retune/tuning"
)

type Rewriter struct {
    Cfg            *config.Config
    InitialOct     *int // nil => default 5
    RelativeThresh *int // nil => disabled; when |Δ|>K use oN
}

var (
    reTuneHdr = regexp.MustCompile(`(?i)TUNE\s*(?:\(\s*(\d+)\s*\))?\s*\{`)
    reON      = regexp.MustCompile(`o\s*(\d+)`)
)

func (rw *Rewriter) Rewrite(src string) (string, error) {
    if rw == nil || rw.Cfg == nil {
        return "", errors.New("nil rewriter or config")
    }
    if rw.Cfg.BaseHz <= 0 {
        return "", errors.New("baseHz must be > 0")
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
    bendRange := rw.Cfg.BendRange

    // Prebuild keys for longest-match
    keys := rw.Cfg.KeysDesc()

    for i < len(src) {
        switch st {
        case outside:
            // Try to match TUNE header at position i
            if loc := reTuneHdr.FindStringSubmatchIndex(src[i:]); loc != nil && loc[0] == 0 {
                // header spans i..i+loc[1]
                nStr := ""
                if loc[2] >= 0 {
                    nStr = src[i+loc[2] : i+loc[3]]
                }
                br := bendRange
                if nStr != "" {
                    // parse integer
                    var n int
                    fmt.Sscanf(nStr, "%d", &n)
                    br = n
                    if br <= 0 {
                        return "", fmt.Errorf("invalid BR in TUNE(n): %s", nStr)
                    }
                }
                // Output BR header
                out.WriteString(fmt.Sprintf("BR(%d)", br))

                // Initialize curOct from left context
                left := src[:i]
                curOct = rw.initOctaveFromLeft(left)

                // advance
                i += loc[1]
                st = inside
                // set current bend range for inside block
                bendRange = br
                continue
            }
            // else: copy one rune
            out.WriteByte(src[i])
            // unmatched closing brace here is an error per spec
            if src[i] == '}' {
                return "", errors.New("unexpected '}' outside TUNE scope")
            }
            i++

        case inside:
            // Nested TUNE detection is an error
            if loc := reTuneHdr.FindStringSubmatchIndex(src[i:]); loc != nil && loc[0] == 0 {
                return "", errors.New("nested TUNE is not allowed")
            }
            if src[i] == '}' {
                // consume and switch to outside (do not output)
                i++
                st = outside
                continue
            }
            // Handle oN update inside
            if m := reON.FindStringIndex(src[i:]); m != nil && m[0] == 0 {
                // write through and update curOct
                s := src[i : i+m[1]]
                out.WriteString(s)
                sub := reON.FindStringSubmatch(s)
                var n int
                fmt.Sscanf(sub[1], "%d", &n)
                curOct = n
                i += m[1]
                continue
            }
            // Handle '<' or '>' explicitly to track curOct
            if src[i] == '<' || src[i] == '>' {
                if src[i] == '<' { curOct-- } else { curOct++ }
                out.WriteByte(src[i])
                i++
                continue
            }

            // Try longest-match against tokens
            matched := false
            for _, k := range keys {
                if strings.HasPrefix(src[i:], k) {
                    // Found token
                    matched = true
                    token := k
                    j := i + len(token)
                    // Count immediate +/- sequence
                    kp, km := 0, 0
                    for j < len(src) {
                        if src[j] == '+' { kp++; j++ } else if src[j] == '-' { km++; j++ } else { break }
                    }
                    // Tail: contiguous non-whitespace chars after +/-; stop at whitespace or '}'
                    tailStart := j
                    for j < len(src) {
                        r := src[j]
                        if unicode.IsSpace(rune(r)) || r == '}' { break }
                        j++
                    }
                    tail := src[tailStart:j]

                    // Evaluate frequency target
                    dn, ok, err := rw.Cfg.EvalDelta(token)
                    if err != nil { return "", err }
                    if !ok { // should not happen as token came from keys
                        matched = false
                        break
                    }
                    dpm, err := rw.Cfg.EvalPM(kp, km)
                    if err != nil { return "", err }
                    fTarget := rw.Cfg.BaseHz + dn + dpm
                    if fTarget <= 0 {
                        return "", fmt.Errorf("f_target<=0 for token %s", token)
                    }

                    // Nearest 12-TET and octave split
                    n := tuning.Nearest12TET(rw.Cfg.BaseHz, fTarget)
                    pc, octAbs := tuning.SplitN(n)
                    // Relative octave fix
                    delta := octAbs - curOct
                    relFix := relativeFix(delta, rw.RelativeThresh, octAbs)
                    curOct = octAbs

                    // PB calculation
                    det := tuning.DetuneSemitones(rw.Cfg.BaseHz, fTarget, n)
                    pb, clipped := tuning.PBValue(det, bendRange)

                    // Emit
                    out.WriteString(relFix)
                    out.WriteString(fmt.Sprintf("PB(%d)", pb))
                    out.WriteString(pc)
                    out.WriteString(tail)
                    out.WriteString("PB(0)")
                    if clipped {
                        out.WriteString(" // WARN: bend overflow")
                    }

                    // advance
                    i = j
                    break
                }
            }
            if matched { continue }
            // Fallback: copy one byte
            out.WriteByte(src[i])
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
