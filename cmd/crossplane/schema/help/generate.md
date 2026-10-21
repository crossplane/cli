The `schema generate` command recreates schemas from the schema lock. It
restores generated files that are missing or were modified, using the exact
package digests and git commits the lock pins, and never changes the lock. It
never resolves versions again, and works offline when the packages are cached.

The command fails if there is no schema lock, or if a source or the project's
dependencies changed since the lock was written. Run `crossplane schema update`
to create the lock or accept the change.
