package permission

import (
	"context"
	"testing"
)

func TestWithPolicyRoundTripsEffectivePolicy(t *testing.T) {
	policy, err := ResolvePolicy(PolicyConfig{DefaultFocus: FocusCoding, DefaultMode: ModeAsk}, Selection{}, nil, PolicyInputs{
		Operator: OperatorPolicy{HardLimits: Authority{}, Requestable: Authority{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := WithPolicy(context.Background(), policy)
	got, ok := PolicyFrom(ctx)
	if !ok {
		t.Fatal("PolicyFrom() did not find the attached policy")
	}
	if got.Selection() != policy.Selection() {
		t.Fatalf("Selection() = %#v, want %#v", got.Selection(), policy.Selection())
	}
}

func TestPolicyFromMissing(t *testing.T) {
	if _, ok := PolicyFrom(context.Background()); ok {
		t.Fatal("PolicyFrom() found a policy in a context without one")
	}
}
