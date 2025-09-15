package config

import (
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "math"
    "strconv"
    "sort"
    "strings"
)

// Config represents the JSON configuration for mml-retune.
// PlusHz and MinusHz accept either a number or an arithmetic expression string.
type Config struct {
    BaseHz    float64            `json:"baseHz"`
    Notes     map[string]string  `json:"notes"`
    PlusHzRaw any                `json:"plusHz,omitempty"`
    MinusHzRaw any               `json:"minusHz,omitempty"`
    BendRange int                `json:"bendRangeSemitones"`

    // derived
    plusExpr  string
    minusExpr string
    keysDesc  []string
}

// Load parses JSON from r into Config and performs basic validation and normalization.
func Load(r io.Reader) (*Config, error) {
    dec := json.NewDecoder(r)
    dec.UseNumber()
    var raw map[string]any
    if err := dec.Decode(&raw); err != nil {
        return nil, fmt.Errorf("invalid JSON: %w", err)
    }

    cfg := &Config{}
    // baseHz
    if v, ok := raw["baseHz"]; ok {
        f, err := asFloat64(v)
        if err != nil {
            return nil, fmt.Errorf("baseHz: %w", err)
        }
        cfg.BaseHz = f
    }
    if cfg.BaseHz <= 0 {
        return nil, errors.New("baseHz must be > 0")
    }

    // bendRangeSemitones
    if v, ok := raw["bendRangeSemitones"]; ok {
        f, err := asFloat64(v)
        if err != nil {
            return nil, fmt.Errorf("bendRangeSemitones: %w", err)
        }
        cfg.BendRange = int(math.Round(f))
        if cfg.BendRange <= 0 {
            return nil, errors.New("bendRangeSemitones must be > 0")
        }
    } else {
        // default to 2 if not provided, a common PB range
        cfg.BendRange = 2
    }

    // notes
    nsRaw, ok := raw["notes"]
    if !ok {
        return nil, errors.New("notes is required")
    }
    nsMap, ok := nsRaw.(map[string]any)
    if !ok {
        return nil, errors.New("notes must be an object of token->expr")
    }
    cfg.Notes = make(map[string]string, len(nsMap))
    for k, v := range nsMap {
        if k == "" { // ignore empty keys
            continue
        }
        switch vv := v.(type) {
        case string:
            cfg.Notes[k] = strings.TrimSpace(vv)
        default:
            f, err := asFloat64(v)
            if err != nil {
                return nil, fmt.Errorf("notes[%s]: %w", k, err)
            }
            cfg.Notes[k] = trimFloat(f)
        }
    }
    if len(cfg.Notes) == 0 {
        return nil, errors.New("notes must not be empty")
    }
    // keys sorted by descending length for longest-match
    cfg.keysDesc = make([]string, 0, len(cfg.Notes))
    for k := range cfg.Notes {
        cfg.keysDesc = append(cfg.keysDesc, k)
    }
    sort.Slice(cfg.keysDesc, func(i, j int) bool {
        if len(cfg.keysDesc[i]) == len(cfg.keysDesc[j]) {
            return cfg.keysDesc[i] < cfg.keysDesc[j]
        }
        return len(cfg.keysDesc[i]) > len(cfg.keysDesc[j])
    })

    // plus/minus
    if v, ok := raw["plusHz"]; ok {
        cfg.plusExpr = normalizeExprAny(v)
    } else {
        cfg.plusExpr = "0"
    }
    if v, ok := raw["minusHz"]; ok {
        cfg.minusExpr = normalizeExprAny(v)
    } else {
        cfg.minusExpr = "0"
    }

    return cfg, nil
}

// KeysDesc returns notes keys sorted by descending length (longest-match first).
func (c *Config) KeysDesc() []string { return c.keysDesc }

// EvalDelta returns Δnote for a token.
func (c *Config) EvalDelta(token string) (float64, bool, error) {
    expr, ok := c.Notes[token]
    if !ok {
        return 0, false, nil
    }
    v, err := ParseExpr(expr)
    if err != nil {
        return 0, true, fmt.Errorf("eval notes[%s]: %w", token, err)
    }
    return v, true, nil
}

// EvalPM computes Δpm = kp*plusHz - km*minusHz using configured expressions.
func (c *Config) EvalPM(kp, km int) (float64, error) {
    plus, err := ParseExpr(c.plusExpr)
    if err != nil {
        return 0, fmt.Errorf("plusHz: %w", err)
    }
    minus, err := ParseExpr(c.minusExpr)
    if err != nil {
        return 0, fmt.Errorf("minusHz: %w", err)
    }
    return float64(kp)*plus - float64(km)*minus, nil
}

// asFloat64 converts numbers or numeric-strings to float64.
func asFloat64(v any) (float64, error) {
    switch t := v.(type) {
    case json.Number:
        f, err := t.Float64()
        if err != nil {
            return 0, err
        }
        return f, nil
    case float64:
        return t, nil
    case float32:
        return float64(t), nil
    case int:
        return float64(t), nil
    case int64:
        return float64(t), nil
    case int32:
        return float64(t), nil
    case string:
        // try parse as expression (single number or expr)
        s := strings.TrimSpace(t)
        if s == "" {
            return 0, errors.New("empty string")
        }
        val, err := ParseExpr(s)
        if err != nil {
            return 0, err
        }
        return val, nil
    default:
        return 0, fmt.Errorf("unsupported number type %T", v)
    }
}

