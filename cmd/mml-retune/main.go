package main

import (
    "flag"
    "fmt"
    "io"
    "os"

    "github.com/anosatsuk124/mml-retune/config"
    "github.com/anosatsuk124/mml-retune/scanner"
)

func main() {
    cfgPath := flag.String("c", "", "Path to config JSON")
    // also accept long form
    cfgPathLong := flag.String("config", "", "Path to config JSON (alias of -c)")
    initOct := flag.Int("initial-octave", 5, "Initial octave when no left-context 'oN' is found")
    relThresh := flag.Int("relative-threshold", -1, "When |Δ| exceeds this, use oN instead of repeated < or >; -1 disables")
    mode := flag.String("mode", "follow", "Pitch mapping mode: follow (default) or absolute")
    flag.Parse()

    path := *cfgPath
    if path == "" && *cfgPathLong != "" { path = *cfgPathLong }
    var cfg *config.Config
    if path != "" {
        f, err := os.Open(path)
        if err != nil {
            fmt.Fprintln(os.Stderr, "failed to open config:", err)
            os.Exit(1)
        }
        defer f.Close()
        cfg, err = config.Load(f)
        if err != nil {
            fmt.Fprintln(os.Stderr, "failed to load config:", err)
            os.Exit(1)
        }
    }

    // read stdin
    data, err := io.ReadAll(os.Stdin)
    if err != nil {
        fmt.Fprintln(os.Stderr, "failed to read stdin:", err)
        os.Exit(1)
    }

    rw := &scanner.Rewriter{Cfg: cfg}
    if initOct != nil { rw.InitialOct = initOct }
    if relThresh != nil && *relThresh >= 0 { rw.RelativeThresh = relThresh }
    // mode
    switch *mode {
    case "follow", "":
        rw.FollowOctave = true
    case "absolute":
        rw.FollowOctave = false
    default:
        fmt.Fprintln(os.Stderr, "invalid --mode, must be 'follow' or 'absolute'")
        os.Exit(2)
    }

    out, err := rw.Rewrite(string(data))
    if err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
    fmt.Print(out)
}
