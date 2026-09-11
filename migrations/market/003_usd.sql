-- Single currency is USD (cents), matching switch. Existing rows were only
-- ever demo data in the old default; relabel them so the column agrees.
ALTER TABLE listings ALTER COLUMN currency SET DEFAULT 'USD';
UPDATE listings SET currency = 'USD' WHERE currency <> 'USD';

-- Seed listings got new (unbranded) items and image names. The seed skips
-- when listings exist, so swap the old rows in place; a no-op on a fresh DB.
UPDATE listings SET title='Vintage 35mm film SLR camera', description='Fully mechanical, recently serviced. Light seals replaced.', image_url='/seed/film_slr_camera.jpg' WHERE image_url LIKE '/seed/nikon_fm2_camera_%';
UPDATE listings SET title='Navy crew neck tee (M)', price_minor=1200, image_url='/seed/navy_tee.jpg' WHERE image_url LIKE '/seed/uniqlo_navy_tee_%';
UPDATE listings SET image_url='/seed/design_everyday_things.jpg' WHERE image_url LIKE '/seed/design_everyday_things_%';
UPDATE listings SET title='Gooseneck electric kettle (white)', image_url='/seed/gooseneck_kettle.jpg' WHERE image_url LIKE '/seed/balmuda_kettle_%';
UPDATE listings SET title='Wooden chess set (weighted pieces)', description='Complete set, felt-bottomed. Board folds for storage.', image_url='/seed/chess_set.jpg' WHERE image_url LIKE '/seed/gundam_rx782_%';
UPDATE listings SET title='Wireless noise-cancelling headphones', image_url='/seed/wireless_headphones.jpg' WHERE image_url LIKE '/seed/sony_headphones_%';
UPDATE listings SET image_url='/seed/levis_501.jpg' WHERE image_url LIKE '/seed/levis_501_%';
UPDATE listings SET title='Dune (paperback)', description='Frank Herbert. English paperback, good condition.', image_url='/seed/dune_paperback.jpg' WHERE image_url LIKE '/seed/norwegian_wood_book_%';
UPDATE listings SET title='Oak desk lamp', image_url='/seed/oak_desk_lamp.jpg' WHERE image_url LIKE '/seed/muji_desk_lamp_%';
UPDATE listings SET title='11-speed rear derailleur', image_url='/seed/rear_derailleur.jpg' WHERE image_url LIKE '/seed/shimano_derailleur_%';
UPDATE listings SET image_url='/seed/ipad_9th_gen.jpg' WHERE image_url LIKE '/seed/ipad_9th_gen_%';
UPDATE listings SET title='Vintage automatic watch', image_url='/seed/automatic_watch.jpg' WHERE image_url LIKE '/seed/seiko_5_watch_%';
UPDATE listings SET title=replace(title, 'Sony WH-1000XM4 headphones', 'Wireless noise-cancelling headphones') WHERE title LIKE '%Sony WH-1000XM4%';
