package normalize

import "testing"

// The first two cases mirror the normalization assertions that lived in the
// deleted MigrationServicePricelistParsingTest (NormalizationUtil itself is
// still used by the stock migration).
func TestNormalizeItemName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"NB ACER AG14-72P-58AK", "NB ACER AG14 72P 58AK"},
		{"NB ACER AG14 72P 58AK", "NB ACER AG14 72P 58AK"},
		{"", ""},
		{"  multiple   spaces  here ", "MULTIPLE SPACES HERE"},
		{"Laptop™ Pro®", "LAPTOP PRO"},
		{"Kabel-USB/2.0", "KABEL USB 2 0"},
	}
	for _, c := range cases {
		if got := NormalizeItemName(c.in); got != c.want {
			t.Errorf("NormalizeItemName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeCompact(t *testing.T) {
	if got := NormalizeCompact("NB-ACER AG14/72P"); got != "NBACERAG1472P" {
		t.Errorf("NormalizeCompact = %q, want %q", got, "NBACERAG1472P")
	}
	if got := NormalizeCompact(""); got != "" {
		t.Errorf("NormalizeCompact('') = %q, want empty", got)
	}
}
