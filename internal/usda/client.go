// Package usda is a thin client for the USDA FoodData Central search API,
// used as the second link in the macro-resolution chain (after the local
// nutrition cache, before a crude keyword estimate).
package usda

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const searchURL = "https://api.nal.usda.gov/fdc/v1/foods/search"

// searchFoodsPageSize is how many ranked results an ad-hoc food search pulls
// back. More than one only so a macro-less top hit can be stepped over; the
// first usable result still wins.
const searchFoodsPageSize = 5

// Client talks to the USDA FoodData Central API.
type Client struct {
	apiKey     string
	httpClient *http.Client
}

// NewClient builds a Client. apiKey must be non-empty - callers should check
// config.USDAAPIKey() first and skip USDA lookups entirely when it's "".
func NewClient(apiKey string) *Client {
	return &Client{apiKey: apiKey, httpClient: &http.Client{Timeout: 10 * time.Second}}
}

// Food is one search result, with the macros we care about mapped out of
// FDC's foodNutrients array (per 100g, matching the site's own convention).
type Food struct {
	FDCID           int
	Description     string
	CaloriesPer100g float64
	ProteinPer100g  float64
	CarbsPer100g    float64
	FatPer100g      float64
	FiberPer100g    float64
	RawJSON         string

	// HasProximates records whether this food actually reported any of the
	// macro nutrients at all. Some FDC entries come back carrying only
	// fatty-acid breakdowns and micronutrients, and for those a macro of 0
	// means "not reported", not "none" - which is indistinguishable from a
	// food that genuinely has none (salt, baking soda) by value alone.
	HasProximates bool
}

type searchResponse struct {
	Foods []food `json:"foods"`
}

// food is one FDC food record, kept as a named type so a cached raw_json
// blob (which is exactly this, re-marshaled) can be re-parsed later by
// FoodFromRawJSON without another API call.
type food struct {
	FDCID         int        `json:"fdcId"`
	Description   string     `json:"description"`
	FoodNutrients []nutrient `json:"foodNutrients"`
}

// nutrient is one entry of a food's foodNutrients array. NutrientID and
// UnitName are what make the macros unambiguous and are both persisted in
// raw_json; they are absent from rows cached before this struct kept them,
// so every field is optional and macrosFrom falls back to exact names.
type nutrient struct {
	NutrientID   int     `json:"nutrientId,omitempty"`
	NutrientName string  `json:"nutrientName"`
	UnitName     string  `json:"unitName,omitempty"`
	Value        float64 `json:"value"`
}

// FDC nutrient ids for the five macros this project tracks. Selecting by id
// (rather than by matching on nutrientName) is the only reliable way to tell
// USDA's two "Energy" rows apart - kcal and kJ share the name "Energy" and
// differ only in unitName - and to keep "Total lipid (fat)" from being
// clobbered by the "Fatty acids, total saturated/trans/..." rows that follow
// it in the same array.
const (
	nutrientIDProtein          = 1003
	nutrientIDFat              = 1004
	nutrientIDCarbs            = 1005
	nutrientIDEnergyKcal       = 1008
	nutrientIDCarbsBySummation = 1050
	nutrientIDEnergyKJ         = 1062
	nutrientIDFiber            = 1079
	nutrientIDEnergyAtwaterGen = 2047
	nutrientIDEnergyAtwaterSpe = 2048
)

// kJPerKcal converts a kJ energy figure to kcal, for the rare food that
// reports only kJ.
const kJPerKcal = 4.184

// kJDetectionRatio is how far above an Atwater (4/4/9) estimate a lone,
// unlabeled energy figure has to sit before it's treated as kJ rather than
// kcal. Atwater can be off by a fair margin for real foods but never by
// anything approaching the 4.184x a unit mix-up produces.
const kJDetectionRatio = 2.5

// FoodFromRawJSON re-derives a Food from a cached raw_json blob, so macros
// can be recomputed from data already on disk after a parsing fix, without
// re-querying USDA (and without risking a search returning a different food
// than the one originally confirmed).
func FoodFromRawJSON(raw string) (Food, error) {
	var f food
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return Food{}, fmt.Errorf("decoding cached USDA food: %w", err)
	}
	return f.toFood(raw)
}

func (f food) toFood(raw string) (Food, error) {
	out := Food{FDCID: f.FDCID, Description: f.Description, RawJSON: raw}
	var found bool
	out.CaloriesPer100g, out.ProteinPer100g, out.CarbsPer100g, out.FatPer100g, out.FiberPer100g, found = macrosFrom(f.FoodNutrients)
	out.HasProximates = found
	return out, nil
}

