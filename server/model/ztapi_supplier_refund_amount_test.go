package model

import "testing"

func TestZTAPISupplierRefundAmounts(t *testing.T) {
	dimensions := []ZTAPISupplierRefundDimension{
		{Dimension: "input", Units: "10", UnitQuota: "2", ChargedQuota: 20, TokenChargedQuota: 20},
		{Dimension: "output", Units: "10", UnitQuota: "8", ChargedQuota: 80, TokenChargedQuota: 80},
	}
	tests := []struct {
		name  string
		mode  string
		lines []ZTAPISupplierRefundUnits
		want  int64
		bad   bool
	}{
		{"full original retail charge", "full", nil, 100, false},
		{"exact output units", "partial", []ZTAPISupplierRefundUnits{{"output", "2.5"}}, 20, false},
		{"different dimension price", "partial", []ZTAPISupplierRefundUnits{{"input", "2.5"}}, 5, false},
		{"mixed dimensions", "partial", []ZTAPISupplierRefundUnits{{"input", "2"}, {"output", "3"}}, 28, false},
		{"dollar only evidence", "partial", nil, 0, true},
		{"unknown dimension", "partial", []ZTAPISupplierRefundUnits{{"money", "1"}}, 0, true},
		{"excess usage", "partial", []ZTAPISupplierRefundUnits{{"input", "11"}}, 0, true},
		{"negative units", "partial", []ZTAPISupplierRefundUnits{{"input", "-1"}}, 0, true},
		{"nondecimal units", "partial", []ZTAPISupplierRefundUnits{{"input", "1/2"}}, 0, true},
		{"huge exponent", "partial", []ZTAPISupplierRefundUnits{{"input", "1e999999"}}, 0, true},
		{"duplicate dimension", "partial", []ZTAPISupplierRefundUnits{{"input", "1"}, {"input", "1"}}, 0, true},
		{"full with ambiguous partial units", "full", []ZTAPISupplierRefundUnits{{"input", "1"}}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := planZTAPISupplierRefund(100, 100, dimensions, nil, tt.mode, tt.lines)
			if tt.bad {
				if err == nil {
					t.Fatal("ambiguous reversal accepted")
				}
				return
			}
			if err != nil || plan.Quota != tt.want || plan.TokenQuota != tt.want {
				t.Fatalf("plan=%+v err=%v want=%d", plan, err, tt.want)
			}
		})
	}
}

func TestZTAPISupplierRefundCumulativeRoundingAndCap(t *testing.T) {
	dims := []ZTAPISupplierRefundDimension{{Dimension: "seconds:720p", Units: "3", UnitQuota: "0.5", ChargedQuota: 2, TokenChargedQuota: 2}}
	var previous []ZTAPISupplierRefundProgress
	for i, want := range []int64{0, 1, 1} {
		plan, err := planZTAPISupplierRefund(2, 2, dims, previous, "partial", []ZTAPISupplierRefundUnits{{"seconds:720p", "1"}})
		if err != nil || plan.Quota != want || plan.TokenQuota != want {
			t.Fatalf("part %d: %+v %v, want %d", i, plan, err, want)
		}
		previous = plan.Progress
	}
	if _, err := planZTAPISupplierRefund(2, 2, dims, previous, "partial", []ZTAPISupplierRefundUnits{{"seconds:720p", "0.1"}}); err == nil {
		t.Fatal("cumulative excess accepted")
	}
	full, err := planZTAPISupplierRefund(2, 2, dims, previous, "full", nil)
	if err != nil || full.Quota != 0 {
		t.Fatalf("fully reversed charge credited again: %+v %v", full, err)
	}
}

func TestZTAPISupplierRefundFullAfterPartialAndNoCharge(t *testing.T) {
	dims := []ZTAPISupplierRefundDimension{{Dimension: "output", Units: "10", UnitQuota: "3", ChargedQuota: 30, TokenChargedQuota: 30}}
	partial, err := planZTAPISupplierRefund(30, 30, dims, nil, "partial", []ZTAPISupplierRefundUnits{{"output", "3"}})
	if err != nil {
		t.Fatal(err)
	}
	full, err := planZTAPISupplierRefund(30, 30, dims, partial.Progress, "full", nil)
	if err != nil || full.Quota != 21 || full.TokenQuota != 21 {
		t.Fatalf("full remainder=%+v err=%v", full, err)
	}
	zero, err := planZTAPISupplierRefund(0, 0, nil, nil, "full", nil)
	if err != nil || zero.Quota != 0 || zero.TokenQuota != 0 {
		t.Fatalf("uncharged refund=%+v err=%v", zero, err)
	}
}

func TestZTAPISupplierRefundRejectsAmbiguousAllocation(t *testing.T) {
	for _, d := range []ZTAPISupplierRefundDimension{
		{Dimension: "input", Units: "10", UnitQuota: "2", ChargedQuota: 100, TokenChargedQuota: 100},
		{Dimension: "input", Units: "10", UnitQuota: "10", ChargedQuota: 100, TokenChargedQuota: 30},
	} {
		if _, err := planZTAPISupplierRefund(d.ChargedQuota, d.TokenChargedQuota, []ZTAPISupplierRefundDimension{d}, nil, "partial", []ZTAPISupplierRefundUnits{{"input", "1"}}); err == nil {
			t.Fatalf("accepted ambiguous allocation: %+v", d)
		}
	}
}
