-- Core schema for mealcli.
-- for the design rationale behind each table.

CREATE TABLE pantry_categories (
  code TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  sort_order INTEGER NOT NULL
);

INSERT INTO pantry_categories (code, label, sort_order) VALUES
  ('grains',           'Grains & Starches',            10),
  ('protein_meat',     'Proteins (Meat & Fish)',        20),
  ('protein_legume',   'Proteins (Legumes & Beans)',    30),
  ('protein_other',    'Proteins (Other)',              40),
  ('veg_fresh_frozen', 'Vegetables (Fresh & Frozen)',   50),
  ('veg_canned',       'Vegetables (Canned)',           60),
  ('fruit',            'Fruits & Fruit Products',       70),
  ('baking',           'Baking & Sweeteners',           80),
  ('oils_condiments',  'Oils & Condiments',             90),
  ('sauces',           'Sauces & Condiments',          100),
  ('dairy',            'Dairy & Cheese',               110),
  ('misc',             'Miscellaneous',                120);

CREATE TABLE pantry_items (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  category TEXT NOT NULL REFERENCES pantry_categories(code),
  default_unit TEXT NOT NULL,
  fdc_id INTEGER,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_pantry_items_category ON pantry_items(category);

-- Stock as lots (batches), not one mutable qty column: enables FIFO,
-- expiry warnings, and waste tracking against a specific purchase.
CREATE TABLE pantry_stock (
  id INTEGER PRIMARY KEY,
  item_id INTEGER NOT NULL REFERENCES pantry_items(id),
  quantity REAL NOT NULL,
  unit TEXT NOT NULL,
  unit_cost REAL,
  acquired_date TEXT NOT NULL,
  expires_date TEXT,
  source_purchase_item_id INTEGER REFERENCES purchase_items(id),
  status TEXT NOT NULL DEFAULT 'on_hand' CHECK (status IN ('on_hand','consumed','discarded'))
);
CREATE INDEX idx_pantry_stock_item ON pantry_stock(item_id);
CREATE INDEX idx_pantry_stock_status ON pantry_stock(status);

CREATE TABLE purchases (
  id INTEGER PRIMARY KEY,
  purchase_date TEXT NOT NULL,
  store TEXT,
  receipt_total REAL,
  raw_source TEXT,
  status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','finished'))
);

CREATE TABLE purchase_items (
  id INTEGER PRIMARY KEY,
  purchase_id INTEGER NOT NULL REFERENCES purchases(id),
  raw_text TEXT NOT NULL,
  upc TEXT,
  matched_item_id INTEGER REFERENCES pantry_items(id),
  quantity REAL,
  unit TEXT,
  unit_price REAL,
  line_total REAL,
  match_confidence REAL,
  match_method TEXT CHECK (match_method IN ('exact_alias','upc','fuzzy','manual','unmatched'))
);
CREATE INDEX idx_purchase_items_purchase ON purchase_items(purchase_id);

-- Receipt-line -> canonical item dictionary. Grows over time as purchases are logged.
CREATE TABLE item_aliases (
  id INTEGER PRIMARY KEY,
  raw_text_normalized TEXT NOT NULL UNIQUE,
  upc TEXT UNIQUE,
  item_id INTEGER NOT NULL REFERENCES pantry_items(id),
  confidence REAL,
  confirmed BOOLEAN NOT NULL DEFAULT 0,
  times_matched INTEGER NOT NULL DEFAULT 1,
  last_matched_at TIMESTAMP
);
CREATE INDEX idx_item_aliases_item ON item_aliases(item_id);

-- Read-through cache of recipes.patrickandthea.com/data/recipes.json.
-- Sync UPSERTs by id (the site's slug) and never delete-then-reinsert,
-- since that would cascade-wipe recipe_meta via the foreign key.
CREATE TABLE recipes (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  emoji TEXT,
  category TEXT,
  serves_raw TEXT,
  image_url TEXT,
  date_added TEXT,
  active BOOLEAN NOT NULL DEFAULT 1,
  tags_json TEXT NOT NULL DEFAULT '[]',
  ingredient_groups_json TEXT NOT NULL DEFAULT '[]',
  instructions_json TEXT NOT NULL DEFAULT '[]',
  notes_json TEXT NOT NULL DEFAULT '[]',
  related_recipes_json TEXT NOT NULL DEFAULT '[]',
  raw_json TEXT NOT NULL,
  synced_at TIMESTAMP NOT NULL
);

-- LOCAL, owned metadata. Sync never touches this table.
CREATE TABLE recipe_meta (
  recipe_id TEXT PRIMARY KEY REFERENCES recipes(id),
  serves_override INTEGER,
  servings_are_meals BOOLEAN NOT NULL DEFAULT 1,
  cost_total REAL,
  macros_per_serving_json TEXT,
  rating INTEGER CHECK (rating BETWEEN 1 AND 5),
  notes TEXT,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Leftover ledger: created on `meal cook`, decremented by `meal log --from-leftovers`.
CREATE TABLE cook_events (
  id INTEGER PRIMARY KEY,
  recipe_id TEXT NOT NULL REFERENCES recipes(id),
  cooked_date TEXT NOT NULL,
  servings_yielded REAL NOT NULL,
  servings_remaining REAL NOT NULL,
  total_cost REAL NOT NULL DEFAULT 0
);
CREATE INDEX idx_cook_events_recipe ON cook_events(recipe_id);

-- Core consumption log. consumed_date is always a single date, never a range
-- string (the old markdown system's bug we're structurally avoiding here).
CREATE TABLE consumption_log (
  id INTEGER PRIMARY KEY,
  consumed_date TEXT NOT NULL,
  event_group_id INTEGER,
  person TEXT NOT NULL CHECK (person IN ('patrick','thea')),
  meal_slot TEXT CHECK (meal_slot IN ('breakfast','lunch','dinner','snack')),
  source TEXT NOT NULL CHECK (source IN ('cooked','leftover','purchased_ready','eaten_out')),
  recipe_id TEXT REFERENCES recipes(id),
  cook_event_id INTEGER REFERENCES cook_events(id),
  free_text_food TEXT,
  servings REAL NOT NULL DEFAULT 1,
  cost REAL NOT NULL DEFAULT 0,
  calories REAL,
  protein REAL,
  carbs REAL,
  fat REAL,
  fiber REAL,
  macro_source TEXT CHECK (macro_source IN ('local_meta','usda','estimated'))
);
CREATE INDEX idx_consumption_log_date ON consumption_log(consumed_date);
CREATE INDEX idx_consumption_log_person ON consumption_log(person);
CREATE INDEX idx_consumption_log_cook_event ON consumption_log(cook_event_id);

CREATE TABLE nutrition_cache (
  fdc_id INTEGER PRIMARY KEY,
  description TEXT,
  query_text TEXT,
  calories_per_100g REAL,
  protein_per_100g REAL,
  carbs_per_100g REAL,
  fat_per_100g REAL,
  fiber_per_100g REAL,
  raw_json TEXT,
  fetched_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_nutrition_cache_query ON nutrition_cache(query_text);

CREATE TABLE waste_log (
  id INTEGER PRIMARY KEY,
  item_id INTEGER REFERENCES pantry_items(id),
  cook_event_id INTEGER REFERENCES cook_events(id),
  description TEXT,
  quantity REAL,
  unit TEXT,
  estimated_cost REAL,
  wasted_date TEXT NOT NULL,
  reason TEXT CHECK (reason IN ('spoiled','expired','too_much_cooked','disliked','other'))
);

CREATE TABLE meal_plan_entries (
  id INTEGER PRIMARY KEY,
  plan_date TEXT NOT NULL,
  meal_slot TEXT NOT NULL CHECK (meal_slot IN ('breakfast','lunch','dinner','snack')),
  recipe_id TEXT REFERENCES recipes(id),
  free_text_meal TEXT,
  planned_for TEXT NOT NULL DEFAULT 'both' CHECK (planned_for IN ('patrick','thea','both')),
  status TEXT NOT NULL DEFAULT 'planned' CHECK (status IN ('planned','cooked','skipped')),
  UNIQUE(plan_date, meal_slot)
);

CREATE TABLE targets (
  person TEXT PRIMARY KEY CHECK (person IN ('patrick','thea')),
  kcal REAL,
  protein REAL,
  carbs REAL,
  fat REAL
);

-- Aggregate views.

CREATE VIEW v_daily_totals AS
SELECT
  consumed_date,
  person,
  SUM(calories) AS total_calories,
  SUM(protein)  AS total_protein,
  SUM(carbs)    AS total_carbs,
  SUM(fat)      AS total_fat,
  SUM(cost)     AS total_cost
FROM consumption_log
GROUP BY consumed_date, person;

CREATE VIEW v_weekly_totals AS
SELECT
  strftime('%Y-W%W', consumed_date) AS iso_week,
  person,
  SUM(calories) AS total_calories,
  SUM(protein)  AS total_protein,
  SUM(carbs)    AS total_carbs,
  SUM(fat)      AS total_fat,
  SUM(cost)     AS total_cost,
  AVG(daily.total_calories) AS avg_daily_calories
FROM consumption_log
JOIN (
  SELECT consumed_date, person, SUM(calories) AS total_calories
  FROM consumption_log GROUP BY consumed_date, person
) AS daily USING (consumed_date, person)
GROUP BY iso_week, person;

CREATE VIEW v_recipe_variety AS
SELECT
  r.id AS recipe_id,
  r.title,
  rm.rating,
  COUNT(ce.id) AS times_cooked,
  MAX(ce.cooked_date) AS last_cooked_date,
  CAST(julianday('now') - julianday(MAX(ce.cooked_date)) AS INTEGER) AS days_since_cooked
FROM recipes r
LEFT JOIN recipe_meta rm ON rm.recipe_id = r.id
LEFT JOIN cook_events ce ON ce.recipe_id = r.id
WHERE r.active = 1
GROUP BY r.id;

CREATE VIEW v_pantry_expiring AS
SELECT
  ps.id AS stock_id,
  pi.name AS item_name,
  pi.category,
  ps.quantity,
  ps.unit,
  ps.expires_date,
  CAST(julianday(ps.expires_date) - julianday('now') AS INTEGER) AS days_until_expiry
FROM pantry_stock ps
JOIN pantry_items pi ON pi.id = ps.item_id
WHERE ps.status = 'on_hand' AND ps.expires_date IS NOT NULL
ORDER BY ps.expires_date ASC;

CREATE VIEW v_waste_summary AS
SELECT
  strftime('%Y-%m', wasted_date) AS month,
  reason,
  COUNT(*) AS occurrences,
  SUM(estimated_cost) AS total_cost
FROM waste_log
GROUP BY month, reason;