// macrosFrom pulls the five tracked macros out of a food's nutrient array,
// preferring FDC nutrient ids and falling back to the exact canonical
// nutrient names for legacy cached rows that predate ids being stored. The
// name fallbacks are deliberately exact rather than substring matches: a
// substring match on "fat" also hits every "Fatty acids, total ..." row, and
// one on "energy" cannot separate kcal from kJ.
func macrosFrom(nutrients []nutrient) (calories, protein, carbs, fat, fiber float64, found bool) {
	var e energyRows
	var carbsBySummation float64
	for _, n := range nutrients {
		name := strings.ToLower(strings.TrimSpace(n.NutrientName))
		unit := strings.ToUpper(strings.TrimSpace(n.UnitName))
		switch {
		case n.NutrientID == nutrientIDEnergyKcal, name == "energy" && unit == "KCAL":
			e.kcal, found = n.Value, true
		case n.NutrientID == nutrientIDEnergyKJ, name == "energy" && unit == "KJ":
			e.kj, found = n.Value, true
		case n.NutrientID == nutrientIDEnergyAtwaterSpe, name == "energy (atwater specific factors)":
			e.atwaterSpecific, found = n.Value, true
		case n.NutrientID == nutrientIDEnergyAtwaterGen, name == "energy (atwater general factors)":
			e.atwaterGeneral, found = n.Value, true
		case name == "energy":
			e.unlabeled, found = append(e.unlabeled, n.Value), true
		case n.NutrientID == nutrientIDProtein, name == "protein":
			protein, found = n.Value, true
		case n.NutrientID == nutrientIDCarbs, name == "carbohydrate, by difference":
			carbs, found = n.Value, true
		case n.NutrientID == nutrientIDCarbsBySummation, name == "carbohydrate, by summation":
			carbsBySummation, found = n.Value, true
		case n.NutrientID == nutrientIDFat, name == "total lipid (fat)":
			fat, found = n.Value, true
		case n.NutrientID == nutrientIDFiber, name == "fiber, total dietary":
			fiber, found = n.Value, true
		}
	}
	// Foundation foods sometimes carry carbohydrate by summation instead of
	// by difference; either is a real total-carbohydrate figure.
	if carbs == 0 {
		carbs = carbsBySummation
	}
	calories = e.resolve(4*protein + 4*carbs + 9*fat)
	return calories, protein, carbs, fat, fiber, found
}

// energyRows collects every energy figure a food reported, since which ones
// are present varies by dataset: SR Legacy gives a kcal row and a kJ row
// that share the name "Energy", while Foundation gives Atwater general
// and/or specific factors (both kcal) and often no plain "Energy" row at all.
type energyRows struct {
	kcal            float64
	kj              float64
	atwaterSpecific float64
	atwaterGeneral  float64
	unlabeled       []float64
}

// resolve picks the kcal figure out of however many energy rows a food
// turned out to have. Anything labeled kcal wins outright. Failing that, a
// legacy cached row carries two identically-named "Energy" entries with
// their units stripped, and since kJ is always ~4.184x kcal the smaller of
// the pair is the kcal one. A single unlabeled figure has nothing to compare
// against, so it's checked against an Atwater estimate instead.
func (e energyRows) resolve(atwaterEstimate float64) float64 {
	switch {
	case e.kcal > 0:
		return e.kcal
	// USDA's own preference order: specific factors are derived for the
	// individual food, general factors are the 4/4/9 defaults.
	case e.atwaterSpecific > 0:
		return e.atwaterSpecific
	case e.atwaterGeneral > 0:
		return e.atwaterGeneral
	}
	if len(e.unlabeled) > 1 {
		smallest := e.unlabeled[0]
		for _, v := range e.unlabeled[1:] {
			if v < smallest {
				smallest = v
			}
		}
		return smallest
	}
	if len(e.unlabeled) == 1 {
		if atwaterEstimate > 0 && e.unlabeled[0] > atwaterEstimate*kJDetectionRatio {
			return e.unlabeled[0] / kJPerKcal
		}
		return e.unlabeled[0]
	}
	if e.kj > 0 {
		return e.kj / kJPerKcal
	}
	return 0
}

