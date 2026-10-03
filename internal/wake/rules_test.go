package wake

import (
	"errors"
	"testing"
	"time"

	"github.com/SK-ENT/rakitsu/internal/config"
)

var errTest = errors.New("boom")

func fp(v float64) *float64 { return &v }

func TestAlarmBelowAbove(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	c := config.WakeCheck{Name: "p", Type: "http_json", AlarmIf: &config.WakeAlarmIf{Below: fp(100)}}
	st := &checkState{}
	if r := evaluate(c, Observation{Number: fp(150)}, now, st, 1); r.Status != StatusOK {
		t.Fatalf("150 above floor should be ok, got %v", r.Status)
	}
	if r := evaluate(c, Observation{Number: fp(90)}, now, st, 1); r.Status != StatusAlarm {
		t.Fatalf("90 below floor should alarm, got %v", r.Status)
	}
	c.AlarmIf = &config.WakeAlarmIf{Above: fp(200)}
	if r := evaluate(c, Observation{Number: fp(250)}, now, st, 1); r.Status != StatusAlarm {
		t.Fatalf("250 above ceiling should alarm")
	}
}

func TestChangePercentWindow(t *testing.T) {
	c := config.WakeCheck{Name: "p", Type: "http_json", AlarmIf: &config.WakeAlarmIf{ChangePercent: fp(3), WindowSeconds: 3600}}
	st := &checkState{}
	t0 := time.Unix(1_000_000, 0)
	evaluate(c, Observation{Number: fp(100)}, t0, st, 1)
	if r := evaluate(c, Observation{Number: fp(102)}, t0.Add(10*time.Minute), st, 1); r.Status != StatusOK {
		t.Fatalf("2%% move must be ok, got %v", r.Status)
	}
	if r := evaluate(c, Observation{Number: fp(104)}, t0.Add(20*time.Minute), st, 1); r.Status != StatusAlarm {
		t.Fatalf("4%% move vs window start must alarm, got %v", r.Status)
	}
	// Samples older than the window are dropped.
	evaluate(c, Observation{Number: fp(200)}, t0.Add(3*time.Hour), st, 1)
	if len(st.Samples) != 1 {
		t.Fatalf("old samples must be pruned, have %d", len(st.Samples))
	}
}

func TestBandHysteresis(t *testing.T) {
	c := config.WakeCheck{Name: "lbl", Type: "http_json", LabelBand: &config.WakeBand{Upper: 1.0, Lower: -1.0, Exit: 0.5}}
	st := &checkState{}
	now := time.Unix(1_000_000, 0)
	step := func(v float64) CheckResult { return evaluate(c, Observation{Number: fp(v)}, now, st, 1) }
	if r := step(0.2); r.Label != "neutral" || r.Changed {
		t.Fatalf("first reading seeds the label without a change: %+v", r)
	}
	if r := step(1.2); r.Label != "up" || !r.Changed {
		t.Fatalf("1.2 must flip to up: %+v", r)
	}
	for _, v := range []float64{0.9, 0.7, 1.05} { // wobble above the exit line: no flip
		if r := step(v); r.Label != "up" || r.Changed {
			t.Fatalf("value %v must stay up without change: %+v", v, r)
		}
	}
	if r := step(0.4); r.Label != "neutral" || !r.Changed {
		t.Fatalf("0.4 is below exit 0.5: back to neutral: %+v", r)
	}
	if r := step(-1.5); r.Label != "down" || !r.Changed {
		t.Fatalf("-1.5 must flip to down: %+v", r)
	}
	if r := step(-0.7); r.Label != "down" || r.Changed {
		t.Fatalf("-0.7 stays down (exit is -0.5): %+v", r)
	}
}

func TestFileMtimeAge(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	c := config.WakeCheck{Name: "f", Type: "file_mtime", MaxAgeSeconds: 300}
	if r := evaluate(c, Observation{Exists: true, ModTime: now.Add(-100 * time.Second)}, now, &checkState{}, 1); r.Status != StatusOK {
		t.Fatalf("fresh file ok, got %v", r.Status)
	}
	if r := evaluate(c, Observation{Exists: true, ModTime: now.Add(-400 * time.Second)}, now, &checkState{}, 1); r.Status != StatusAlarm {
		t.Fatalf("stale file alarms, got %v", r.Status)
	}
	if r := evaluate(c, Observation{Exists: false}, now, &checkState{}, 1); r.Status != StatusAlarm {
		t.Fatalf("missing file alarms, got %v", r.Status)
	}
}

func TestFileContainsAndHTTPStatus(t *testing.T) {
	now := time.Unix(1, 0)
	if r := evaluate(config.WakeCheck{Name: "c", Type: "file_contains", Contains: "FAILED"}, Observation{Exists: true, Contains: true}, now, &checkState{}, 1); r.Status != StatusAlarm {
		t.Fatal("contains FAILED must alarm")
	}
	h := config.WakeCheck{Name: "h", Type: "http_status", Expect: 200}
	if r := evaluate(h, Observation{HTTPStatus: 500}, now, &checkState{}, 1); r.Status != StatusAlarm {
		t.Fatal("500 != 200 must alarm")
	}
	if r := evaluate(h, Observation{HTTPStatus: 200}, now, &checkState{}, 1); r.Status != StatusOK {
		t.Fatal("200 ok")
	}
}

func TestHTTPDebounce(t *testing.T) {
	now := time.Unix(1, 0)
	h := config.WakeCheck{Name: "h", Type: "http_status", Expect: 200}
	st := &checkState{}
	// consecutive = 2: first alarm is held back as ok, second is raised.
	if r := evaluate(h, Observation{HTTPStatus: 500}, now, st, 2); r.Status != StatusOK {
		t.Fatalf("first alarm must be debounced, got %v", r.Status)
	}
	if r := evaluate(h, Observation{HTTPStatus: 500}, now, st, 2); r.Status != StatusAlarm {
		t.Fatalf("second consecutive alarm must raise, got %v", r.Status)
	}
	if r := evaluate(h, Observation{HTTPStatus: 200}, now, st, 2); r.Status != StatusOK || st.ConsecBad != 0 {
		t.Fatalf("ok resets the counter")
	}
	// File checks are immediate.
	f := config.WakeCheck{Name: "f", Type: "file_contains", Contains: "X"}
	if r := evaluate(f, Observation{Exists: true, Contains: true}, now, &checkState{}, 2); r.Status != StatusAlarm {
		t.Fatal("file checks are not debounced")
	}
}

func TestUnknownOnError(t *testing.T) {
	now := time.Unix(1, 0)
	f := config.WakeCheck{Name: "f", Type: "file_contains", Contains: "X"}
	if r := evaluate(f, Observation{Err: errTest}, now, &checkState{}, 1); r.Status != StatusUnknown {
		t.Fatalf("error must be unknown, got %v", r.Status)
	}
}

func TestOverallOutcome(t *testing.T) {
	ok := CheckResult{Status: StatusOK}
	if overall([]CheckResult{ok, ok}) != OutcomeQuiet {
		t.Fatal("all ok is quiet")
	}
	if overall([]CheckResult{ok, {Status: StatusOK, Changed: true}}) != OutcomeChanged {
		t.Fatal("changed label is changed")
	}
	if overall([]CheckResult{{Status: StatusUnknown}, {Status: StatusOK, Changed: true}}) != OutcomeUnknown {
		t.Fatal("unknown beats changed")
	}
	if overall([]CheckResult{{Status: StatusUnknown}, {Status: StatusAlarm}}) != OutcomeAlarm {
		t.Fatal("alarm beats unknown")
	}
}
