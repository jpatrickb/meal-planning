-- SUM() over an all-NULL group returns NULL, not 0 - hit for real the first
-- time a recipe with no macros set (calories/protein/carbs/fat are nullable
-- columns) got cooked: v_daily_totals.total_calories came back NULL, and
-- GetMacroAnalytics's outer AVG(NULL) couldn't scan into a plain float64.
-- COALESCE at the view level fixes it for every consumer at once, not just
-- the one query that happened to crash first. cost is untouched - it's
-- NOT NULL DEFAULT 0 at the column level, so SUM(cost) is never NULL.

DROP VIEW v_daily_totals;
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

DROP VIEW v_weekly_totals;
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
