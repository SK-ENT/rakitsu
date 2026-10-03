// Wake-up timer check result evaluation and outcome rules.
package wake

import (
	"fmt"
	"strings"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

type Status string

const (
	StatusOK      Status = "ok"
	StatusAlarm   Status = "alarm"
	StatusUnknown Status = "unknown"
)

type Outcome string

const (
	OutcomeQuiet   Outcome = "quiet"
	OutcomeChanged Outcome = "changed"
	OutcomeAlarm   Outcome = "alarm"
	OutcomeUnknown Outcome = "unknown"
)

type sample struct {
	T int64   `json:"t"` // unix seconds
	V float64 `json:"v"`
}

type checkState struct {
	Samples   []sample `json:"samples,omitempty"`
	Label     string   `json:"label,omitempty"`
	ConsecBad int      `json:"consec_bad,omitempty"`
}

type CheckResult struct {
	Name    string
	Status  Status
	Detail  string // <= 512 chars, no query strings
	Label   string // band label for http_json with label_band, else ""
	Changed bool   // band label flipped this tick
}

type Observation struct {
	Err        error     // unreadable, timeout, refused, bad body
	Exists     bool      // file checks
	ModTime    time.Time // file_mtime
	Contains   bool      // file_contains
	HTTPStatus int       // http_status
	Number     *float64  // http_json
}

func isHTTP(t string) bool { return t == "http_status" || t == "http_json" }

// evaluate turns one observation into a result. consecutive is the debounce N
// for http checks (alarm or unknown must repeat N times before it is raised).
func evaluate(c config.WakeCheck, o Observation, now time.Time, st *checkState, consecutive int) CheckResult {
	r := CheckResult{Name: c.Name, Status: StatusOK}
	raw := StatusOK
	switch {
	case o.Err != nil:
		raw, r.Detail = StatusUnknown, truncate(o.Err.Error(), 512)
	default:
		switch c.Type {
		case "file_mtime":
			age := now.Sub(o.ModTime)
			if !o.Exists {
				raw, r.Detail = StatusAlarm, "file missing"
			} else if age > time.Duration(c.MaxAgeSeconds)*time.Second {
				raw, r.Detail = StatusAlarm, fmt.Sprintf("age %ds > %ds", int(age.Seconds()), c.MaxAgeSeconds)
			}
		case "file_contains":
			if o.Contains {
				raw, r.Detail = StatusAlarm, fmt.Sprintf("file contains %q", truncate(c.Contains, 64))
			}
		case "http_status":
			if o.HTTPStatus != c.Expect {
				raw, r.Detail = StatusAlarm, fmt.Sprintf("status %d != %d", o.HTTPStatus, c.Expect)
			}
		case "http_json":
			if o.Number == nil {
				raw, r.Detail = StatusUnknown, "no number"
				break
			}
			v := *o.Number
			r.Detail = fmt.Sprintf("value %g", v)
			if a := c.AlarmIf; a != nil {
				if a.Below != nil && v < *a.Below {
					raw = StatusAlarm
				}
				if a.Above != nil && v > *a.Above {
					raw = StatusAlarm
				}
				if a.ChangePercent != nil {
					if pctMove(st, v, now, a.WindowSeconds) >= *a.ChangePercent {
						raw = StatusAlarm
					}
				}
			}
			if b := c.LabelBand; b != nil {
				prev := st.Label
				st.Label = bandLabel(prev, v, *b)
				r.Label = st.Label
				r.Changed = prev != "" && prev != st.Label
			}
		default:
			raw, r.Detail = StatusUnknown, "unknown check type"
		}
	}
	if raw == StatusOK {
		st.ConsecBad = 0
	} else if isHTTP(c.Type) {
		st.ConsecBad++
		if st.ConsecBad < consecutive {
			raw = StatusOK // debounced: not raised yet
		}
	}
	r.Status = raw
	return r
}

func pctMove(st *checkState, v float64, now time.Time, windowSec int) float64 {
	cut := now.Add(-time.Duration(windowSec) * time.Second).Unix()
	kept := st.Samples[:0]
	for _, s := range st.Samples {
		if s.T >= cut {
			kept = append(kept, s)
		}
	}
	st.Samples = kept
	move := 0.0
	if len(st.Samples) > 0 && st.Samples[0].V != 0 {
		base := st.Samples[0].V
		move = (v - base) / base * 100
		if move < 0 {
			move = -move
		}
	}
	st.Samples = append(st.Samples, sample{T: now.Unix(), V: v})
	return move
}

func bandLabel(prev string, v float64, b config.WakeBand) string {
	switch prev {
	case "up":
		if v >= b.Exit {
			return "up"
		}
	case "down":
		if v <= -b.Exit {
			return "down"
		}
	}
	switch {
	case v > b.Upper:
		return "up"
	case v < b.Lower:
		return "down"
	}
	return "neutral"
}

func overall(rs []CheckResult) Outcome {
	out := OutcomeQuiet
	for _, r := range rs {
		switch {
		case r.Status == StatusAlarm:
			return OutcomeAlarm
		case r.Status == StatusUnknown:
			out = OutcomeUnknown
		case r.Changed && out == OutcomeQuiet:
			out = OutcomeChanged
		}
	}
	return out
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// redactURL drops the query string and userinfo for audit/detail text.
func redactURL(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i] + "?[redacted]"
	}
	return u
}
