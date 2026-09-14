-- Shopping list: a curated checklist, not a fully auto-derived one.
-- `meal shopping-list build` seeds it from planned recipes' raw ingredient
-- text (recipes.json has no structured quantities to net against pantry
-- stock), and Claude is expected to consolidate/edit it conversationally via
-- `add`/`check`/`clear` from there - "Claude decides, the CLI persists."
CREATE TABLE shopping_list_items (
  id INTEGER PRIMARY KEY,
  description TEXT NOT NULL,
  source_recipe_id TEXT REFERENCES recipes(id),
  checked BOOLEAN NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_shopping_list_items_recipe ON shopping_list_items(source_recipe_id);