// undesirableDescriptors are processed/derivative-form words that shouldn't
// win a generic pantry-item match unless the item's own name already implies
// that form (e.g. "Diced Tomatoes (canned)" should still match a canned
// entry). USDA's relevance ranking for a bare generic query like "Bananas"
// or "Carrots" doesn't reliably put the plain raw entry first even within
// Foundation/SR Legacy - a dehydrated or powdered variant can outrank it -
// so SearchGenericFoods filters candidates rather than trusting position 1.
var undesirableDescriptors = []string{
	"dehydrated", "powder", "dried", "juice", "concentrate", "isolate",
	"extract", "freeze-dried", "flakes", "flour",
	"whipped", "topping", "pressurized", "imitation", "substitute",
	"souffle", "casserole", "soup", "salad", "sandwich", "kimchi",
	"pickled", "fermented", "bread", "cake", "pie",
}

// contradictoryForms maps a preparation form stated in a pantry item's own
// name to the forms that cannot then be the same food. An item called "Black
// Beans (canned)" matching "Beans, black, mature seeds, raw" isn't a near
// miss - dry beans carry roughly 3.7x the calories of canned ones per gram,
// so the mismatch quietly multiplies every macro computed from it.
//
// undesirableDescriptors can't catch this: "raw" and "dry" are perfectly
// desirable for the many items that really are raw or dry, and only become
// wrong in the presence of a contradicting word in the item's own name.
var contradictoryForms = map[string][]string{
	"canned": {"raw", "dry", "dried", "uncooked", "frozen"},
	"cooked": {"raw", "dry", "dried", "uncooked"},
	"frozen": {"canned", "dried"},
	"dry":    {"canned", "cooked"},
	"dried":  {"canned", "cooked"},
	"fresh":  {"canned", "dried", "dry", "frozen"},
}

// contradictsStatedForm reports whether desc states a preparation form that
// the item's own name rules out.
func contradictsStatedForm(nameLower, descLower string) bool {
	for form, conflicts := range contradictoryForms {
		if !containsWord(nameLower, form) {
			continue
		}
		for _, c := range conflicts {
			if containsWord(descLower, c) && !containsWord(nameLower, c) {
				return true
			}
		}
	}
	return false
}

// containsWord matches whole words only, so "dry" doesn't fire on "dryer"
// and "raw" doesn't fire on "strawberry".
func containsWord(s, word string) bool {
	for i := 0; i+len(word) <= len(s); i++ {
		if s[i:i+len(word)] != word {
			continue
		}
		if i > 0 && isWordChar(s[i-1]) {
			continue
		}
		if j := i + len(word); j < len(s) && isWordChar(s[j]) {
			continue
		}
		return true
	}
	return false
}

func isWordChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// SearchGenericFoods is like SearchFoods but restricted to USDA's
// "Foundation" and "SR Legacy" datasets - generic reference entries for raw/
// whole foods - and picks the first of several candidates that both (a)
// leads with the query's own core word as its primary noun (USDA
// descriptions are formatted "PrimaryFood, modifier, modifier" - a query for
// "Potatoes" matching "Bread, potato" has the wrong primary food entirely,
// even though "potato" appears in it) and (b) doesn't contain a
// processed/derivative-form or prepared-dish word the query itself didn't
// ask for. Used for linking pantry items (generic, canonical names) to
// nutrition data without per-item confirmation, not for ad-hoc
// consumption-log queries (which may genuinely mean a branded product and
// already go through their own confirmation path via SearchFoods).
//
// Item names sometimes carry a parenthetical or slash ("Provolone Cheese
// (sliced)", "Taco/Fajita Seasoning") that USDA's search endpoint rejects
// outright (400) rather than just ignoring - stripped before querying, but
// the full original name is still used for the descriptor/noun filtering
// above so a legitimate qualifier like "(canned)" still counts.
func (c *Client) SearchGenericFoods(ctx context.Context, itemName string) (*Food, error) {
	query := cleanQueryForSearch(itemName)
	foods, err := c.search(ctx, query, "Foundation,SR Legacy", 25)
	if err != nil || len(foods) == 0 {
		return nil, err
	}

	nameLower := strings.ToLower(itemName)
	// English food names are almost always modifier(s)-then-noun ("Heavy
	// Cream", "Fresh Ginger", "Bell Peppers", "Rice Vinegar") - the LAST
	// word is a much better guess at the defining food noun than the first,
	// which is usually just a modifier a first-word check would wrongly
	// require the description to lead with. If the query itself uses
	// USDA's own "Food, modifier" comma phrasing (common for a manual
	// override query like "milk, whole"), take the last word of the
	// pre-comma segment instead - otherwise the trailing modifier ends up
	// treated as the noun, which is backwards.
	nounPhrase := strings.ToLower(query)
	if j := strings.Index(nounPhrase, ","); j >= 0 {
		nounPhrase = nounPhrase[:j]
	}
	words := strings.Fields(nounPhrase)
	coreWord := ""
	if len(words) > 0 {
		coreWord = strings.TrimSuffix(words[len(words)-1], "s")
	}

	for i := range foods {
		descLower := strings.ToLower(foods[i].Description)
		if looksBranded(foods[i].Description) || !foods[i].HasProximates {
			continue
		}
		primaryNoun := descLower
		if j := strings.Index(primaryNoun, ","); j >= 0 {
			primaryNoun = primaryNoun[:j]
		}
		if coreWord != "" && !strings.Contains(primaryNoun, coreWord) && !strings.Contains(descLower, coreWord) {
			continue
		}
		if contradictsStatedForm(nameLower, descLower) {
			continue
		}
		clean := true
		for _, kw := range undesirableDescriptors {
			if strings.Contains(descLower, kw) && !strings.Contains(nameLower, kw) {
				clean = false
				break
			}
		}
		if clean {
			return &foods[i], nil
		}
	}
	// Nothing survived every filter: relax the primary-noun requirement
	// (still non-branded, still no undesirable descriptor) rather than
	// force a match through completely unfiltered. If even that finds
	// nothing, report no match (nil, not an error) - for something like a
	// specific spice blend Foundation/SR Legacy may just not have a decent
	// generic entry at all, and a bad match is worse than an honest miss
	// here, the same never-fabricate posture used throughout this project.
	for i := range foods {
		if looksBranded(foods[i].Description) || !foods[i].HasProximates {
			continue
		}
		descLower := strings.ToLower(foods[i].Description)
		if contradictsStatedForm(nameLower, descLower) {
			continue
		}
		clean := true
		for _, kw := range undesirableDescriptors {
			if strings.Contains(descLower, kw) && !strings.Contains(nameLower, kw) {
				clean = false
				break
			}
		}
		if clean {
			return &foods[i], nil
		}
	}
	return nil, nil
}

