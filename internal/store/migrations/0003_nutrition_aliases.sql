-- Maps a free-text food query to a canonical nutrition_cache entry (by
-- fdc_id), mirroring the item_aliases/pantry_items separation: nutrition_cache
-- stays the canonical per-food data, this table is just "which query texts
-- mean the same food." Every exact query_text is either self-confirmed (the
-- text that originally produced this fdc_id via USDA) or explicitly
-- confirmed via `meal log --reuse-fdc-id` - never written automatically from
-- a fuzzy guess.
CREATE TABLE nutrition_query_aliases (
  query_text TEXT PRIMARY KEY,
  fdc_id INTEGER NOT NULL REFERENCES nutrition_cache(fdc_id),
  confirmed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_nutrition_query_aliases_fdc ON nutrition_query_aliases(fdc_id);
