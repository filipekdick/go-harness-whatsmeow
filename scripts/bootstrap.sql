\set ON_ERROR_STOP on

\if :{?company_name}
\else
\echo 'missing psql variable company_name'
\quit 1
\endif

\if :{?system_prompt}
\else
\echo 'missing psql variable system_prompt'
\quit 1
\endif

BEGIN;

INSERT INTO companies (name, system_prompt)
SELECT :'company_name', :'system_prompt'
WHERE NOT EXISTS (
    SELECT 1
    FROM companies
    WHERE name = :'company_name' AND is_active
);

SELECT id AS company_id
FROM companies
WHERE name = :'company_name' AND is_active
ORDER BY id
LIMIT 1
\gset bootstrap_

UPDATE companies
SET system_prompt = :'system_prompt', updated_at = now()
WHERE id = :bootstrap_company_id;

INSERT INTO wa_channels (company_id, channel)
VALUES (:bootstrap_company_id, 'CUSTOMER')
ON CONFLICT (company_id, channel) DO UPDATE
SET is_active = TRUE, archived_at = NULL;

COMMIT;

\echo 'company/customer channel ready; company_id=' :bootstrap_company_id
