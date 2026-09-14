-- Per-item nutrition taken from a product label, and provenance for unit
-- conversion factors.
--
-- USDA is the right source for generic foods, but it has no entry at all for
-- many branded products in this pantry (a specific protein powder, a store
-- cereal), and the automatic match for those lands on something merely
-- similar. When the physical package is in hand its label is better data
-- than anything USDA can offer, so item_nutrition takes precedence over a
-- pantry item's fdc_id link wherever macros are computed.
CREATE TABLE item_nutrition (
  item_id INTEGER PRIMARY KEY REFERENCES pantry_items(id),
  calories_per_100g REAL NOT NULL,
  protein_per_100g REAL NOT NULL,
  carbs_per_100g REAL NOT NULL,
  fat_per_100g REAL NOT NULL,
  fiber_per_100g REAL NOT NULL,
  source TEXT NOT NULL CHECK (source IN ('label','patrick_provided')),
  note TEXT,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- A conversion factor is either something real was measured for (a package's
-- stated net weight, a scale) or a standard reference figure applied from
-- food knowledge (a cup of all-purpose flour weighing about 120 g). Both are
-- worth recording; conflating them is not, since only the second is worth
-- revisiting when a number looks off. Mirrors typical_shelf_life.source.
ALTER TABLE unit_conversions ADD COLUMN source TEXT NOT NULL DEFAULT 'patrick_provided';
