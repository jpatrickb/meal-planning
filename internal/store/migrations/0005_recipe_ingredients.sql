-- Recipe-to-pantry-item ingredient mapping. See
-- docs/requirements-ingredient-costing.md for the full design.

-- One row per raw ingredient line on a recipe, not per pantry item, so
-- completeness (unmapped / partial / complete, doc §4) can be derived by
-- comparing row count against the recipe's actual ingredient line count.
CREATE TABLE recipe_ingredients (
  id INTEGER PRIMARY KEY,
  recipe_id TEXT NOT NULL REFERENCES recipes(id),
  ingredient_group TEXT,
  raw_ingredient_text TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('mapped','not_tracked')),
  item_id INTEGER REFERENCES pantry_items(id),
  quantity REAL,
  unit TEXT,
  quantity_source TEXT CHECK (quantity_source IN ('recipe_stated','estimated')),
  confirmed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (
    (status = 'not_tracked' AND item_id IS NULL AND quantity IS NULL AND unit IS NULL AND quantity_source IS NULL)
    OR
    (status = 'mapped' AND item_id IS NOT NULL AND quantity IS NOT NULL AND unit IS NOT NULL AND quantity_source IS NOT NULL)
  )
);
CREATE INDEX idx_recipe_ingredients_recipe ON recipe_ingredients(recipe_id);
CREATE INDEX idx_recipe_ingredients_item ON recipe_ingredients(item_id);

-- Per-cook-event actuals: one row per pantry lot actually decremented, since
-- FIFO consumption of a single ingredient can span multiple lots. Not
-- written to until pantry-decrement-on-cook lands, but modeled now since it
-- shares the same design pass and schema.
CREATE TABLE cook_event_ingredients (
  id INTEGER PRIMARY KEY,
  cook_event_id INTEGER NOT NULL REFERENCES cook_events(id),
  item_id INTEGER NOT NULL REFERENCES pantry_items(id),
  pantry_stock_id INTEGER NOT NULL REFERENCES pantry_stock(id),
  quantity REAL NOT NULL,
  unit TEXT NOT NULL,
  source TEXT NOT NULL CHECK (source IN ('recipe_default','override'))
);
CREATE INDEX idx_cook_event_ingredients_cook_event ON cook_event_ingredients(cook_event_id);
CREATE INDEX idx_cook_event_ingredients_item ON cook_event_ingredients(item_id);

-- Real unit conversion. item_id NULL = universal (e.g. cup -> ml); a
-- non-NULL item_id is a density/size-specific factor for that one pantry
-- item (a cup of flour vs. a cup of milk weigh differently; a carrot's
-- average weight is item-specific). quantity(to_unit) = quantity(from_unit) * factor.
-- The UNIQUE index below does not actually dedupe universal (item_id NULL)
-- rows -- SQLite treats NULLs as distinct in a unique index -- so the store
-- layer checks-then-inserts for that case itself.
CREATE TABLE unit_conversions (
  id INTEGER PRIMARY KEY,
  item_id INTEGER REFERENCES pantry_items(id),
  from_unit TEXT NOT NULL,
  to_unit TEXT NOT NULL,
  factor REAL NOT NULL,
  confirmed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(item_id, from_unit, to_unit)
);
