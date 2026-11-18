## Summary

## Checklist

- [ ] Migrations are expand-only, or the file is marked `-- migration: contract` (and the flag is confirmed fully on in production). N/A if no migrations.
- [ ] OpenAPI spec changed → ran `make generate` and committed the generated code. N/A if the spec is untouched.
