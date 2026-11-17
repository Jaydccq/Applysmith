package main

import (
	"encoding/binary"
	"math"
	"os"
)

const sampleRate = 44100

// chord is one bar of harmony as MIDI note numbers.
type chord struct {
	bass int
	pad  []int
	arp  [4]int
}

// score describes one piece: a four-bar progression played in a loop, with
// the layers entering and leaving by position in the film.
type score struct {
	bpm     float64
	chords  []chord
	pattern [8]int  // arpeggio: index into chord.arp for each eighth note
	drums   float64 // level of kick, clap and shaker; 0 leaves them out
	bell    []int   // a slow melody over four bars, one note per half bar; 0 rests
}

var scores = map[string]score{
	// D major, bright and steady.
	"intro": {
		bpm: 104,
		chords: []chord{
			{38, []int{50, 57, 62, 64, 66}, [4]int{62, 66, 69, 74}},
			{35, []int{47, 54, 57, 62, 66}, [4]int{59, 62, 66, 69}},
			{43, []int{43, 50, 59, 62, 66}, [4]int{59, 62, 66, 67}},
			{45, []int{45, 52, 57, 61, 64}, [4]int{57, 61, 64, 69}},
		},
		pattern: [8]int{0, 2, 1, 2, 3, 2, 1, 2},
		drums:   1,
		bell:    []int{78, 81, 78, 74, 74, 79, 76, 73},
	},
	// F major, slower and lighter, so it sits under a tutorial.
	"setup": {
		bpm: 86,
		chords: []chord{
			{41, []int{53, 57, 60, 64}, [4]int{65, 69, 72, 76}},
			{45, []int{52, 55, 60, 64}, [4]int{64, 67, 69, 72}},
			{46, []int{53, 57, 62, 65}, [4]int{65, 69, 70, 74}},
			{36, []int{55, 60, 64, 67}, [4]int{64, 67, 72, 76}},
		},
		pattern: [8]int{0, 1, 2, 3, 2, 1, 2, 1},
		drums:   0.55,
		bell:    []int{81, 0, 76, 0, 77, 0, 79, 76},
	},
}

type stereo struct{ l, r []float64 }

func newStereo(seconds float64) *stereo {
	n := int(seconds * sampleRate)
	return &stereo{make([]float64, n), make([]float64, n)}
}

// add mixes a mono voice of the given length at start, panned from -1 (left)
// to 1 (right). voice receives the time since the note began.
func (s *stereo) add(start, length, pan, gain float64, voice func(t float64) float64) {
	first := int(start * sampleRate)
	gl := gain * math.Sqrt((1-pan)/2)
	gr := gain * math.Sqrt((1+pan)/2)
	for i := 0; i < int(length*sampleRate); i++ {
		at := first + i
		if at < 0 || at >= len(s.l) {
			continue
		}
		v := voice(float64(i) / sampleRate)
		s.l[at] += gl * v
		s.r[at] += gr * v
	}
}

func (s *stereo) mix(o *stereo, gain float64) {
	for i := range s.l {
		s.l[i] += gain * o.l[i]
		s.r[i] += gain * o.r[i]
	}
}

// echo adds a ping-pong delay: each repeat crosses to the other side.
func (s *stereo) echo(delay, feedback, wet float64) {
	d := int(delay * sampleRate)
	bl := append([]float64(nil), s.l...)
	br := append([]float64(nil), s.r...)
	for i := d; i < len(bl); i++ {
		bl[i] += feedback * br[i-d]
		br[i] += feedback * bl[i-d]
	}
	for i := d; i < len(bl); i++ {
		s.l[i] += wet * br[i-d]
		s.r[i] += wet * bl[i-d]
	}
}

func hz(midi int) float64 { return 440 * math.Pow(2, float64(midi-69)/12) }

func sine(f, t float64) float64 { return math.Sin(2 * math.Pi * f * t) }

// swell is an envelope that rises over attack, holds, and falls over release.
func swell(t, length, attack, release float64) float64 {
	switch {
	case t < attack:
		return t / attack
	case t > length-release:
		return math.Max(0, (length-t)/release)
	}
	return 1
}

// noise is a fixed pseudo-random sequence, so a render is reproducible.
type noise uint32

func (n *noise) next() float64 {
	*n = *n*1664525 + 1013904223
	return float64(*n>>8)/float64(1<<23) - 1
}

