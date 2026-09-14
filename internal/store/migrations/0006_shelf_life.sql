-- Typical shelf-life estimates, used to auto-set pantry_stock.expires_date
-- at purchase time so Patrick doesn't have to supply an expiration date for
-- every item. Patrick seeds some values directly; anything else gets a
-- reasonable estimate from Claude's own food-safety judgment (source
-- 'estimated'), same posture as the nutrition/unit judgment calls already
-- made throughout this system.
CREATE TABLE typical_shelf_life (
  item_id INTEGER PRIMARY KEY REFERENCES pantry_items(id),
  days INTEGER NOT NULL,
  source TEXT NOT NULL CHECK (source IN ('patrick_provided','estimated')),
  confirmed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
