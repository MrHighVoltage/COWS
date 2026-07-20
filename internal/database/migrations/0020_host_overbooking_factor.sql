ALTER TABLE host_settings ADD COLUMN overbooking_factor REAL NOT NULL DEFAULT 1.0 CHECK (overbooking_factor >= 0.1 AND overbooking_factor <= 1000000.0);
