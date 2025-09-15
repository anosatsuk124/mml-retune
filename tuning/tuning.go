package tuning

import (
    "math"
)

// Nearest12TET returns n (integer semitone offset from A4) whose f(n)
// is closest to the given frequency fTarget, with baseA as A4 reference.
func Nearest12TET(baseA, fTarget float64) int {
    if fTarget <= 0 || baseA <= 0 {
        return 0
    }
    n0 := int(math.Round(12 * math.Log2(fTarget/baseA)))

    // tie-break: choose smaller |n| if equal distance
    best := n0
    bestDiff := math.Abs(freqAt(baseA, n0) - fTarget)
    for _, cand := range []int{n0 - 1, n0 + 1} { // check neighbors for exact minimal
        d := math.Abs(freqAt(baseA, cand) - fTarget)
        if d < bestDiff || (almostEqual(d, bestDiff) && absInt(cand) < absInt(best)) {
            best = cand
            bestDiff = d
        }
    }
    return best
}

func freqAt(baseA float64, n int) float64 {
    return baseA * math.Pow(2, float64(n)/12.0)
}

// SplitN maps semitone offset n from A4 into pitch class (pc) and absolute octave.
// pc is one of: c c+ d d+ e f f+ g g+ a a+ b
// A4 (n=0) -> pc="a", octAbs=4.
func SplitN(n int) (pc string, octAbs int) {
    // Map using MIDI assumption: A4 = 69, C4 = 60
    midi := 69 + n
    pcs := [12]string{"c", "c+", "d", "d+", "e", "f", "f+", "g", "g+", "a", "a+", "b"}
    idx := mod(midi, 12)
    pc = pcs[idx]
    octAbs = midi/12 - 1
    return
}

func PBValue(detSemi float64, br int) (pb int, clipped bool) {
    if br == 0 {
        return 0, false
    }
    v := math.Round(8192.0 * detSemi / float64(br))
    if v < -8192 {
        return -8192, true
    }
    if v > 8191 {
        return 8191, true
    }
    return int(v), false
}

func DetuneSemitones(baseA, fTarget float64, n int) float64 {
    fN := freqAt(baseA, n)
    return 12 * math.Log2(fTarget/fN)
}

func mod(a, b int) int {
    r := a % b
    if r < 0 { r += b }
    return r
}

func absInt(x int) int { if x < 0 { return -x }; return x }
func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