// compose renders a score to fit seconds exactly. The first bars are pad and
// arpeggio only, the body adds bass, drums and the bell, and the last bars
// thin out again under a fade.
func compose(sc score, seconds float64) *stereo {
	beat := 60 / sc.bpm
	bar := 4 * beat
	bars := int(math.Ceil(seconds / bar))
	out := newStereo(seconds)
	pad, arp, bell := newStereo(seconds), newStereo(seconds), newStereo(seconds)
	rnd := noise(7)

	intro := 4
	ending := bars - 3
	for b := 0; b < bars; b++ {
		at := float64(b) * bar
		ch := sc.chords[b%len(sc.chords)]
		body := b >= intro && b < ending

		for i, n := range ch.pad {
			f := hz(n)
			pan := -0.6 + 1.2*float64(i)/float64(len(ch.pad)-1)
			pad.add(at, bar+0.6, pan, 0.038, func(t float64) float64 {
				tone := sine(f*0.997, t) + sine(f*1.003, t) + 0.35*sine(2*f, t) + 0.22*sine(3*f, t) + 0.1*sine(4*f, t)
				return tone * swell(t, bar+0.6, 0.5, 0.6)
			})
		}

		if b >= 2 || sc.drums < 1 {
			for step, idx := range sc.pattern {
				f := hz(ch.arp[idx])
				level := 0.17
				if step%2 == 1 {
					level = 0.12
				}
				if !body {
					level *= 0.7
				}
				arp.add(at+float64(step)*beat/2, 0.9, 0.35*math.Sin(float64(step)), level, func(t float64) float64 {
					return (sine(f, t) + 0.5*sine(2*f, t)*math.Exp(-5*t) + 0.3*sine(3*f, t)*math.Exp(-8*t) + 0.15*sine(4*f, t)*math.Exp(-12*t)) *
						math.Exp(-5*t) * math.Min(1, t/0.004)
				})
			}
		}

		if body {
			f := hz(ch.bass)
			for _, pos := range []float64{0, 1.5, 2.5} {
				out.add(at+pos*beat, 0.9*beat, 0, 0.16, func(t float64) float64 {
					return (sine(f, t) + 0.45*sine(2*f, t) + 0.12*sine(3*f, t)) * swell(t, 0.9*beat, 0.01, 0.12)
				})
			}
			if len(sc.bell) > 0 && (b/4)%2 == 1 {
				for half := 0; half < 2; half++ {
					n := sc.bell[(b%4)*2+half]
					if n == 0 {
						continue
					}
					f := hz(n)
					bell.add(at+float64(half)*2*beat, 2.2, 0.2, 0.11, func(t float64) float64 {
						return (sine(f, t) + 0.35*sine(2*f, t)*math.Exp(-4*t) + 0.12*sine(4.2*f, t)*math.Exp(-9*t)) *
							math.Exp(-2.2*t) * math.Min(1, t/0.006)
					})
				}
			}
		}

		if body && sc.drums > 0 {
			for _, pos := range []float64{0, 2} {
				out.add(at+pos*beat, 0.35, 0, 0.36*sc.drums, func(t float64) float64 {
					phase := 2 * math.Pi * (48*t + (110/28.0)*(1-math.Exp(-28*t)))
					return math.Sin(phase) * math.Exp(-9*t)
				})
			}
			for _, pos := range []float64{1, 3} {
				var low float64
				out.add(at+pos*beat, 0.22, 0.1, 0.2*sc.drums, func(t float64) float64 {
					v := rnd.next()
					low += 0.25 * (v - low)
					return (v - low*0.6) * math.Exp(-26*t)
				})
			}
			for step := 0; step < 8; step++ {
				level := 0.07
				if step%2 == 1 {
					level = 0.12
				}
				var low float64
				out.add(at+float64(step)*beat/2, 0.07, -0.3, level*sc.drums, func(t float64) float64 {
					v := rnd.next()
					low += 0.5 * (v - low)
					return (v - low) * math.Exp(-70*t)
				})
			}
		}
	}

	arp.echo(0.75*beat, 0.38, 0.32)
	bell.echo(1.5*beat, 0.4, 0.4)
	out.mix(pad, 1)
	out.mix(arp, 1)
	out.mix(bell, 1)
	out.finish(seconds)
	return out
}

// finish fades the ends, soft-limits the peaks and normalizes to -3 dBFS.
func (s *stereo) finish(seconds float64) {
	const fadeIn, fadeOut = 0.4, 3.5
	peak := 0.0
	for i := range s.l {
		t := float64(i) / sampleRate
		g := math.Min(1, t/fadeIn)
		if left := seconds - t; left < fadeOut {
			g *= math.Pow(math.Max(0, left/fadeOut), 1.6)
		}
		s.l[i] = math.Tanh(1.4*s.l[i]) * g
		s.r[i] = math.Tanh(1.4*s.r[i]) * g
		peak = math.Max(peak, math.Max(math.Abs(s.l[i]), math.Abs(s.r[i])))
	}
	if peak == 0 {
		return
	}
	gain := math.Pow(10, -3.0/20) / peak
	for i := range s.l {
		s.l[i] *= gain
		s.r[i] *= gain
	}
}

// writeWAV writes 16-bit stereo PCM.
func writeWAV(file string, s *stereo) error {
	data := len(s.l) * 4
	buf := make([]byte, 0, 44+data)
	le := binary.LittleEndian
	buf = append(buf, "RIFF"...)
	buf = le.AppendUint32(buf, uint32(36+data))
	buf = append(buf, "WAVEfmt "...)
	buf = le.AppendUint32(buf, 16)
	buf = le.AppendUint16(buf, 1)
	buf = le.AppendUint16(buf, 2)
	buf = le.AppendUint32(buf, sampleRate)
	buf = le.AppendUint32(buf, sampleRate*4)
	buf = le.AppendUint16(buf, 4)
	buf = le.AppendUint16(buf, 16)
	buf = append(buf, "data"...)
	buf = le.AppendUint32(buf, uint32(data))
	for i := range s.l {
		buf = le.AppendUint16(buf, uint16(int16(s.l[i]*32767)))
		buf = le.AppendUint16(buf, uint16(int16(s.r[i]*32767)))
	}
	return os.WriteFile(file, buf, 0o644)
}
