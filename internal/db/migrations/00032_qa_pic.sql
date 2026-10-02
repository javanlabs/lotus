-- +goose Up
-- PIC (person in charge) of a section and of a case, both optional. The
-- design dropped legacy pic_id as 0% filled in the 2026-09-26 dump; QA
-- Tools has used it since (2026-09-04), so it is back. A case without its
-- own PIC is in the charge of its nearest ancestor section's PIC.
-- Plain user references, like every *_by column (control plane, no org).
ALTER TABLE qa_sections ADD COLUMN pic_id uuid REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE qa_cases ADD COLUMN pic_id uuid REFERENCES users (id) ON DELETE SET NULL;
-- The case list filters on PIC, and deleting a user sets these to NULL.
CREATE INDEX qa_sections_pic_id_idx ON qa_sections (pic_id) WHERE pic_id IS NOT NULL;
CREATE INDEX qa_cases_pic_id_idx ON qa_cases (pic_id) WHERE pic_id IS NOT NULL;

-- +goose Down
ALTER TABLE qa_cases DROP COLUMN IF EXISTS pic_id;
ALTER TABLE qa_sections DROP COLUMN IF EXISTS pic_id;
