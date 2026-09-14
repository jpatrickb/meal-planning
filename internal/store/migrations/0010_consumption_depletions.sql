-- Which pantry lots a consumption entry drew from.
--
-- `meal log --deplete-pantry` recorded the cost it computed but not where
-- the stock came from, so voiding an entry could not give the stock back:
-- the note on `log void` told the user to work out the quantities and
-- replay them by hand with `pantry adjust`, which is error-prone and left
-- invented lots behind. cook_event_ingredients has always recorded exactly
-- this for a cook; this is the same ledger for an ad-hoc log entry.
--
-- Quantity and unit are the LOT's own, matching cook_event_ingredients and
-- pantry_stock, so restoring is a plain addition with no conversion to get
-- wrong on the way back.
CREATE TABLE consumption_depletions (
  id INTEGER PRIMARY KEY,
  consumption_log_id INTEGER NOT NULL REFERENCES consumption_log(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES pantry_items(id),
  pantry_stock_id INTEGER NOT NULL REFERENCES pantry_stock(id),
  quantity REAL NOT NULL,
  unit TEXT NOT NULL
);
CREATE INDEX idx_consumption_depletions_log ON consumption_depletions(consumption_log_id);
CREATE INDEX idx_consumption_depletions_stock ON consumption_depletions(pantry_stock_id);
