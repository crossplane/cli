The `schema update` command generates schemas for the current project's APIs
(XRDs) and dependencies, and records what it generated in the schema lock
(`.lock.json` in the schemas directory). It is the command to run during
development, after changing an XRD or the project's dependencies.

Sources whose content did not change are skipped. Schemas of dependencies that
are no longer used are removed. Set `spec.schemas.apiGroups` in the project file
to generate schemas only for matching API groups.

Versions already in the schema lock are preferred, as long as they still satisfy
the version constraints of your dependencies. Use `--refresh` to ignore the
locked versions, resolve to the newest versions that are allowed, and pull them
again.

When several packages require the same dependency, a dependency you declare in
the project always wins. If its version violates a constraint of another
package, the command prints a warning and uses it anyway. Otherwise it picks the
highest version that satisfies every constraint, and fails if there is none.

`crossplane project build` only runs an update if there is no schema lock. After
that it never changes the lock, and only recreates missing schemas or fails if
they no longer match the lock.
