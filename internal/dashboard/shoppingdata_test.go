package dashboard

import "testing"

func TestSplitSection(t *testing.T) {
	tests := []struct {
		name            string
		in              string
		wantSection     string
		wantDescription string
	}{
		{
			name:            "uppercase prefix becomes the section",
			in:              "PRODUCE: Limes, 2 (Tue, juice and zest)",
			wantSection:     "PRODUCE",
			wantDescription: "Limes, 2 (Tue, juice and zest)",
		},
		{
			name:            "multi-word uppercase prefix",
			in:              "CANNED GOODS: Diced tomatoes, 28 oz",
			wantSection:     "CANNED GOODS",
			wantDescription: "Diced tomatoes, 28 oz",
		},
		{
			name:            "no colon at all falls through to the catch-all",
			in:              "Rolled oats, large canister",
			wantSection:     ungroupedSection,
			wantDescription: "Rolled oats, large canister",
		},
		{
			name:            "lowercase in the prefix means it is ordinary text, not a section",
			in:              "Milk: get the 2% this time",
			wantSection:     ungroupedSection,
			wantDescription: "Milk: get the 2% this time",
		},
		{
			name:            "prefix longer than the cap stays part of the description",
			in:              "REALLY LONG AISLE NAME THAT RUNS ON: Salt",
			wantSection:     ungroupedSection,
			wantDescription: "REALLY LONG AISLE NAME THAT RUNS ON: Salt",
		},
		{
			name:            "leading colon is not a section",
			in:              ": orphaned",
			wantSection:     ungroupedSection,
			wantDescription: ": orphaned",
		},
		{
			name:            "prefix with no letters is not a section",
			in:              "12: two dozen eggs",
			wantSection:     ungroupedSection,
			wantDescription: "12: two dozen eggs",
		},
		{
			name:            "colon without the trailing space is not a separator",
			in:              "PRODUCE:Limes",
			wantSection:     ungroupedSection,
			wantDescription: "PRODUCE:Limes",
		},
		{
			name:            "only the first separator splits",
			in:              "PANTRY: Rice, 2 lb: the jasmine kind",
			wantSection:     "PANTRY",
			wantDescription: "Rice, 2 lb: the jasmine kind",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			section, desc := splitSection(tt.in)
			if section != tt.wantSection {
				t.Errorf("section = %q, want %q", section, tt.wantSection)
			}
			if desc != tt.wantDescription {
				t.Errorf("description = %q, want %q", desc, tt.wantDescription)
			}
		})
	}
}
