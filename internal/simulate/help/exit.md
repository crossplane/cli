The command exits with one of these statuses, so a CI pipeline can gate on
the result:

- 0: the simulation succeeded and found no changes.
- 1: the simulation failed, including when its inputs can't be read. If a
  provider plan fails, so does its whole composite resource, which then
  reports an error entry and none of its resources' plans. A run also fails,
  with or without `--skip-plan`, when two composite resources it renders would
  both control one resource, even if one of them leaves it unchanged, or
  render it differently, since no one diff or provider plan is correct for
  both.
- 2: only schema validation failed.
- 3: the simulation succeeded and found changes, including drift a provider
  plan found that the cluster diff didn't.
- 4: the simulation succeeded, found no changes, and a composite resource or
  a resource it composes was failing (Synced=False) before apply, including
  one the change would delete. A resource that's only not ready, or is
  paused, doesn't set it.
- 7: the simulation succeeded, found changes, and a resource was failing:
  3 and 4 combined.
- 80: a flag was unknown or had an invalid value, so nothing was simulated.
- 130: the simulation was interrupted.

An error wins over what the simulation found: 130 over 1 over 2, and any of
them over 3, 4 and 7. Without an error, the status adds 4 for a failing
resource to 3 for changes, so 3 and 7 both mean changes, and 4 and 7 both mean
a failing resource.
