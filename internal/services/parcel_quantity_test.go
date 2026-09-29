package services

import "testing"

func TestScaleParcelForQuantity(t *testing.T) {
	unit := ParcelInput{Length: "20", Width: "15", Height: "10", DistanceUnit: "cm", Weight: "1.2", MassUnit: "kg"}

	got := scaleParcelForQuantity(unit, 7)
	if got.Weight != "8.400" {
		t.Fatalf("want weight 8.400, got %s", got.Weight)
	}
	if got.Length != "20" || got.Width != "15" || got.Height != "70.000" {
		t.Fatalf("want smallest side stacked to 70, got %+v", got)
	}

	if same := scaleParcelForQuantity(unit, 1); same != unit {
		t.Fatalf("quantity 1 must not change the parcel, got %+v", same)
	}

	bad := ParcelInput{Length: "x", Width: "15", Height: "10", Weight: "1.2"}
	if kept := scaleParcelForQuantity(bad, 3); kept.Length != "x" || kept.Height != "10" {
		t.Fatalf("unreadable sides must be left as-is, got %+v", kept)
	}
}

func TestMergeParcelsSumsWeightAndKeepsLargestSides(t *testing.T) {
	got := mergeParcels([]ParcelInput{
		{Length: "20", Width: "15", Height: "10", Weight: "1", DistanceUnit: "cm", MassUnit: "kg"},
		{Length: "30", Width: "10", Height: "12", Weight: "2.5", DistanceUnit: "cm", MassUnit: "kg"},
	})
	if got.Weight != "3.500" || got.Length != "30" || got.Width != "15" || got.Height != "12" {
		t.Fatalf("unexpected merged parcel %+v", got)
	}
}
