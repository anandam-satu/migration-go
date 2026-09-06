package service

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestParsePricelistRowsNoHeader(t *testing.T) {
	values := [][]any{
		{"Layar 14\" FHD", "NB ACER AL14-37P-32RZ", "10.000.000", "11.500.000"},
		{"Intel i5 RAM 8GB", "NB LENOVO IDEAPAD 3", "7.250.000,50", "8.000.000"},
		// Row without item_name (merged/variant continuation) is skipped.
		{"Varian warna hitam", "", "9.500.000", ""},
		// Entirely blank row is skipped.
		{"", "", "", ""},
		{"", "HP DESKJET 2720", "Rp 950.000", "1.150.000"},
	}

	rows, skipped := parsePricelistRows(values)
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (only the no-item_name row)", skipped)
	}
	if len(rows) != 3 {
		t.Fatalf("parsed %d rows, want 3", len(rows))
	}

	nb, ok := rows["NB ACER AL14 37P 32RZ"]
	if !ok {
		t.Fatalf("missing normalized key %q; got %v", "NB ACER AL14 37P 32RZ", keysOf(rows))
	}
	if nb.ItemName != "NB ACER AL14-37P-32RZ" {
		t.Errorf("ItemName = %q, want original raw name kept", nb.ItemName)
	}
	if nb.Spesifikasi == nil || *nb.Spesifikasi != "Layar 14\" FHD" {
		t.Errorf("Spesifikasi = %v, want %q", nb.Spesifikasi, "Layar 14\" FHD")
	}
	if nb.Modal == nil || !nb.Modal.Equal(decimal.NewFromInt(10000000)) {
		t.Errorf("Modal = %v, want 10000000", nb.Modal)
	}
	if nb.FinalPricelist == nil || !nb.FinalPricelist.Equal(decimal.NewFromInt(11500000)) {
		t.Errorf("FinalPricelist = %v, want 11500000", nb.FinalPricelist)
	}

	// Money with comma decimal separator.
	lenovo := rows["NB LENOVO IDEAPAD 3"]
	if lenovo.Modal == nil || !lenovo.Modal.Equal(decimal.RequireFromString("7250000.50")) {
		t.Errorf("Lenovo Modal = %v, want 7250000.50", lenovo.Modal)
	}

	// "Rp " prefix stripped.
	hp := rows["HP DESKJET 2720"]
	if hp.Modal == nil || !hp.Modal.Equal(decimal.NewFromInt(950000)) {
		t.Errorf("HP Modal = %v, want 950000", hp.Modal)
	}
	if hp.Spesifikasi != nil {
		t.Errorf("HP Spesifikasi = %v, want nil (blank cell)", hp.Spesifikasi)
	}
}

func TestParsePricelistRowsDuplicateMergesUp(t *testing.T) {
	values := [][]any{
		// First occurrence has everything -> later duplicate must not overwrite.
		{"Spec A", "ITEM SAMA", "1000", "1500"},
		{"Spec B", "item-sama", "2000", "2500"},
		// First occurrence is sparse -> later row fills the gaps only.
		{"", "PRODUK LAIN", "", ""},
		{"Spec C", "produk lain", "500", "750"},
	}

	rows, _ := parsePricelistRows(values)
	if len(rows) != 2 {
		t.Fatalf("parsed %d rows, want 2 (duplicates merged)", len(rows))
	}

	first := rows["ITEM SAMA"]
	if first.Spesifikasi == nil || *first.Spesifikasi != "Spec A" {
		t.Errorf("ITEM SAMA Spesifikasi = %v, want first row wins (Spec A)", first.Spesifikasi)
	}
	if first.Modal == nil || !first.Modal.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("ITEM SAMA Modal = %v, want 1000 (not overwritten by 2000)", first.Modal)
	}

	second := rows["PRODUK LAIN"]
	if second.Spesifikasi == nil || *second.Spesifikasi != "Spec C" {
		t.Errorf("PRODUK LAIN Spesifikasi = %v, want gap filled from later row", second.Spesifikasi)
	}
	if second.ItemName != "PRODUK LAIN" {
		t.Errorf("PRODUK LAIN ItemName = %q, want PRODUK LAIN", second.ItemName)
	}
}

func TestParsePricelistRowsHandlesNumericCells(t *testing.T) {
	// Defensive: FORMATTED_VALUE normally returns strings, but numbers should
	// still be parsed correctly if the client ever sends them.
	values := [][]any{
		{"", "BARANG ANGKA", float64(2857000), float64(3300000)},
	}
	rows, _ := parsePricelistRows(values)
	r := rows["BARANG ANGKA"]
	if r.Modal == nil || !r.Modal.Equal(decimal.NewFromInt(2857000)) {
		t.Errorf("Modal = %v, want 2857000", r.Modal)
	}
}

func TestCleanDecimal(t *testing.T) {
	cases := []struct {
		in   string
		want string // decimal text, "" = expect nil
	}{
		{"", ""},
		{"   ", ""},
		{"Rp", ""},
		{"2857000", "2857000"},
		{"2.857.000", "2857000"},
		{"2.857.000,50", "2857000.5"},
		{"7.250.000,50", "7250000.5"},
		{"44.00", "44"},
		{"Rp 2.857.000", "2857000"},
		{"Rp2.857.000", "2857000"},
		{"1.234.567", "1234567"},
		{"1234.5", "1234.5"},         // single dot, group of 1 -> decimal separator
		{"1.000", "1000"},            // one group of exactly 3 -> thousand separator
		{"-5000", "-5000"},           // negative allowed
		{"abc", ""},                  // unparsable -> nil
		{"1.2.3", ""},                // no 3-digit groups -> left as-is, BigDecimal rejects it -> nil
		{"2.857.000 abc", "2857000"}, // stray letters removed
	}
	for _, c := range cases {
		got := cleanDecimal(c.in)
		if c.want == "" {
			if got != nil {
				t.Errorf("cleanDecimal(%q) = %v, want nil", c.in, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("cleanDecimal(%q) = nil, want %s", c.in, c.want)
			continue
		}
		if !got.Equal(decimal.RequireFromString(c.want)) {
			t.Errorf("cleanDecimal(%q) = %s, want %s", c.in, got.String(), c.want)
		}
	}
}

func keysOf(rows map[string]pricelistRow) []string {
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	return keys
}
