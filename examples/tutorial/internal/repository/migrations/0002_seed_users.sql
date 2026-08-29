-- Two demo accounts, both with password "password123" (bcrypt-hashed for
-- real, not a placeholder string) -- see docs/tutorial/09-authentication.md.
-- A real service would never ship credentials in a migration; this exists
-- purely so the tutorial's login endpoint has something to authenticate
-- against without a signup flow, which is explicitly out of scope.
INSERT INTO users (id, username, password_hash) VALUES
    ('11111111-1111-1111-1111-111111111111', 'alice', '$2a$10$7zAE.OJ3lyD/3Eq4T2XMV.zU162KDzXOQnpHXE/kkd00GSQ.V4I3.'),
    ('22222222-2222-2222-2222-222222222222', 'bob',   '$2a$10$SHJKrTmOvH8SK6p6gGxu9e3.d7Z3DuDbhHOFQ22G0E5zz0GEj3YM.');
