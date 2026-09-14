-- Adds 'from_pantry' as a valid consumption_log.source: ad-hoc consumption of
-- a raw pantry ingredient eaten as-is (a banana, a slice of bread), as
-- opposed to 'purchased_ready' (a ready-to-eat item never stored in the
-- pantry) or 'cooked'/'leftover' (goes through a recipe). `meal log
-- --deplete-pantry` uses this source and decrements real pantry stock in the
-- same transaction, closing the gap where that previously required a
-- separate manual `meal pantry adjust` call.
--
-- SQLite can't ALTER a CHECK constraint in place, so the table is rebuilt.
-- The dependent views are dropped up front, before the table swap, rather
-- than after -- dropping a table out from under a still-defined view errors
-- immediately on this driver.

DROP VIEW v_daily_totals;
DROP VIEW v_weekly_totals;

CREATE TABLE consumption_log_new (
  id INTEGER PRIMARY KEY,
  consumed_date TEXT NOT NULL,
  event_group_id INTEGER,
  person TEXT NOT NULL CHECK (person IN ('patrick','thea')),
  meal_slot TEXT CHECK (meal_slot IN ('breakfast','lunch','dinner','snack')),
  source TEXT NOT NULL CHECK (source IN ('cooked','leftover','purchased_ready','eaten_out','from_pantry')),
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

INSERT INTO consumption_log_new SELECT * FROM consumption_log;

DROP TABLE consumption_log;
ALTER TABLE consumption_log_new RENAME TO consumption_log;

CREATE INDEX idx_consumption_log_date ON consumption_log(consumed_date);
CREATE INDEX idx_consumption_log_person ON consumption_log(person);
CREATE INDEX idx_consumption_log_cook_event ON consumption_log(cook_event_id);

CREATE VIEW v_daily_totals AS
SELECT
  consumed_date,
  person,
  COALESCE(SUM(calories), 0) AS total_calories,
  COALESCE(SUM(protein), 0)  AS total_protein,
  COALESCE(SUM(carbs), 0)    AS total_carbs,
  COALESCE(SUM(fat), 0)      AS total_fat,
  SUM(cost)                  AS total_cost
FROM consumption_log
GROUP BY consumed_date, person;

CREATE VIEW v_weekly_totals AS
SELECT
  strftime('%Y-W%W', consumed_date) AS iso_week,
  person,
  COALESCE(SUM(calories), 0) AS total_calories,
  COALESCE(SUM(protein), 0)  AS total_protein,
  COALESCE(SUM(carbs), 0)    AS total_carbs,
  COALESCE(SUM(fat), 0)      AS total_fat,
  SUM(cost)                  AS total_cost,
  AVG(daily.total_calories)  AS avg_daily_calories
FROM consumption_log
JOIN (
  SELECT consumed_date, person, COALESCE(SUM(calories), 0) AS total_calories
  FROM consumption_log GROUP BY consumed_date, person
) AS daily USING (consumed_date, person)
GROUP BY iso_week, person;
