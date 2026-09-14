-- Widen item_nutrition.source to allow 'estimated'.
--
-- Some pantry items are spice or seasoning blends (garlic salt, garam
-- masala) that USDA simply has no entry for, and whose package isn't to
-- hand. Forcing a choice between a wrong USDA match and no data at all
-- makes a whole recipe's macros unknown over an ingredient contributing a
-- few calories. A reasonable figure, explicitly marked as an estimate,
-- is the same posture typical_shelf_life already takes.
CREATE TABLE item_nutrition_new (
  item_id INTEGER PRIMARY KEY REFERENCES pantry_items(id),
  calories_per_100g REAL NOT NULL,
  protein_per_100g REAL NOT NULL,
  carbs_per_100g REAL NOT NULL,
  fat_per_100g REAL NOT NULL,
  fiber_per_100g REAL NOT NULL,
  source TEXT NOT NULL CHECK (source IN ('label','patrick_provided','estimated')),
  note TEXT,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO item_nutrition_new SELECT * FROM item_nutrition;
DROP TABLE item_nutrition;
ALTER TABLE item_nutrition_new RENAME TO item_nutrition;
