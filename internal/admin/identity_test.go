package admin

import "testing"

func TestDriverIsSmallestAllowedIDAndChangesAtRestart(t *testing.T) {
	for _, ids := range [][]int64{{42, 7, 9, 7}, {9, 42, 7}, {7, 9, 42}} {
		p, err := NewPolicy(-100123, ids, "BridgeBot")
		if err != nil {
			t.Fatal(err)
		}
		ids[0] = 1
		if p.DriverID() != 7 || !p.CanDriveAccount(7) {
			t.Fatalf("driver must be copied and deterministic: %d", p.DriverID())
		}
		for _, id := range []int64{0, -7, 1, 9, 42} {
			if p.CanDriveAccount(id) {
				t.Fatalf("unexpected driver %d", id)
			}
		}
	}
	p, _ := NewPolicy(-100123, []int64{42, 9}, "BridgeBot")
	if p.DriverID() != 9 || p.CanDriveAccount(7) {
		t.Fatal("restart must revoke previous driver")
	}
	var zero Policy
	if zero.DriverID() != 0 || zero.CanDriveAccount(0) || zero.CanDriveAccount(7) {
		t.Fatal("zero policy must not drive")
	}
}

func TestMutationRequiresAuthorizedOriginalOwnerOrExplicitOverride(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42, 7}, "BridgeBot")
	cases := []struct {
		actor, origin  int64
		override, want bool
	}{
		{42, 42, false, true}, {42, 7, false, false}, {42, 7, true, true},
		{8, 8, false, false}, {8, 7, true, false}, {0, 0, true, false},
		{7, 0, true, false}, {7, -1, true, false}, {-7, 7, true, false},
	}
	for _, tc := range cases {
		if got := p.CanMutateOriginal(tc.actor, tc.origin, tc.override); got != tc.want {
			t.Fatalf("ownership case %+v: %v", tc, got)
		}
	}
}
