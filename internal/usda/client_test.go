package usda

import "testing"

// Every case here is a shape real USDA data actually comes in. The two that
// motivated the parser rewrite are the duplicate "Energy" rows (kcal and kJ
// share a nutrientName, so a name match alone picks whichever came last) and
// the "Fatty acids, total ..." rows that follow "Total lipid (fat)" and used
// to overwrite it.
func TestFoodFromRawJSON(t *testing.T) {
	cases := []struct {
		name                            string
		raw                             string
		cal, protein, carbs, fat, fiber float64
	}{
		{
			name: "current shape: nutrient ids decide, kJ row ignored",
			raw: `{"fdcId":1,"description":"Cheese, parmesan, grated","foodNutrients":[
				{"nutrientId":1004,"nutrientName":"Total lipid (fat)","unitName":"G","value":27.8},
				{"nutrientId":1008,"nutrientName":"Energy","unitName":"KCAL","value":420},
				{"nutrientId":1003,"nutrientName":"Protein","unitName":"G","value":28.4},
				{"nutrientId":1005,"nutrientName":"Carbohydrate, by difference","unitName":"G","value":13.9},
				{"nutrientId":1079,"nutrientName":"Fiber, total dietary","unitName":"G","value":0},
				{"nutrientId":1062,"nutrientName":"Energy","unitName":"kJ","value":1757},
				{"nutrientId":1257,"nutrientName":"Fatty acids, total trans","unitName":"G","value":0.876},
				{"nutrientId":1258,"nutrientName":"Fatty acids, total saturated","unitName":"G","value":15.4}]}`,
			cal: 420, protein: 28.4, carbs: 13.9, fat: 27.8, fiber: 0,
		},
		{
			name: "legacy cached shape: no ids, no units, kJ row last",
			raw: `{"fdcId":2,"description":"Milk, whole","foodNutrients":[
				{"nutrientName":"Total lipid (fat)","value":3.2},
				{"nutrientName":"Energy","value":60},
				{"nutrientName":"Protein","value":3.27},
				{"nutrientName":"Carbohydrate, by difference","value":4.63},
				{"nutrientName":"Energy","value":251},
				{"nutrientName":"Fatty acids, total trans","value":0.112},
				{"nutrientName":"Total fat (NLEA)","value":2.77}]}`,
			cal: 60, protein: 3.27, carbs: 4.63, fat: 3.2, fiber: 0,
		},
		{
			name: "single unlabeled energy is taken at face value when Atwater agrees",
			raw: `{"fdcId":3,"description":"Branded thing","foodNutrients":[
				{"nutrientName":"Energy","value":350},
				{"nutrientName":"Protein","value":10},
				{"nutrientName":"Carbohydrate, by difference","value":60},
				{"nutrientName":"Total lipid (fat)","value":5}]}`,
			cal: 350, protein: 10, carbs: 60, fat: 5,
		},
		{
			name: "single unlabeled energy far above Atwater is read as kJ",
			raw: `{"fdcId":4,"description":"Branded thing in kJ","foodNutrients":[
				{"nutrientName":"Energy","value":1464},
				{"nutrientName":"Protein","value":10},
				{"nutrientName":"Carbohydrate, by difference","value":60},
				{"nutrientName":"Total lipid (fat)","value":5}]}`,
			cal: 1464 / kJPerKcal, protein: 10, carbs: 60, fat: 5,
		},
		{
			name: "Foundation food: Atwater specific factors preferred over general",
			raw: `{"fdcId":6,"description":"Cream, heavy","foodNutrients":[
				{"nutrientName":"Total lipid (fat)","value":35.6},
				{"nutrientName":"Protein","value":2.02},
				{"nutrientName":"Energy (Atwater General Factors)","value":343},
				{"nutrientName":"Energy (Atwater Specific Factors)","value":336},
				{"nutrientName":"Carbohydrate, by summation","value":3.8}]}`,
			cal: 336, protein: 2.02, carbs: 3.8, fat: 35.6,
		},
		{
			name: "kJ-only food is converted rather than stored as kJ",
			raw: `{"fdcId":5,"description":"kJ only","foodNutrients":[
				{"nutrientId":1062,"nutrientName":"Energy","unitName":"kJ","value":418.4},
				{"nutrientId":1003,"nutrientName":"Protein","unitName":"G","value":2}]}`,
			cal: 100, protein: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FoodFromRawJSON(tc.raw)
			if err != nil {
				t.Fatalf("FoodFromRawJSON: %v", err)
			}
			check(t, "calories", got.CaloriesPer100g, tc.cal)
			check(t, "protein", got.ProteinPer100g, tc.protein)
			check(t, "carbs", got.CarbsPer100g, tc.carbs)
			check(t, "fat", got.FatPer100g, tc.fat)
			check(t, "fiber", got.FiberPer100g, tc.fiber)
		})
	}
}

func check(t *testing.T, label string, got, want float64) {
	t.Helper()
	if diff := got - want; diff > 0.01 || diff < -0.01 {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// The real mis-links this rule exists for, plus the legitimate matches it
// must not reject. Dry beans carry ~3.7x the calories of canned per gram, so
// getting this wrong multiplies every macro computed from the item.
func TestContradictsStatedForm(t *testing.T) {
	cases := []struct {
		name, desc string
		want       bool
	}{
		{"black beans (canned)", "beans, black, mature seeds, raw", true},
		{"garbanzo beans (canned)", "chickpeas (garbanzo beans, bengal gram), dry", true},
		{"black beans (canned)", "beans, black turtle, mature seeds, canned", false},
		{"brown lentils (dry)", "lentils, dry", false},
		{"brown lentils (dry)", "lentils, mature seeds, cooked, boiled", true},
		{"diced tomatoes (canned)", "tomatoes, canned, red, ripe, diced", false},
		{"fresh basil", "basil, fresh", false},
		{"fresh basil", "spices, basil, dried", true},
		{"frozen peas", "peas, green, frozen, unprepared", false},
		{"bananas", "bananas, raw", false},
		// Whole-word matching: "raw" must not fire inside "strawberries".
		{"strawberries (fresh)", "strawberries, raw", false},
	}
	for _, tc := range cases {
		if got := contradictsStatedForm(tc.name, tc.desc); got != tc.want {
			t.Errorf("contradictsStatedForm(%q, %q) = %v, want %v", tc.name, tc.desc, got, tc.want)
		}
	}
}