// normalizeExprAny converts any acceptable JSON value to an expression string.
func normalizeExprAny(v any) string {
    switch t := v.(type) {
    case json.Number:
        f, _ := t.Float64()
        return trimFloat(f)
    case float64:
        return trimFloat(t)
    case float32:
        return trimFloat(float64(t))
    case int:
        return trimFloat(float64(t))
    case int64:
        return trimFloat(float64(t))
    case int32:
        return trimFloat(float64(t))
    case string:
        return strings.TrimSpace(t)
    default:
        return "0"
    }
}

func trimFloat(f float64) string {
    s := fmt.Sprintf("%f", f)
    s = strings.TrimRight(s, "0")
    s = strings.TrimRight(s, ".")
    if s == "" || s == "-0" {
        return "0"
    }
    return s
}

// --------------------
// Expression evaluator
// --------------------

// token type for the expression evaluator
type tok struct{ kind, val string }

// ParseExpr evaluates a simple arithmetic expression containing numbers,
// + - * / and parentheses. No identifiers or functions.
func ParseExpr(s string) (float64, error) {
    // Shunting-yard to RPN, then eval
    toks, err := tokenize(s)
    if err != nil {
        return 0, err
    }
    // to RPN
    var out []tok
    var opstack []tok
    prec := func(op string) int {
        switch op {
        case "+", "-":
            return 1
        case "*", "/":
            return 2
        default:
            return 0
        }
    }
    for _, t := range toks {
        switch t.kind {
        case "num":
            out = append(out, t)
        case "op":
            for len(opstack) > 0 {
                top := opstack[len(opstack)-1]
                if top.kind == "op" && prec(top.val) >= prec(t.val) {
                    out = append(out, top)
                    opstack = opstack[:len(opstack)-1]
                } else {
                    break
                }
            }
            opstack = append(opstack, t)
        case "lpar":
            opstack = append(opstack, t)
        case "rpar":
            matched := false
            for len(opstack) > 0 {
                top := opstack[len(opstack)-1]
                opstack = opstack[:len(opstack)-1]
                if top.kind == "lpar" {
                    matched = true
                    break
                }
                out = append(out, top)
            }
            if !matched {
                return 0, errors.New("mismatched parentheses")
            }
        }
    }
    for i := len(opstack) - 1; i >= 0; i-- {
        if opstack[i].kind == "lpar" || opstack[i].kind == "rpar" {
            return 0, errors.New("mismatched parentheses")
        }
        out = append(out, opstack[i])
    }

    // eval RPN
    var st []float64
    pop := func() (float64, error) {
        if len(st) == 0 {
            return 0, errors.New("invalid expression")
        }
        v := st[len(st)-1]
        st = st[:len(st)-1]
        return v, nil
    }
    for _, t := range out {
        switch t.kind {
        case "num":
            f, err := strconvParseFloat(t.val)
            if err != nil {
                return 0, err
            }
            st = append(st, f)
        case "op":
            b, err := pop()
            if err != nil { return 0, err }
            a, err := pop()
            if err != nil { return 0, err }
            switch t.val {
            case "+": st = append(st, a+b)
            case "-": st = append(st, a-b)
            case "*": st = append(st, a*b)
            case "/":
                if b == 0 { return 0, errors.New("division by zero") }
                st = append(st, a/b)
            }
        default:
            return 0, errors.New("invalid token in RPN")
        }
    }
    if len(st) != 1 {
        return 0, errors.New("invalid expression")
    }
    return st[0], nil
}

func tokenize(s string) ([]tok, error) {
    s = strings.TrimSpace(s)
    toks := make([]tok, 0, len(s))
    i := 0
    lastKind := "op" // treat BOS like operator to allow leading sign
    for i < len(s) {
        c := s[i]
        switch {
        case c == ' ' || c == '\t' || c == '\n' || c == '\r':
            i++
        case (c == '+' || c == '-') && (lastKind == "op" || lastKind == "lpar"):
            // unary sign before number
            if i+1 < len(s) && ((s[i+1] >= '0' && s[i+1] <= '9') || s[i+1] == '.') {
                j := i + 2
                for j < len(s) {
                    if (s[j] >= '0' && s[j] <= '9') || s[j] == '.' {
                        j++
                    } else {
                        break
                    }
                }
                toks = append(toks, tok{kind: "num", val: s[i:j]})
                i = j
                lastKind = "num"
                continue
            }
            // fallback as operator
            toks = append(toks, tok{kind: "op", val: string(c)})
            i++
            lastKind = "op"
        case c >= '0' && c <= '9' || c == '.':
            j := i + 1
            for j < len(s) {
                if (s[j] >= '0' && s[j] <= '9') || s[j] == '.' {
                    j++
                } else {
                    break
                }
            }
            toks = append(toks, tok{kind: "num", val: s[i:j]})
            i = j
            lastKind = "num"
        case c == '+' || c == '-' || c == '*' || c == '/':
            toks = append(toks, tok{kind: "op", val: string(c)})
            i++
            lastKind = "op"
        case c == '(':
            toks = append(toks, tok{kind: "lpar", val: "("})
            i++
            lastKind = "lpar"
        case c == ')':
            toks = append(toks, tok{kind: "rpar", val: ")"})
            i++
            lastKind = "rpar"
        default:
            return nil, fmt.Errorf("invalid character in expression: %q", c)
        }
    }
    return toks, nil
}

func strconvParseFloat(s string) (float64, error) { return strconv.ParseFloat(s, 64) }
