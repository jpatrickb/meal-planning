// Package recipesync fetches and caches recipes.patrickandthea.com's public
// recipe export. That site is the read-only source of truth for recipe
// content - this package only ever reads from it, never writes.
package recipesync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jpatrickb/meal-planning/internal/store"
)

// flexString unmarshals a JSON string, number, or null into a plain string
// ("" for null). The live site's `serves` field is usually a string like
// "4–6" but at least one recipe has it as a bare JSON number - this tolerates
// either without assuming a fixed type.
type flexString string

func (f *flexString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		*f = flexString(n.String())
		return nil
	}
	return fmt.Errorf("unsupported value %s for flexString", data)
}

const recipesURL = "https://recipes.patrickandthea.com/data/recipes.json"

type sitePayload struct {
	Recipes []siteRecipe `json:"recipes"`
}

type siteRecipe struct {
	ID               string                  `json:"id"`
	Title            string                  `json:"title"`
	Emoji            string                  `json:"emoji"`
	Category         string                  `json:"category"`
	Tags             []string                `json:"tags"`
	Serves           flexString              `json:"serves"`
	Image            string                  `json:"image"`
	RelatedRecipes   []string                `json:"relatedRecipes"`
	IngredientGroups []store.IngredientGroup `json:"ingredientGroups"`
	Instructions     []string                `json:"instructions"`
	Notes            []string                `json:"notes"`
	DateAdded        string                  `json:"dateAdded"`
}

// Result summarizes what a sync did.
type Result struct {
	Fetched int
	Synced  time.Time
}

// Fetch retrieves and parses the live recipe feed without touching the database.
func Fetch(ctx context.Context) ([]siteRecipe, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, recipesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", recipesURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("fetching %s: unexpected status %d: %s", recipesURL, resp.StatusCode, body)
	}

	var payload sitePayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding recipe feed: %w", err)
	}
	return payload.Recipes, nil
}

// Sync fetches the live feed and upserts every recipe into db in one
// transaction. Recipes that disappear from the feed are marked inactive
// (never deleted) so cook_events/consumption_log/recipe_meta referencing
// them stay intact.
func Sync(ctx context.Context, db *sql.DB) (Result, error) {
	recipes, err := Fetch(ctx)
	if err != nil {
		return Result{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := db.Begin()
	if err != nil {
		return Result{}, fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	if err := store.DeactivateMissingRecipes(tx); err != nil {
		return Result{}, fmt.Errorf("deactivating stale recipes: %w", err)
	}

	for _, sr := range recipes {
		raw, err := json.Marshal(sr)
		if err != nil {
			return Result{}, fmt.Errorf("marshaling recipe %s: %w", sr.ID, err)
		}
		r := store.Recipe{
			ID:               sr.ID,
			Title:            sr.Title,
			Emoji:            sr.Emoji,
			Category:         sr.Category,
			ServesRaw:        string(sr.Serves),
			ImageURL:         sr.Image,
			DateAdded:        sr.DateAdded,
			Tags:             orEmpty(sr.Tags),
			IngredientGroups: sr.IngredientGroups,
			Instructions:     orEmpty(sr.Instructions),
			Notes:            orEmpty(sr.Notes),
			RelatedRecipes:   orEmpty(sr.RelatedRecipes),
			RawJSON:          string(raw),
			SyncedAt:         now,
		}
		if err := store.UpsertRecipe(tx, r); err != nil {
			return Result{}, fmt.Errorf("upserting recipe %s: %w", sr.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("committing sync: %w", err)
	}

	synced, err := time.Parse(time.RFC3339, now)
	if err != nil {
		return Result{}, err
	}
	return Result{Fetched: len(recipes), Synced: synced}, nil
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
