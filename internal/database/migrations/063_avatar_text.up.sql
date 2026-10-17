-- Avatars are stored as data URLs / IPFS hashes on the user row (no
-- dedicated file host exists). VARCHAR(64) only fits an IPFS multihash, so
-- widen to TEXT for resized data-URL avatars from the profile uploader.
ALTER TABLE users
    ALTER COLUMN avatar_ipfs_hash TYPE TEXT;
