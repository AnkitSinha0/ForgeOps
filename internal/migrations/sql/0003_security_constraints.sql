-- 0003_security_constraints: database backstops for checks the Go code now makes
-- (security audit 2026-11-11). The Go checks give a clear 400; these make sure a
-- future code path that skips them still can't store the bad value.

-- A hostname is lowercase LDH labels (letters, digits, hyphen; no hyphen at either
-- end of a label), 1-63 characters each, at least two labels, 253 characters in all,
-- no trailing dot. Non-ASCII names must arrive as punycode (xn--...). Before this,
-- "shop.example.com." could be claimed next to another tenant's "shop.example.com",
-- because the unique index compares strings, not hosts.
ALTER TABLE domains ADD CONSTRAINT domains_hostname_shape CHECK (
    char_length(hostname) <= 253
    AND hostname ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
);

-- A succeeded build is one that produced an image. Without this, a build could be
-- marked succeeded with image_ref = '' and then deployed.
ALTER TABLE builds ADD CONSTRAINT builds_succeeded_has_image CHECK (
    status <> 'succeeded' OR image_ref <> ''
);

-- Failure reasons come from build output, so they are bounded (the Go side truncates).
ALTER TABLE builds ADD CONSTRAINT builds_error_bounded CHECK (octet_length(error) <= 4096);
