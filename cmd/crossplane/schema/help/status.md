The `schema status` command lists generated schema files that are missing or
differ from the schema lock. It only reads the schemas directory, so it works
offline, and it exits with an error if any file differs.

It doesn't check whether sources changed since the lock was written. Run
`crossplane schema update` for that.