// looksBranded reports whether desc contains a token that reads like a
// brand name - USDA's Foundation/SR Legacy datasets are supposed to be
// generic reference data, but branded entries (SILK, CHOBANI, CAMPBELL'S,
// TACO BELL, MCDONALD'S, ...) still show up in practice. A run of 3+
// uppercase letters anywhere but the very start of the description is a
// decent signal - generic USDA descriptions are lowercase apart from
// capitalizing the first letter of a sentence.
func looksBranded(desc string) bool {
	upperRun := 0
	for i, r := range desc {
		if r >= 'A' && r <= 'Z' {
			upperRun++
			if upperRun >= 3 && i >= 3 {
				return true
			}
		} else {
			upperRun = 0
		}
	}
	return false
}

func cleanQueryForSearch(s string) string {
	if i := strings.Index(s, "("); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "/", " ")
	return strings.TrimSpace(s)
}

// SearchFoods returns the best (first) match for query against USDA's full
// index, or nil (not an error) if the search succeeded but found nothing -
// callers should fall back to a crude estimate in that case, not treat it
// as a failure.
func (c *Client) SearchFoods(ctx context.Context, query string) (*Food, error) {
	foods, err := c.search(ctx, query, "", searchFoodsPageSize)
	if err != nil || len(foods) == 0 {
		return nil, err
	}
	// Keep USDA's own ranking, but skip past a top hit that reported no
	// macro nutrients at all - it would land in the cache as a confident
	// row of zeroes. Falling back to the first result if none of them have
	// macros keeps the old behavior for a query where that's all there is.
	for i := range foods {
		if foods[i].HasProximates {
			return &foods[i], nil
		}
	}
	return &foods[0], nil
}

func (c *Client) search(ctx context.Context, query, dataType string, pageSize int) ([]Food, error) {
	params := url.Values{
		"query":    {query},
		"pageSize": {fmt.Sprint(pageSize)},
		"api_key":  {c.apiKey},
	}
	if dataType != "" {
		params.Set("dataType", dataType)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("building USDA request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling USDA search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("USDA search returned status %d: %s", resp.StatusCode, body)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading USDA response: %w", err)
	}

	var parsed searchResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decoding USDA response: %w", err)
	}

	foods := make([]Food, 0, len(parsed.Foods))
	for _, f := range parsed.Foods {
		oneFoodJSON, err := json.Marshal(f)
		if err != nil {
			return nil, fmt.Errorf("re-marshaling USDA food: %w", err)
		}
		parsedFood, err := f.toFood(string(oneFoodJSON))
		if err != nil {
			return nil, err
		}
		foods = append(foods, parsedFood)
	}
	return foods, nil
}
