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
- 80: a flag was unknown or had an invalid value, so nothing was simulated.
- 130: the simulation was interrupted.

When more than one applies, the lowest of 1, 2 and 3 wins.
